package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"1mate/internal/ids"
	"1mate/internal/model"
	"1mate/internal/provision"
	"1mate/internal/sshx"
)

// InstallPayload is queued for the install_docker job.
type InstallPayload struct {
	HostID              string   `json:"host_id"`
	Strategy            string   `json:"strategy"`
	DockerVersion       string   `json:"docker_version"`
	Mirror              string   `json:"mirror"`
	ComposeVersion      string   `json:"compose_version"`
	ComposeURLBase      string   `json:"compose_url_base"`
	RegistryMirrors     []string `json:"registry_mirrors"`
	SaveSudoPassword    string   `json:"save_sudo_password"`
	AcceptSystemChanges bool     `json:"accept_system_changes"`
}

// InstallOptionsFor resolves install options from settings and the request.
func (s *Service) InstallOptionsFor(hostID string, p InstallPayload) (provision.Options, error) {
	strategy := provision.Strategy(p.Strategy)
	if strategy == "" {
		strategy = provision.StrategyRepo
	}
	composeVersion, err := s.Store.GetSetting("compose_binary_version", model.DefaultComposeVersion)
	if err != nil {
		return provision.Options{}, err
	}
	base, err := s.Store.GetSetting("compose_download_base_url", "")
	if err != nil {
		return provision.Options{}, err
	}
	mirror := p.Mirror
	if mirror == "" {
		mirror, _ = s.Store.GetSetting("default_pkg_mirror", "")
	}
	requireV2, _ := s.Store.GetSetting("require_compose_v2", "false")
	o := provision.Options{
		Strategy:        strategy,
		DockerVersion:   strings.TrimSpace(p.DockerVersion),
		Mirror:          mirror,
		ComposeVersion:  strings.TrimSpace(p.ComposeVersion),
		ComposeURLBase:  strings.TrimSpace(p.ComposeURLBase),
		RegistryMirrors: p.RegistryMirrors,
		RequireV2:       requireV2 == "true",
	}
	if o.ComposeVersion == "" {
		o.ComposeVersion = composeVersion
	}
	if o.ComposeURLBase == "" {
		o.ComposeURLBase = base
	}
	if o.Mirror == "none" {
		o.Mirror = ""
	}
	return o, o.Validate()
}

// InstallScript renders the preview for a host.
func (s *Service) InstallScript(hostID string, p InstallPayload) (string, provision.Options, error) {
	if _, err := s.Store.GetHost(hostID); err != nil {
		return "", provision.Options{}, err
	}
	o, err := s.InstallOptionsFor(hostID, p)
	if err != nil {
		return "", o, err
	}
	body, err := provision.Preview(o)
	return body, o, err
}
// RunInstallJob executes the installer on the host and re-probes afterwards.
func (s *Service) RunInstallJob(ctx context.Context, p InstallPayload, log func(string, ...any)) error {
	if log == nil {
		log = func(string, ...any) {}
	}
	h, err := s.Store.GetHost(p.HostID)
	if err != nil {
		return err
	}
	o, err := s.InstallOptionsFor(p.HostID, p)
	if err != nil {
		return err
	}
	body, err := provision.Script(o)
	if err != nil {
		return err
	}

	if p.SaveSudoPassword != "" && h.SudoMode == model.SudoPassword {
		newID, err := s.storePlainCredential(model.CredSudoPass, p.SaveSudoPassword)
		if err != nil {
			return err
		}
		old := h.SudoCredentialID
		h.SudoCredentialID = newID
		s.Red.Add(p.SaveSudoPassword)
		if err := s.Store.UpdateHost(h); err != nil {
			return err
		}
		if old != "" {
			_ = s.Store.DeleteCredential(old)
		}
	}

	_ = s.Store.SetHostStatus(h.ID, model.HostInstalling, "installing Docker and Compose")

	client, sudo, err := s.Connect(h)
	if err != nil {
		_ = s.Store.SetHostProbeResult(h.ID, model.HostUnreachable, err.Error(), "")
		return classifySSHError(err)
	}
	defer s.closeQuietly(client)

	if sudo.Mode == "none" && h.SudoMode == model.SudoNone {
		// Confirm we can escalate before touching the host.
		probeRes, perr := client.Run(ctx, "id -u", sshx.RunOptions{Timeout: 20 * time.Second})
		if perr != nil {
			return classifySSHError(perr)
		}
		if strings.TrimSpace(probeRes.Stdout) != "0" {
			msg := "the SSH user is not root and no sudo mode is configured; set sudo_mode to root, nopasswd or password"
			_ = s.Store.SetHostStatus(h.ID, model.HostDenied, msg)
			return fmt.Errorf("%w: %s", ErrForbidden, msg)
		}
	}
	if sudo.Mode == "password" && sudo.Password == "" {
		_ = s.Store.SetHostStatus(h.ID, model.HostDenied, "sudo password is missing")
		return fmt.Errorf("%w: E_SUDO_PASSWORD: a sudo password is required on this host", ErrForbidden)
	}

	remotePath := "/tmp/1mate-install-" + ids.New()[:8] + ".sh"
	log("uploading installer to %s", remotePath)
	if err := client.Upload(ctx, remotePath, body, "0700"); err != nil {
		_ = s.Store.SetHostStatus(h.ID, model.HostUnknown, "upload failed: "+err.Error())
		return classifySSHError(err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = client.Run(cleanupCtx, "rm -f "+sshx.Quote(remotePath), sshx.RunOptions{Timeout: 15 * time.Second, Sudo: sudo})
	}()

	log("running installer (strategy=%s mirror=%s compose=%s)", o.Strategy, orNone(o.Mirror), o.ComposeVersion)
	res, err := client.RunStreaming(ctx, "sh "+sshx.Quote(remotePath), sshx.RunOptions{
		Timeout: 30 * time.Minute,
		Sudo:    sudo,
	}, func(chunk string) {
		for _, line := range strings.Split(strings.TrimRight(chunk, "\n"), "\n") {
			if line == "" {
				continue
			}
			log("%s", line)
		}
	})

	if err != nil {
		_ = s.Store.SetHostStatus(h.ID, model.HostUnknown, "installer interrupted: "+err.Error())
		return classifySSHError(err)
	}
	if res.ExitCode != 0 {
		msg := lastMarker(res.Stdout, "1MATE_ERROR=")
		if msg == "" {
			msg = fmt.Sprintf("installer exited with code %d", res.ExitCode)
		}
		_ = s.Store.SetHostStatus(h.ID, model.HostUnknown, msg)
		return errors.New(msg)
	}

	log("installer finished; re-probing the host")
	if _, err := s.ProbeHost(ctx, h, log); err != nil {
		return fmt.Errorf("installer succeeded but the follow-up probe failed: %w", err)
	}
	return nil
}

func lastMarker(out, prefix string) string {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// ParseInstallPayload decodes a job payload.
func ParseInstallPayload(raw string) (InstallPayload, error) {
	var p InstallPayload
	if raw == "" || raw == "{}" {
		return p, nil
	}
	err := json.Unmarshal([]byte(raw), &p)
	return p, err
}
