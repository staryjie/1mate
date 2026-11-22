package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"1mate/internal/cryptox"
	"1mate/internal/model"
	"1mate/internal/redact"
	"1mate/internal/sshx"
	"1mate/internal/store"
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

	// Dial connects to a host. Replaced by tests.
	Dial func(addr string, port int, auth sshx.Auth) sshx.Client

	// ptySessions holds active interactive terminal sessions keyed by session ID.
	ptyMu     sync.Mutex
	ptySessions map[string]*ptySession
	nextPtyID   int
}

type ptySession struct {
	hostID string
	pty    sshx.PtySession
	client sshx.Client
}

func New(st *store.Store, box *cryptox.Box, red *redact.Redactor, dataDir string) *Service {
	return &Service{
		Store:       st,
		Box:         box,
		Red:         red,
		DataDir:     dataDir,
		ptySessions: make(map[string]*ptySession),
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
func (s *Service) Connect(h *model.Host) (sshx.Client, sshx.Sudo, error) {
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
