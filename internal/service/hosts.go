package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"1mate/internal/compose"
	"1mate/internal/ids"
	"1mate/internal/model"
	"1mate/internal/probe"
	"1mate/internal/sshx"
	"1mate/internal/store"
)

var hostNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]{1,63}$`)
var userPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.-]{0,31}$`)
var hostPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{2,253}$`)

// HostInput is the create/update payload for a host.
type HostInput struct {
	Name          string           `json:"name"`
	Address       string           `json:"address"`
	SSHPort       int              `json:"ssh_port"`
	SSHUser       string           `json:"ssh_user"`
	AuthType      model.HostAuthType `json:"auth_type"`
	Password      string           `json:"password"`
	PrivateKey    string           `json:"private_key"`
	KeyPassphrase string           `json:"key_passphrase"`
	SudoMode      model.SudoMode   `json:"sudo_mode"`
	SudoPassword  string           `json:"sudo_password"`
	// TrustFingerprint accepts a new host key after an explicit change.
	TrustFingerprint bool `json:"trust_fingerprint"`
}

func (in *HostInput) Normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.Address = strings.TrimSpace(in.Address)
	in.SSHUser = strings.TrimSpace(in.SSHUser)
	if in.SSHPort == 0 {
		in.SSHPort = 22
	}
	if in.AuthType == "" {
		in.AuthType = model.HostAuthKey
	}
	if in.SudoMode == "" {
		in.SudoMode = model.SudoNone
	}
}

func (in HostInput) Validate() error { return validateHostFields(in, true) }

// validateHostFields checks the merged view. When requireSecret is false the
// secret fields may be empty because stored credentials are reused.
func validateHostFields(in HostInput, requireSecret bool) error {
	var problems []string
	if !hostNamePattern.MatchString(in.Name) {
		problems = append(problems, "name must be 2-64 characters of letters, digits, dot, dash, underscore or space")
	}
	if !hostPattern.MatchString(in.Address) {
		problems = append(problems, "address must be a hostname or IP address")
	}
	if in.SSHPort < 1 || in.SSHPort > 65535 {
		problems = append(problems, "ssh_port must be between 1 and 65535")
	}
	if !userPattern.MatchString(in.SSHUser) {
		problems = append(problems, "ssh_user is not a valid user name")
	}
	switch in.AuthType {
	case model.HostAuthKey:
		switch {
		case in.PrivateKey == "":
			if requireSecret {
				problems = append(problems, "private_key is required for key authentication")
			}
		case !strings.Contains(in.PrivateKey, "PRIVATE KEY"):
			problems = append(problems, "private_key does not look like a PEM private key")
		}
	case model.HostAuthPassword:
		if requireSecret && in.Password == "" {
			problems = append(problems, "password is required for password authentication")
		}
	default:
		problems = append(problems, "auth_type must be key or password")
	}
	switch in.SudoMode {
	case model.SudoNone, model.SudoRoot, model.SudoNopasswd, model.SudoPassword:
	default:
		problems = append(problems, "sudo_mode must be none, root, nopasswd or password")
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrValidation, strings.Join(problems, "; "))
	}
	return nil
}

// CreateHost stores a host with encrypted credentials.
func (s *Service) CreateHost(in HostInput) (*model.Host, error) {
	in.Normalize()
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if existing, err := s.findHostByName(in.Name); err == nil && existing != nil {
		return nil, fmt.Errorf("%w: a host named %q already exists", ErrConflict, in.Name)
	}

	credID := ids.New()
	secret := in.PrivateKey
	if in.AuthType == model.HostAuthPassword {
		secret = in.Password
	}
	ct, err := s.Box.Encrypt(secret, credentialAAD(credID))
	if err != nil {
		return nil, err
	}
	passCT := ""
	if in.KeyPassphrase != "" {
		passCT, err = s.Box.Encrypt(in.KeyPassphrase, credentialAAD(credID)+":passphrase")
		if err != nil {
			return nil, err
		}
	}
	kind := model.CredSSHKey
	if in.AuthType == model.HostAuthPassword {
		kind = model.CredSSHPassword
	}
	if err := s.Store.CreateCredential(&model.Credential{
		ID:                   credID,
		Kind:                 kind,
		Ciphertext:           ct,
		PassphraseCiphertext: passCT,
		KeyVersion:           1,
		CreatedAt:            time.Now().UTC(),
	}); err != nil {
		return nil, err
	}
	s.Red.Add(secret)
	s.Red.Add(in.KeyPassphrase)

	sudoCredID := ""
	if in.SudoMode == model.SudoPassword && in.SudoPassword != "" {
		sudoCredID, err = s.storePlainCredential(model.CredSudoPass, in.SudoPassword)
		if err != nil {
			return nil, err
		}
		s.Red.Add(in.SudoPassword)
	}

	h := &model.Host{
		ID:               ids.New(),
		Name:             in.Name,
		Address:          in.Address,
		SSHPort:          in.SSHPort,
		SSHUser:          in.SSHUser,
		AuthType:         in.AuthType,
		CredentialID:     credID,
		SudoMode:         in.SudoMode,
		SudoCredentialID: sudoCredID,
		Status:           model.HostUnknown,
	}
	if h.SudoMode == model.SudoRoot {
		h.SudoMode = model.SudoRoot
	}
	if err := s.Store.CreateHost(h); err != nil {
		_ = s.Store.DeleteCredential(credID)
		return nil, err
	}
	return h, nil
}

func (s *Service) storePlainCredential(kind model.CredentialKind, plain string) (string, error) {
	id := ids.New()
	ct, err := s.Box.Encrypt(plain, credentialAAD(id))
	if err != nil {
		return "", err
	}
	return id, s.Store.CreateCredential(&model.Credential{
		ID: id, Kind: kind, Ciphertext: ct, KeyVersion: 1, CreatedAt: time.Now().UTC(),
	})
}

func (s *Service) findHostByName(name string) (*model.Host, error) {
	hosts, err := s.Store.ListHosts()
	if err != nil {
		return nil, err
	}
	for i := range hosts {
		if strings.EqualFold(hosts[i].Name, name) {
			return &hosts[i], nil
		}
	}
	return nil, store.ErrNotFound
}

// UpdateHost applies a partial update, rotating credentials only when new
// secret material is supplied.
func (s *Service) UpdateHost(id string, in HostInput) (*model.Host, error) {
	h, err := s.Store.GetHost(id)
	if err != nil {
		return nil, err
	}
	in.Normalize()
	if in.Address == "" {
		in.Address = h.Address
	}
	if in.SSHUser == "" {
		in.SSHUser = h.SSHUser
	}
	if in.Name == "" {
		in.Name = h.Name
	}
	if in.SSHPort == 0 {
		in.SSHPort = h.SSHPort
	}
	if in.AuthType == "" {
		in.AuthType = h.AuthType
	}
	if in.SudoMode == "" {
		in.SudoMode = h.SudoMode
	}
	if err := validateHostFields(in, false); err != nil {
		return nil, err
	}

	if in.Name != h.Name {
		if other, err := s.findHostByName(in.Name); err == nil && other != nil && other.ID != h.ID {
			return nil, fmt.Errorf("%w: a host named %q already exists", ErrConflict, in.Name)
		}
	}

	cred, credErr := s.Store.GetCredential(h.CredentialID)
	if credErr != nil {
		return nil, credErr
	}

	newSecret := in.PrivateKey
	if in.AuthType == model.HostAuthPassword {
		newSecret = in.Password
	}
	kindChanged := in.AuthType != h.AuthType
	var currentKind model.CredentialKind
	currentKind = cred.Kind
	if !kindChanged && newSecret != "" {
		wantKind := model.CredSSHKey
		if in.AuthType == model.HostAuthPassword {
			wantKind = model.CredSSHPassword
		}
		kindChanged = currentKind != wantKind
	}

	if newSecret != "" {
		targetID := h.CredentialID
		if kindChanged {
			targetID = ids.New()
		}
		kind := model.CredSSHKey
		if in.AuthType == model.HostAuthPassword {
			kind = model.CredSSHPassword
		}
		ct, err := s.Box.Encrypt(newSecret, credentialAAD(targetID))
		if err != nil {
			return nil, err
		}
		passCT := ""
		if in.KeyPassphrase != "" {
			passCT, err = s.Box.Encrypt(in.KeyPassphrase, credentialAAD(targetID)+":passphrase")
			if err != nil {
				return nil, err
			}
		}
		if kindChanged {
			if err := s.Store.CreateCredential(&model.Credential{
				ID: targetID, Kind: kind, Ciphertext: ct, PassphraseCiphertext: passCT,
				KeyVersion: 1, CreatedAt: time.Now().UTC(),
			}); err != nil {
				return nil, err
			}
			old := h.CredentialID
			h.CredentialID = targetID
			_ = s.Store.DeleteCredential(old)
		} else {
			// Re-encrypt in place: the AAD only depends on the id.
			if err := s.Store.DeleteCredential(targetID); err != nil {
				return nil, err
			}
			if err := s.Store.CreateCredential(&model.Credential{
				ID: targetID, Kind: kind, Ciphertext: ct, PassphraseCiphertext: passCT,
				KeyVersion: 1, CreatedAt: time.Now().UTC(),
			}); err != nil {
				return nil, err
			}
		}
		s.Red.Add(newSecret)
		s.Red.Add(in.KeyPassphrase)
	} else if kindChanged {
		return nil, fmt.Errorf("%w: new credentials are required when switching auth_type", ErrValidation)
	}

	if in.SudoMode == model.SudoPassword && in.SudoPassword != "" {
		newID, err := s.storePlainCredential(model.CredSudoPass, in.SudoPassword)
		if err != nil {
			return nil, err
		}
		old := h.SudoCredentialID
		h.SudoCredentialID = newID
		s.Red.Add(in.SudoPassword)
		if old != "" {
			_ = s.Store.DeleteCredential(old)
		}
	} else if in.SudoMode == model.SudoPassword && h.SudoCredentialID == "" {
		return nil, fmt.Errorf("%w: sudo_password is required when sudo_mode is password", ErrValidation)
	}
	if in.SudoMode != model.SudoPassword {
		if h.SudoCredentialID != "" {
			_ = s.Store.DeleteCredential(h.SudoCredentialID)
		}
		h.SudoCredentialID = ""
	}

	addressChanged := h.Address != in.Address || h.SSHPort != in.SSHPort || h.SSHUser != in.SSHUser
	h.Name = in.Name
	h.Address = in.Address
	h.SSHPort = in.SSHPort
	h.SSHUser = in.SSHUser
	h.AuthType = in.AuthType
	h.SudoMode = in.SudoMode
	// A changed endpoint or credential invalidates the pinned fingerprint. An
	// explicit trust request clears it too, forcing a fresh probe.
	if addressChanged || in.TrustFingerprint {
		h.HostKeyFingerpr = ""
	}
	h.Status = model.HostUnknown
	h.StatusMessage = ""
	if err := s.Store.UpdateHost(h); err != nil {
		return nil, err
	}
	return h, nil
}

// DeleteHost removes a host and its credentials, refusing when instances exist.
func (s *Service) DeleteHost(id string) error {
	h, err := s.Store.GetHost(id)
	if err != nil {
		return err
	}
	n, err := s.Store.CountInstancesForHost(id)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: host still has %d instance(s); delete them first", ErrConflict, n)
	}
	if err := s.Store.DeleteHost(id); err != nil {
		return err
	}
	_ = s.Store.DeleteCredential(h.CredentialID)
	_ = s.Store.DeleteCredential(h.SudoCredentialID)
	return nil
}

// ProbeHost runs the read-only capability probe and stores the outcome.
func (s *Service) ProbeHost(ctx context.Context, h *model.Host, logf func(string, ...any)) (*model.Capabilities, error) {
	client, sudo, err := s.Connect(h)
	if err != nil {
		s.recordProbeFailure(h, err)
		return nil, err
	}
	defer s.closeQuietly(client)

	// Upload the probe script to a temp file, then execute it with `sh <file>`.
	// Passing the script through `sh -c '...'` or `sh -s` breaks when the remote
	// user's login shell is zsh: zsh cannot parse sshx.Quote's '\''
	// escaping inside single quotes, corrupting the script (e.g. '#'
	// comment lines become commands and OS_ID ends up empty). Uploading the
	// script as a file and running `sh <file>` avoids all quoting issues.
	probePath := "/tmp/1mate-probe-" + ids.New()[:8] + ".sh"
	if err := client.UploadReader(ctx, probePath, strings.NewReader(probe.Script), "0755", sshx.RunOptions{
		Timeout: 90 * time.Second,
		Sudo:    sudo,
	}); err != nil {
		s.recordProbeFailure(h, err)
		return nil, classifySSHError(err)
	}
	res, err := client.Run(ctx, "sh "+sshx.Quote(probePath), sshx.RunOptions{
		Timeout: 90 * time.Second,
		Sudo:    sudo,
	})
	if err != nil {
		s.recordProbeFailure(h, err)
		return nil, classifySSHError(err)
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(res.Stdout)
		if msg == "" {
			msg = fmt.Sprintf("probe exited with code %d", res.ExitCode)
		}
		s.recordProbeFailure(h, errors.New(msg))
		return nil, fmt.Errorf("%w: %s", ErrValidation, msg)
	}
	caps, err := probe.Parse(h.ID, res.Stdout)
	if err != nil {
		return nil, err
	}
	caps.ProbedAt = time.Now().UTC()
	caps.RawJSON = truncate(res.Stdout, 32*1024)
	if err := s.Store.SaveCapabilities(caps); err != nil {
		return nil, err
	}
	status, message := probe.Status(caps)
	if err := s.Store.SetHostProbeResult(h.ID, status, message, client.HostKeyFingerprint()); err != nil {
		return nil, err
	}
	if logf != nil {
		logf("host %s: status=%s docker=%s compose=%s", h.Name, status, caps.DockerServer, caps.ComposeKind)
	}
	return caps, nil
}

func (s *Service) recordProbeFailure(h *model.Host, err error) {
	status := model.HostUnreachable
	msg := err.Error()
	if errors.Is(err, sshx.ErrHostKeyChanged) {
		status = model.HostKeyChanged
		msg = "host key fingerprint changed; re-trust the host before continuing"
	}
	if errors.Is(err, ErrForbidden) || strings.Contains(strings.ToLower(msg), "authentication") {
		status = model.HostDenied
	}
	_ = s.Store.SetHostProbeResult(h.ID, status, msg, "")
}

// ProbePayload is queued for the host_probe job.
type ProbePayload struct {
	HostID string `json:"host_id"`
}

func (s *Service) RunProbeJob(ctx context.Context, hostID string, logf func(string, ...any)) error {
	h, err := s.Store.GetHost(hostID)
	if err != nil {
		return err
	}
	if logf != nil {
		logf("probing %s (%s@%s:%d)", h.Name, h.SSHUser, h.Address, h.SSHPort)
	}
	_, err = s.ProbeHost(ctx, h, logf)
	return err
}

// CapabilitiesFor returns capabilities or a not-found error.
func (s *Service) CapabilitiesFor(hostID string) (*model.Capabilities, error) {
	return s.Store.GetCapabilities(hostID)
}

// ComposeCommandFor returns the compose command prefix for a host, or a
// validation error explaining why it cannot be used.
func (s *Service) ComposeCommandFor(hostID string, requireV2 bool) (string, model.ComposeKind, error) {
	caps, err := s.Store.GetCapabilities(hostID)
	if err != nil {
		return "", model.ComposeNone, fmt.Errorf("%w: probe the host first", ErrValidation)
	}
	if caps.UnsupportedReason != "" {
		return "", model.ComposeNone, fmt.Errorf("%w: %s", ErrValidation, caps.UnsupportedReason)
	}
	if caps.SocketAccess == model.SocketDenied {
		return "", model.ComposeNone, fmt.Errorf("%w: the SSH user cannot access the docker socket", ErrValidation)
	}
	cmd, ok := compose.Command(caps.ComposeKind)
	if !ok {
		return "", model.ComposeNone, fmt.Errorf("%w: no usable Docker Compose on this host", ErrValidation)
	}
	if requireV2 && caps.ComposeKind == model.ComposeStandaloneV1 {
		return "", model.ComposeNone, fmt.Errorf("%w: Compose v2 is required but only v1 is installed", ErrValidation)
	}
	return cmd, caps.ComposeKind, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n...truncated"
}

// MarshalPayload is a small helper for job payloads.
func MarshalPayload(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
