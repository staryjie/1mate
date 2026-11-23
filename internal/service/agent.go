package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"1mate/internal/model"
)

// Agent 通道帧类型。所有帧都是 JSON 文本帧，二进制内容以 base64 放在 b64 字段，
// 这样既避免单条命令过长（Linux 单参数上限约 128KB），又不需要二进制帧协商。
const (
	frameHello     = "hello"
	frameCmd       = "cmd"
	frameOut       = "out"
	frameResult    = "result"
	frameCancel    = "cancel"
	frameUpBegin   = "up_begin"
	frameUpChunk   = "up_chunk"
	frameUpEnd     = "up_end"
	frameUpAck     = "up_ack"
	frameUpDone    = "up_done"
	frameUpError   = "up_error"
	frameDownBegin = "down_begin"
	frameDownChunk = "down_chunk"
	frameDownEnd   = "down_end"
	framePong      = "pong"
)

// 传输分块大小：base64 后约 43KB，远低于 128KB 的单参数上限。
const agentChunkSize = 32 * 1024

// wireFrame 是 Agent 通道的统一帧格式。
type wireFrame struct {
	Type string `json:"type"`

	// hello（Agent → 平台）
	Version string `json:"version,omitempty"`
	OS      string `json:"os,omitempty"`
	Arch    string `json:"arch,omitempty"`
	IP      string `json:"ip,omitempty"`

	// cmd（平台 → Agent）
	CmdID        string `json:"cmd_id,omitempty"`
	Cmd          string `json:"cmd,omitempty"`
	Stdin        string `json:"stdin,omitempty"`
	SudoPassword string `json:"sudo_password,omitempty"`
	Timeout      int    `json:"timeout,omitempty"`

	// out（Agent → 平台，流式输出）
	Stream string `json:"stream,omitempty"` // stdout | stderr
	Data   string `json:"data,omitempty"`   // 明文增量

	// result（Agent → 平台）
	ExitCode int    `json:"exit_code,omitempty"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	Error    string `json:"error,omitempty"`

	// 文件传输（双向）
	XferID string `json:"xfer_id,omitempty"`
	Path   string `json:"path,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Seq    int    `json:"seq,omitempty"`
	B64    string `json:"b64,omitempty"`
}

