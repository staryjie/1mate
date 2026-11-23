package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"1mate/internal/agentx"
	"1mate/internal/cryptox"
	"1mate/internal/model"
	"1mate/internal/redact"
	"1mate/internal/sshx"
	"1mate/internal/store"
	"github.com/gorilla/websocket"
)

var (
	ErrNotFound  = store.ErrNotFound
	ErrValidation = errors.New("validation failed")
	ErrConflict  = errors.New("conflict")
	ErrForbidden = errors.New("forbidden")
	ErrUnreachable = errors.New("host unreachable")
	ErrNoPrivilege = errors.New("no privilege escalation available")
)

// Service wires storage, crypto and the SSH transport into operations the API
// and job handlers share.
type Service struct {
	Store   *store.Store
	Box     *cryptox.Box
	Red     *redact.Redactor
	DataDir string
	Log     *slog.Logger

	// Dial connects to a host. Replaced by tests.
	Dial func(addr string, port int, auth sshx.Auth) sshx.Client

	// ptySessions holds active interactive terminal sessions keyed by session ID.
	ptyMu     sync.Mutex
	ptySessions map[string]*ptySession
	nextPtyID   int

	// agentHub manages active Agent WebSocket connections keyed by agent ID.
	agentHub *AgentHub
}

type ptySession struct {
	hostID string
	pty    sshx.PtySession
	client sshx.Client
}

func New(st *store.Store, box *cryptox.Box, red *redact.Redactor, dataDir string, log *slog.Logger, agentHub *AgentHub) *Service {
	return &Service{
		Store:       st,
		Box:         box,
		Red:         red,
		DataDir:     dataDir,
		Log:         log,
		ptySessions: make(map[string]*ptySession),
		agentHub:    agentHub,
		Dial: func(addr string, port int, auth sshx.Auth) sshx.Client {
			return sshx.New(addr, port, auth)
		},
	}
}

// sshAuth decrypts the host credential into usable SSH auth material.
func (s *Service) sshAuth(h *model.Host) (sshx.Auth, error) {
	cred, err := s.Store.GetCredential(h.CredentialID)
	if err != nil {
		return sshx.Auth{}, fmt.Errorf("host credential: %w", err)
	}
	secret, err := s.Box.Decrypt(cred.Ciphertext, credentialAAD(cred.ID))
	if err != nil {
		return sshx.Auth{}, err
	}
	auth := sshx.Auth{User: h.SSHUser, KnownHosts: h.HostKeyFingerpr}
	if cred.Kind == model.CredSSHKey {
		auth.PrivateKeyPEM = secret
		if cred.PassphraseCiphertext != "" {
			pass, err := s.Box.Decrypt(cred.PassphraseCiphertext, credentialAAD(cred.ID)+":passphrase")
			if err != nil {
				return sshx.Auth{}, err
			}
			auth.KeyPassphrase = pass
		}
	} else {
		auth.Password = secret
	}
	return auth, nil
}

func credentialAAD(id string) string { return "credentials:" + id }

// sudoFor resolves the sudo strategy, including the optional password.
func (s *Service) sudoFor(h *model.Host) (sshx.Sudo, error) {
	sudo := sshx.Sudo{Mode: string(h.SudoMode)}
	if h.SudoMode == model.SudoPassword && h.SudoCredentialID != "" {
		cred, err := s.Store.GetCredential(h.SudoCredentialID)
		if err != nil {
			return sudo, err
		}
		pass, err := s.Box.Decrypt(cred.Ciphertext, credentialAAD(cred.ID))
		if err != nil {
			return sudo, err
		}
		sudo.Password = pass
		s.Red.Add(pass)
	}
	return sudo, nil
}

// Connect opens a session for a job and registers secrets for redaction.
// For Agent-mode hosts it returns an agentx.Client backed by the WebSocket
// connection; for SSH-mode hosts it dials via s.Dial.
func (s *Service) Connect(h *model.Host) (sshx.Client, sshx.Sudo, error) {
	if h.ConnectionType == model.ConnTypeAgent {
		return s.connectAgent(h)
	}
	return s.connectSSH(h)
}

// connectSSH opens an agentless SSH session for the host.
func (s *Service) connectSSH(h *model.Host) (sshx.Client, sshx.Sudo, error) {
	auth, err := s.sshAuth(h)
	if err != nil {
		return nil, sshx.Sudo{}, err
	}
	s.Red.Add(auth.Password)
	s.Red.Add(auth.KeyPassphrase)
	sudo, err := s.sudoFor(h)
	if err != nil {
		return nil, sshx.Sudo{}, err
	}
	return s.Dial(h.Address, h.SSHPort, auth), sudo, nil
}

