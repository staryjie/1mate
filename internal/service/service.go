package service

import (
	"errors"
	"fmt"
	"strings"

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
}

func New(st *store.Store, box *cryptox.Box, red *redact.Redactor, dataDir string) *Service {
	return &Service{
		Store:   st,
		Box:     box,
		Red:     red,
		DataDir: dataDir,
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
		strings.Contains(msg, "permission denied"):
		return fmt.Errorf("%w: ssh authentication failed", ErrForbidden)
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "i/o timeout"),
		strings.Contains(msg, "no such host"), strings.Contains(msg, "connect"):
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
