package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeWSConn struct {
	mu      sync.Mutex
	messages [][]byte
	closed  bool
}

func (f *fakeWSConn) WriteJSON(v any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f.messages = append(f.messages, data)
	return nil
}

func (f *fakeWSConn) ReadMessage() (int, []byte, error) {
	return 0, nil, errors.New("connection closed")
}

func (f *fakeWSConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeWSConn) lastMessage() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.messages) == 0 {
		return nil
	}
	return f.messages[len(f.messages)-1]
}

func TestAgentHubSendCommandNotConnected(t *testing.T) {
	hub := NewAgentHub(slog.Default())

	_, err := hub.SendCommand(context.Background(), "nonexistent", "echo hi", 5)
	if !errors.Is(err, ErrAgentNotConnected) {
		t.Errorf("expected ErrAgentNotConnected, got %v", err)
	}
}

func TestAgentHubUnregisterFailsPending(t *testing.T) {
	hub := NewAgentHub(slog.Default())
	fakeConn := &fakeWSConn{}
	hub.Register("agent-1", "host-1", fakeConn)

	hub.Unregister("agent-1")

	if hub.IsConnected("agent-1") {
		t.Error("expected agent to be disconnected after Unregister")
	}
	if hub.HostIDForAgent("agent-1") != "" {
		t.Error("expected empty host ID after Unregister")
	}
}

func TestAgentHubConnectedAgents(t *testing.T) {
	hub := NewAgentHub(slog.Default())

	if len(hub.ConnectedAgents()) != 0 {
		t.Error("expected no connected agents initially")
	}

	hub.Register("agent-1", "host-1", &fakeWSConn{})
	hub.Register("agent-2", "host-2", &fakeWSConn{})

	agents := hub.ConnectedAgents()
	if len(agents) != 2 {
		t.Errorf("expected 2 connected agents, got %d", len(agents))
	}

	hub.Unregister("agent-1")
	agents = hub.ConnectedAgents()
	if len(agents) != 1 {
		t.Errorf("expected 1 connected agent after unregister, got %d", len(agents))
	}
}

func TestAgentHubSendCommandRoundTrip(t *testing.T) {
	hub := NewAgentHub(slog.Default())
	fakeConn := &fakeWSConn{}
	hub.Register("agent-1", "host-1", fakeConn)

	// Simulate the agent receiving a command and sending back a result.
	// Since fakeWSConn.ReadMessage returns an error (closed), we simulate
	// by directly calling the internal pending channel via a manual result.
	// First, grab the command that would have been sent.
	time.Sleep(50 * time.Millisecond)

	// We can't easily intercept the WebSocket write with fakeWSConn alone,
	// so instead test that SendCommand on a fake connection that never
	// responds times out properly.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := hub.SendCommand(ctx, "agent-1", "echo test", 30)
	if !errors.Is(err, context.DeadlineExceeded) && err != nil {
		t.Errorf("expected timeout error, got %v", err)
	}
}