// connectAgent opens an Agent-backed session for the host. No SSH credentials
// are needed; the Agent WebSocket connection carries all commands.
func (s *Service) connectAgent(h *model.Host) (sshx.Client, sshx.Sudo, error) {
	if s.agentHub == nil {
		return nil, sshx.Sudo{}, errors.New("agent hub not initialized")
	}
	if !s.agentHub.IsConnected(h.AgentID) {
		return nil, sshx.Sudo{}, fmt.Errorf("agent %s not connected", h.AgentID)
	}
	// 提权策略（G6）：Agent 主机的 sudo 密码同样从凭据解出，稍后通过
	// sudo_password 帧字段下发，不进入命令行。
	sudo, err := s.sudoFor(h)
	if err != nil {
		return nil, sshx.Sudo{}, err
	}
	commander := agentCommander{svc: s}
	client := agentx.New(h.AgentID, commander)
	return client, sudo, nil
}

// agentCommander adapts Service.SendAgentCommand to the agentx.AgentCommander
// interface, converting *AgentResult to sshx.Result.
type agentCommander struct {
	svc *Service
}

func (a agentCommander) SendCommand(ctx context.Context, agentID string, cmd string, timeoutSeconds int) (sshx.Result, error) {
	res, err := a.svc.SendAgentCommand(ctx, agentID, cmd, timeoutSeconds)
	if err != nil {
		return sshx.Result{}, err
	}
	return sshx.Result{
		ExitCode: res.ExitCode,
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
	}, nil
}

func (a agentCommander) IsConnected(agentID string) bool {
	return a.svc.AgentConnected(agentID)
}

// RunStream 通过 Agent 执行命令并回传增量输出（G4）。
func (a agentCommander) RunStream(ctx context.Context, agentID, cmd string, opts agentx.RunOptions, onOutput func(stream, text string)) (sshx.Result, error) {
	res, err := a.svc.RunAgentCommand(ctx, agentID, cmd, CommandOptions{
		Timeout:      opts.Timeout,
		Stdin:        opts.Stdin,
		SudoPassword: opts.SudoPassword,
		OnOutput:     onOutput,
	})
	if err != nil {
		return sshx.Result{}, err
	}
	return sshx.Result{
		ExitCode: res.ExitCode,
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
	}, nil
}

// UploadFile 通过 Agent 分块上传文件（G2/G3）。
func (a agentCommander) UploadFile(ctx context.Context, agentID, path, mode string, r io.Reader) error {
	return a.svc.UploadToAgent(ctx, agentID, path, mode, r)
}

// DownloadCommand 通过 Agent 执行命令并把输出流式写入 w（G2/G3）。
func (a agentCommander) DownloadCommand(ctx context.Context, agentID, cmd string, w io.Writer) (int64, error) {
	return a.svc.DownloadFromAgent(ctx, agentID, cmd, w)
}

// dockerPrefix returns the docker command prefix, honouring the case where the
// SSH user cannot talk to the socket directly.
func dockerPrefix(sudo sshx.Sudo, caps *model.Capabilities) string {
	if caps != nil && caps.DockerSudo {
		switch sudo.Mode {
		case "root":
			return ""
		case "nopasswd":
			return "sudo -n "
		case "password":
			return "sudo -S -p '' "
		}
	}
	return ""
}

// classifySSHError maps transport failures to stable API error codes.
func classifySSHError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, sshx.ErrHostKeyChanged):
		return fmt.Errorf("%w: %s", ErrForbidden, err.Error())
	case errors.Is(err, sshx.ErrNoPrivilege):
		return fmt.Errorf("%w: %s", ErrForbidden, err.Error())
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "unable to authenticate"), strings.Contains(msg, "no supported methods"),
		strings.Contains(msg, "permission denied"), strings.Contains(msg, "认证失败"), strings.Contains(msg, "权限被拒绝"):
		return fmt.Errorf("%w: ssh authentication failed", ErrForbidden)
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "i/o timeout"),
		strings.Contains(msg, "no such host"), strings.Contains(msg, "connect"),
		strings.Contains(msg, "连接被拒绝"), strings.Contains(msg, "连接超时"), strings.Contains(msg, "主机不可达"):
		return fmt.Errorf("%w: %s", ErrUnreachable, err.Error())
	}
	return err
}

// closeQuietly closes an SSH client, ignoring the error on shutdown paths.
func (s *Service) closeQuietly(c sshx.Client) {
	if c != nil {
		_ = c.Close()
	}
}

