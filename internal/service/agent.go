package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"1mate/internal/model"
)

// AgentCommand is the server→agent message framing over the Agent WebSocket.
type AgentCommand struct {
	ID     string `json:"cmd_id"`
	Cmd    string `json:"cmd"`
	Timeout int   `json:"timeout"` // seconds
}

// AgentResult is the agent→server message framing over the Agent WebSocket.
type AgentResult struct {
	ID       string `json:"cmd_id"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Error    string `json:"error"`
}

const (
	// ConnTypeSSH marks a host reached via agentless SSH.
	ConnTypeSSH = model.ConnTypeSSH
	// ConnTypeAgent marks a host reached via an Agent client.
	ConnTypeAgent = model.ConnTypeAgent
)

// ErrAgentNotConnected is returned when no Agent WebSocket connection exists for a host.
var ErrAgentNotConnected = errors.New("agent not connected")

// agentConn is the minimal interface an Agent WebSocket connection must satisfy.
type agentConn interface {
	WriteJSON(v any) error
	ReadMessage() (int, []byte, error)
	Close() error
}

// agentConnection holds a single Agent WebSocket connection and its pending command results.
type agentConnection struct {
	conn      agentConn
	agentID   string
	hostID    string
	pending   map[string]chan *AgentResult
	pendingMu sync.Mutex
	done      chan struct{}
	once      sync.Once
}

// AgentHub manages active Agent WebSocket connections keyed by agent ID.
// It is used by the Service layer to send commands to and receive results from
// connected Agent clients over WebSocket.
type AgentHub struct {
	mu    sync.Mutex
	conns map[string]*agentConnection // agentID -> agentConnection
	log   *slog.Logger
}

// NewAgentHub creates an AgentHub with no active connections.
func NewAgentHub(log *slog.Logger) *AgentHub {
	return &AgentHub{
		conns: make(map[string]*agentConnection),
		log:   log,
	}
}

// Register associates an Agent WebSocket connection with an agent ID and host ID.
// If a connection already exists for the same agent ID, the old one is closed.
func (h *AgentHub) Register(agentID, hostID string, conn agentConn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if old, ok := h.conns[agentID]; ok {
		old.once.Do(func() { close(old.done) })
		old.conn.Close()
	}

	ac := &agentConnection{
		conn:    conn,
		agentID: agentID,
		hostID:  hostID,
		pending: make(map[string]chan *AgentResult),
		done:    make(chan struct{}),
	}
	h.conns[agentID] = ac

	// Start the read loop that dispatches incoming AgentResult frames to pending commands.
	go h.readLoop(ac)

	h.log.Debug("agent registered", "agent_id", agentID, "host_id", hostID)
}

// Unregister removes and closes an Agent WebSocket connection.
func (h *AgentHub) Unregister(agentID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	ac, ok := h.conns[agentID]
	if !ok {
		return
	}
	delete(h.conns, agentID)
	ac.once.Do(func() { close(ac.done) })
	_ = ac.conn.Close()

	// Fail all pending commands for this connection.
	ac.pendingMu.Lock()
	for id, ch := range ac.pending {
		select {
		case ch <- &AgentResult{ID: id, Error: "agent disconnected"}:
		default:
		}
		delete(ac.pending, id)
	}
	ac.pendingMu.Unlock()

	h.log.Debug("agent unregistered", "agent_id", agentID)
}

// SendCommand sends a command to the Agent identified by agentID and waits for
// the result. Returns ErrAgentNotConnected if no Agent connection exists.
func (h *AgentHub) SendCommand(ctx context.Context, agentID string, cmd string, timeoutSeconds int) (*AgentResult, error) {
	h.mu.Lock()
	ac, ok := h.conns[agentID]
	h.mu.Unlock()
	if !ok {
		return nil, ErrAgentNotConnected
	}

	cmdID := fmt.Sprintf("cmd-%d", time.Now().UnixNano())
	resultCh := make(chan *AgentResult, 1)

	ac.pendingMu.Lock()
	ac.pending[cmdID] = resultCh
	ac.pendingMu.Unlock()

	defer func() {
		ac.pendingMu.Lock()
		delete(ac.pending, cmdID)
		ac.pendingMu.Unlock()
	}()

	command := AgentCommand{
		ID:        cmdID,
		Cmd:       cmd,
		Timeout:   timeoutSeconds,
	}

	if err := ac.conn.WriteJSON(command); err != nil {
		h.log.Error("agent send command failed", "agent_id", agentID, "cmd_id", cmdID, "error", err)
		h.Unregister(agentID)
		return nil, fmt.Errorf("agent send command: %w", err)
	}

	select {
	case result := <-resultCh:
		return result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Duration(timeoutSeconds) * time.Second):
		return nil, fmt.Errorf("agent command timeout after %d seconds", timeoutSeconds)
	}
}

// readLoop reads messages from the Agent WebSocket and dispatches AgentResult
// frames to the matching pending command channel. It runs in its own goroutine
// per connection and exits when the connection closes.
func (h *AgentHub) readLoop(ac *agentConnection) {
	defer ac.once.Do(func() { close(ac.done) })

	for {
		_, msg, err := ac.conn.ReadMessage()
		if err != nil {
			h.log.Debug("agent read loop exiting", "agent_id", ac.agentID, "error", err)
			return
		}

		var result AgentResult
		if err := json.Unmarshal(msg, &result); err != nil {
			h.log.Error("agent result decode failed", "agent_id", ac.agentID, "error", err)
			continue
		}

		ac.pendingMu.Lock()
		ch, ok := ac.pending[result.ID]
		ac.pendingMu.Unlock()
		if !ok {
			h.log.Debug("agent result for unknown command", "agent_id", ac.agentID, "cmd_id", result.ID)
			continue
		}

		select {
		case ch <- &result:
		default:
			h.log.Warn("agent result channel full", "agent_id", ac.agentID, "cmd_id", result.ID)
		}
	}
}

// HostIDForAgent returns the host ID associated with an agent ID, or "" if not connected.
func (h *AgentHub) HostIDForAgent(agentID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ac, ok := h.conns[agentID]; ok {
		return ac.hostID
	}
	return ""
}

// ConnectedAgents returns the IDs of all currently connected Agents.
func (h *AgentHub) ConnectedAgents() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.conns))
	for id := range h.conns {
		out = append(out, id)
	}
	return out
}

// IsConnected reports whether an Agent is currently connected.
func (h *AgentHub) IsConnected(agentID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.conns[agentID]
	return ok
}