// AgentResult is the agent→server command result.
type AgentResult struct {
	ID       string `json:"cmd_id"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Error    string `json:"error"`
}

// AgentCommand is kept for compatibility with earlier framing.
type AgentCommand struct {
	ID      string `json:"cmd_id"`
	Cmd     string `json:"cmd"`
	Timeout int    `json:"timeout"`
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

// cmdOutcome 把「命令结果」与「通道级错误」区分开：连接断开属于后者，
// 不能伪装成一条执行失败的命令（否则调用方拿到 nil error 会误判成功）。
type cmdOutcome struct {
	res *AgentResult
	err error
}

// cmdWaiter 等待一条命令的最终结果，并可选接收流式输出。
type cmdWaiter struct {
	ch    chan cmdOutcome
	onOut func(stream, text string)
}

// xferWaiter 等待一次文件传输的控制帧。
type xferWaiter struct {
	ch chan wireFrame
}

// agentConnection holds a single Agent WebSocket connection and its waiters.
type agentConnection struct {
	conn    agentConn
	agentID string
	hostID  string

	// writeMu 保证同一时刻只有一个写者：gorilla/websocket 的 WriteMessage
	// 内部不加锁，并发写会交错破坏帧。
	writeMu sync.Mutex

	mu   sync.Mutex
	cmds map[string]*cmdWaiter
	xfer map[string]*xferWaiter

	version string
	osName  string
	arch    string
	ip      string

	done chan struct{}
	once sync.Once
}

// send 串行化写入。
func (ac *agentConnection) send(f wireFrame) error {
	ac.writeMu.Lock()
	defer ac.writeMu.Unlock()
	return ac.conn.WriteJSON(f)
}

// AgentHub manages active Agent WebSocket connections keyed by agent ID.
type AgentHub struct {
	mu    sync.Mutex
	conns map[string]*agentConnection
	log   *slog.Logger

	// onHello 在 Agent 发送 hello 帧（携带 IP、版本、架构）时被调用。
	// Service 通过此回调将上报的 IP 同步到主机地址字段。
	onHello func(agentID, ip string)
}

// SetOnHello 设置 hello 帧回调。
func (h *AgentHub) SetOnHello(f func(agentID, ip string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onHello = f
}

// NewAgentHub creates an AgentHub with no active connections.
func NewAgentHub(log *slog.Logger) *AgentHub {
	if log == nil {
		log = slog.Default()
	}
	return &AgentHub{
		conns: make(map[string]*agentConnection),
		log:   log,
	}
}

// Register associates an Agent WebSocket connection with an agent ID and host ID.
func (h *AgentHub) Register(agentID, hostID string, conn agentConn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if old, ok := h.conns[agentID]; ok {
		old.once.Do(func() { close(old.done) })
		_ = old.conn.Close()
	}

	ac := &agentConnection{
		conn:    conn,
		agentID: agentID,
		hostID:  hostID,
		cmds:    make(map[string]*cmdWaiter),
		xfer:    make(map[string]*xferWaiter),
		done:    make(chan struct{}),
	}
	h.conns[agentID] = ac

	go h.readLoop(ac)

	h.log.Debug("agent registered", "agent_id", agentID, "host_id", hostID)
}

// Unregister removes and closes an Agent WebSocket connection.
func (h *AgentHub) Unregister(agentID string) {
	h.mu.Lock()
	ac, ok := h.conns[agentID]
	if ok {
		delete(h.conns, agentID)
	}
	h.mu.Unlock()
	if !ok {
		return
	}
	ac.shutdown("agent disconnected")
	h.log.Debug("agent unregistered", "agent_id", agentID)
}

// shutdown 关闭连接并让所有等待者立即失败。
func (ac *agentConnection) shutdown(reason string) {
	ac.once.Do(func() { close(ac.done) })
	_ = ac.conn.Close()

	ac.mu.Lock()
	for id, w := range ac.cmds {
		select {
		case w.ch <- cmdOutcome{err: fmt.Errorf("agent disconnected: %s", reason)}:
		default:
		}
		delete(ac.cmds, id)
	}
	for id, w := range ac.xfer {
		select {
		case w.ch <- wireFrame{Type: frameUpError, XferID: id, Error: reason}:
		default:
		}
		delete(ac.xfer, id)
	}
	ac.mu.Unlock()
}

// defaultAgentCommandTimeout is used when no timeout is specified.
const defaultAgentCommandTimeout = 10 * time.Minute

// CommandOptions tunes a single Agent command.
type CommandOptions struct {
	// Timeout 为 0 时使用 defaultAgentCommandTimeout。
	Timeout time.Duration
	// Stdin 作为命令的标准输入（明文）。
	Stdin string
	// SudoPassword 非空时，Agent 用 `sudo -S` 执行命令并把密码写入 sudo 的 stdin。
	// 密码只走这条专用字段，不拼进命令行，也不落日志。
	SudoPassword string
	// OnOutput 非 nil 时接收执行期间的增量输出。
	OnOutput func(stream, text string)
}

// RunCommand sends a command and waits for the final result, streaming output
// increments to opts.OnOutput when set.
func (h *AgentHub) RunCommand(ctx context.Context, agentID, cmd string, opts CommandOptions) (*AgentResult, error) {
	ac, err := h.connection(agentID)
	if err != nil {
		return nil, err
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultAgentCommandTimeout
	}

	cmdID := fmt.Sprintf("cmd-%d", time.Now().UnixNano())
	w := &cmdWaiter{ch: make(chan cmdOutcome, 1), onOut: opts.OnOutput}

	ac.mu.Lock()
	ac.cmds[cmdID] = w
	ac.mu.Unlock()

	defer func() {
		ac.mu.Lock()
		delete(ac.cmds, cmdID)
		ac.mu.Unlock()
	}()

	f := wireFrame{
		Type:         frameCmd,
		CmdID:        cmdID,
		Cmd:          cmd,
		Stdin:        opts.Stdin,
		SudoPassword: opts.SudoPassword,
		Timeout:      int(timeout / time.Second),
	}
	if err := ac.send(f); err != nil {
		h.log.Error("agent send command failed", "agent_id", agentID, "cmd_id", cmdID, "error", err)
		h.Unregister(agentID)
		return nil, fmt.Errorf("agent send command: %w", err)
	}

	select {
	case o := <-w.ch:
		if o.err != nil {
			return nil, o.err
		}
		return o.res, nil
	case <-ctx.Done():
		// 传播取消：通知 Agent 终止该命令。
		_ = ac.send(wireFrame{Type: frameCancel, CmdID: cmdID})
		return nil, ctx.Err()
	case <-time.After(timeout):
		_ = ac.send(wireFrame{Type: frameCancel, CmdID: cmdID})
		return nil, fmt.Errorf("agent command timeout after %s", timeout)
	}
}

// SendCommand 是 RunCommand 的兼容包装（timeoutSeconds 以秒计）。
func (h *AgentHub) SendCommand(ctx context.Context, agentID, cmd string, timeoutSeconds int) (*AgentResult, error) {
	opts := CommandOptions{}
	if timeoutSeconds > 0 {
		opts.Timeout = time.Duration(timeoutSeconds) * time.Second
	}
	return h.RunCommand(ctx, agentID, cmd, opts)
}

// Upload 通过分块协议把 r 的内容写到 Agent 主机上的 path，逐块确认。
// 内容经 base64 分块传输，单帧远小于命令长度上限，因此不受文件大小限制。
func (h *AgentHub) Upload(ctx context.Context, agentID, path, mode string, r io.Reader) error {
	ac, err := h.connection(agentID)
	if err != nil {
		return err
	}
	xferID := fmt.Sprintf("up-%d", time.Now().UnixNano())
	w := &xferWaiter{ch: make(chan wireFrame, 8)}

	ac.mu.Lock()
	ac.xfer[xferID] = w
	ac.mu.Unlock()
	defer func() {
		ac.mu.Lock()
		delete(ac.xfer, xferID)
		ac.mu.Unlock()
	}()

	if err := ac.send(wireFrame{Type: frameUpBegin, XferID: xferID, Path: path, Mode: mode}); err != nil {
		return fmt.Errorf("upload begin: %w", err)
	}

	buf := make([]byte, agentChunkSize)
	seq := 0
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			f := wireFrame{
				Type:   frameUpChunk,
				XferID: xferID,
				Seq:    seq,
				B64:    base64.StdEncoding.EncodeToString(buf[:n]),
			}
			if err := ac.send(f); err != nil {
				return fmt.Errorf("upload chunk %d: %w", seq, err)
			}
			if err := waitAck(ctx, w, xferID, seq); err != nil {
				return err
			}
			seq++
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("read source: %w", rerr)
		}
	}

	if err := ac.send(wireFrame{Type: frameUpEnd, XferID: xferID}); err != nil {
		return fmt.Errorf("upload end: %w", err)
	}
	for {
		select {
		case f := <-w.ch:
			switch f.Type {
			case frameUpDone:
				return nil
			case frameUpError:
				return fmt.Errorf("upload failed: %s", f.Error)
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Minute):
			return errors.New("upload timed out waiting for agent")
		}
	}
}

// waitAck 等待某个序号的块确认。
func waitAck(ctx context.Context, w *xferWaiter, xferID string, seq int) error {
	for {
		select {
		case f := <-w.ch:
			switch f.Type {
			case frameUpAck:
				if f.Seq == seq {
					return nil
				}
			case frameUpError:
				return fmt.Errorf("upload failed: %s", f.Error)
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Minute):
			return fmt.Errorf("upload chunk %d not acknowledged", seq)
		}
	}
}

// Download 执行 cmd 并把 stdout 增量写入 w，避免把整个输出缓冲到内存。
func (h *AgentHub) Download(ctx context.Context, agentID, cmd string, w io.Writer) (int64, error) {
	ac, err := h.connection(agentID)
	if err != nil {
		return 0, err
	}
	xferID := fmt.Sprintf("down-%d", time.Now().UnixNano())
	xw := &xferWaiter{ch: make(chan wireFrame, 16)}

	ac.mu.Lock()
	ac.xfer[xferID] = xw
	ac.mu.Unlock()
	defer func() {
		ac.mu.Lock()
		delete(ac.xfer, xferID)
		ac.mu.Unlock()
	}()

	if err := ac.send(wireFrame{Type: frameDownBegin, XferID: xferID, Cmd: cmd}); err != nil {
		return 0, fmt.Errorf("download begin: %w", err)
	}

	var written int64
	for {
		select {
		case f := <-xw.ch:
			switch f.Type {
			case frameDownChunk:
				data, derr := base64.StdEncoding.DecodeString(f.B64)
				if derr != nil {
					return written, fmt.Errorf("decode chunk: %w", derr)
				}
				n, werr := w.Write(data)
				written += int64(n)
				if werr != nil {
					return written, werr
				}
			case frameDownEnd:
				if f.Error != "" {
					return written, fmt.Errorf("download command failed: %s", f.Error)
				}
				if f.ExitCode != 0 {
					return written, fmt.Errorf("download command exited %d", f.ExitCode)
				}
				return written, nil
			case frameUpError:
				return written, fmt.Errorf("download failed: %s", f.Error)
			}
		case <-ctx.Done():
			return written, ctx.Err()
		case <-time.After(2 * time.Minute):
			return written, errors.New("download timed out waiting for agent")
		}
	}
}

// connection 返回已连接的 agentConnection。
func (h *AgentHub) connection(agentID string) (*agentConnection, error) {
	h.mu.Lock()
	ac, ok := h.conns[agentID]
	h.mu.Unlock()
	if !ok {
		return nil, ErrAgentNotConnected
	}
	return ac, nil
}

// readLoop 分发 Agent 回传的各类帧。
func (h *AgentHub) readLoop(ac *agentConnection) {
	for {
		_, msg, err := ac.conn.ReadMessage()
		if err != nil {
			h.log.Debug("agent read loop exiting", "agent_id", ac.agentID, "error", err)
			return
		}

		var f wireFrame
		if err := json.Unmarshal(msg, &f); err != nil {
			h.log.Error("agent frame decode failed", "agent_id", ac.agentID, "error", err)
			continue
		}

		switch f.Type {
		case frameHello:
			ac.mu.Lock()
			ac.version, ac.osName, ac.arch, ac.ip = f.Version, f.OS, f.Arch, f.IP
			ac.mu.Unlock()
			h.log.Info("agent hello", "agent_id", ac.agentID, "version", f.Version, "os", f.OS, "arch", f.Arch, "ip", f.IP)
			if h.onHello != nil {
				go h.onHello(ac.agentID, f.IP)
			}

		case frameOut:
			ac.mu.Lock()
			w := ac.cmds[f.CmdID]
			ac.mu.Unlock()
			if w != nil && w.onOut != nil {
				w.onOut(f.Stream, f.Data)
			}

		case frameResult:
			ac.mu.Lock()
			w := ac.cmds[f.CmdID]
			ac.mu.Unlock()
			if w == nil {
				h.log.Debug("agent result for unknown command", "agent_id", ac.agentID, "cmd_id", f.CmdID)
				continue
			}
			select {
			case w.ch <- cmdOutcome{res: &AgentResult{ID: f.CmdID, ExitCode: f.ExitCode, Stdout: f.Stdout, Stderr: f.Stderr, Error: f.Error}}:
			default:
			}

		case frameUpAck, frameUpDone, frameUpError, frameDownChunk, frameDownEnd:
			ac.mu.Lock()
			xw := ac.xfer[f.XferID]
			ac.mu.Unlock()
			if xw == nil {
				continue
			}
			select {
			case xw.ch <- f:
			default:
				h.log.Warn("agent transfer channel full", "agent_id", ac.agentID, "xfer_id", f.XferID)
			}

		default:
			h.log.Debug("agent unknown frame", "agent_id", ac.agentID, "type", f.Type)
		}
	}
}

// HostIDForAgent returns the host ID associated with an agent ID, or "" if not connected.
func (h *AgentHub) HostIDForAgent(agentID string) string {
	ac, err := h.connection(agentID)
	if err != nil {
		return ""
	}
	return ac.hostID
}

// AgentVersion 返回 Agent 客户端上报的版本（未上报时为空）。
func (h *AgentHub) AgentVersion(agentID string) string {
	ac, err := h.connection(agentID)
	if err != nil {
		return ""
	}
	ac.mu.Lock()
	defer ac.mu.Unlock()
	return ac.version
}

// AgentIP 返回 Agent 客户端上报的主机 IP（未上报时为空）。
func (h *AgentHub) AgentIP(agentID string) string {
	ac, err := h.connection(agentID)
	if err != nil {
		return ""
	}
	ac.mu.Lock()
	defer ac.mu.Unlock()
	return ac.ip
}

// UpdateHostAddressFromAgent polls for the agent's reported IP and updates
// the host's address field if it is empty. Runs until IP is reported or ctx
// is cancelled, up to a 10 second maximum wait.
func (s *Service) UpdateHostAddressFromAgent(ctx context.Context, agentID, hostID string) {
	deadline := time.Now().Add(10 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		ip := s.agentHub.AgentIP(agentID)
		if ip != "" {
			h, err := s.Store.GetHost(hostID)
			if err == nil && (h.Address == "" || strings.HasPrefix(h.Address, "agent-")) {
				h.Address = ip
				if err := s.Store.UpdateHost(h); err != nil {
					s.Log.Error("failed to update host address from agent IP", "host_id", hostID, "ip", ip, "error", err)
				} else {
					s.Log.Info("host address updated from agent IP", "host_id", hostID, "ip", ip)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			s.Log.Warn("agent IP not reported within timeout", "agent_id", agentID, "host_id", hostID)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
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