// CreatePtySession connects to the host and starts an interactive PTY session.
// Returns a session ID that the API layer uses to route WebSocket messages.
func (s *Service) CreatePtySession(hostID, term string, rows, cols int) (string, error) {
	h, err := s.Store.GetHost(hostID)
	if err != nil {
		return "", err
	}
	client, sudo, err := s.Connect(h)
	if err != nil {
		return "", classifySSHError(err)
	}
	pty, err := client.PtyStart(context.Background(), term, rows, cols, sshx.RunOptions{
		Sudo: sudo,
	})
	if err != nil {
		client.Close()
		return "", classifySSHError(err)
	}
	s.ptyMu.Lock()
	defer s.ptyMu.Unlock()
	s.nextPtyID++
	id := fmt.Sprintf("pty-%d", s.nextPtyID)
	s.ptySessions[id] = &ptySession{hostID: hostID, pty: pty, client: client}
	return id, nil
}

// PtyWrite sends input to a PTY session.
func (s *Service) PtyWrite(sessionID string, data []byte) error {
	s.ptyMu.Lock()
	ps, ok := s.ptySessions[sessionID]
	s.ptyMu.Unlock()
	if !ok {
		return store.ErrNotFound
	}
	_, err := ps.pty.Write(data)
	return err
}

// PtyRead reads output from a PTY session. Blocks until data is available or
// the session is closed.
func (s *Service) PtyRead(sessionID string, buf []byte) (int, error) {
	s.ptyMu.Lock()
	ps, ok := s.ptySessions[sessionID]
	s.ptyMu.Unlock()
	if !ok {
		return 0, store.ErrNotFound
	}
	return ps.pty.Read(buf)
}

// PtyResize resizes a PTY session's window.
func (s *Service) PtyResize(sessionID string, rows, cols int) error {
	s.ptyMu.Lock()
	ps, ok := s.ptySessions[sessionID]
	s.ptyMu.Unlock()
	if !ok {
		return store.ErrNotFound
	}
	return ps.pty.Resize(rows, cols)
}

// PtyClose closes a PTY session and releases its SSH connection.
func (s *Service) PtyClose(sessionID string) error {
	s.ptyMu.Lock()
	ps, ok := s.ptySessions[sessionID]
	if ok {
		delete(s.ptySessions, sessionID)
	}
	s.ptyMu.Unlock()
	if !ok {
		return store.ErrNotFound
	}
	_ = ps.pty.Close()
	_ = ps.client.Close()
	return nil
}

// RegisterAgent associates an Agent WebSocket connection with an agent ID and host ID.
func (s *Service) RegisterAgent(agentID, hostID string, conn *websocket.Conn) {
	if s.agentHub == nil {
		s.Log.Error("agent hub not initialized", "agent_id", agentID)
		_ = conn.Close()
		return
	}
	s.agentHub.Register(agentID, hostID, conn)
}

// UnregisterAgent removes an Agent WebSocket connection.
func (s *Service) UnregisterAgent(agentID string) {
	if s.agentHub == nil {
		return
	}
	s.agentHub.Unregister(agentID)
}

// SendAgentCommand sends a command to an Agent and waits for the result.
func (s *Service) SendAgentCommand(ctx context.Context, agentID string, cmd string, timeoutSeconds int) (*AgentResult, error) {
	if s.agentHub == nil {
		return nil, ErrAgentNotConnected
	}
	return s.agentHub.SendCommand(ctx, agentID, cmd, timeoutSeconds)
}

// RunAgentCommand sends a command to an Agent with full options (streaming,
// stdin, sudo password) and waits for the final result.
func (s *Service) RunAgentCommand(ctx context.Context, agentID, cmd string, opts CommandOptions) (*AgentResult, error) {
	if s.agentHub == nil {
		return nil, ErrAgentNotConnected
	}
	return s.agentHub.RunCommand(ctx, agentID, cmd, opts)
}

// UploadToAgent streams r into a file on the Agent host using chunked frames.
func (s *Service) UploadToAgent(ctx context.Context, agentID, path, mode string, r io.Reader) error {
	if s.agentHub == nil {
		return ErrAgentNotConnected
	}
	return s.agentHub.Upload(ctx, agentID, path, mode, r)
}

// DownloadFromAgent runs cmd on the Agent host and streams its stdout into w.
func (s *Service) DownloadFromAgent(ctx context.Context, agentID, cmd string, w io.Writer) (int64, error) {
	if s.agentHub == nil {
		return 0, ErrAgentNotConnected
	}
	return s.agentHub.Download(ctx, agentID, cmd, w)
}

// AgentVersion returns the version reported by the connected Agent, if any.
func (s *Service) AgentVersion(agentID string) string {
	if s.agentHub == nil {
		return ""
	}
	return s.agentHub.AgentVersion(agentID)
}

// AgentConnected reports whether an Agent is currently connected.
func (s *Service) AgentConnected(agentID string) bool {
	if s.agentHub == nil {
		return false
	}
	return s.agentHub.IsConnected(agentID)
}
