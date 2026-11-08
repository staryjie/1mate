package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"1mate/internal/sshx"
)

// ConnectionTest is the result of a pre-save connectivity check.
type ConnectionTest struct {
	OK          bool   `json:"ok"`
	Fingerprint string `json:"fingerprint"`
	Message     string `json:"message"`
	DockerOut   string `json:"docker"`
}

// TestConnectionInput mirrors HostInput but always carries fresh secrets.
type TestConnectionInput struct {
	Address       string `json:"address"`
	SSHPort       int    `json:"ssh_port"`
	SSHUser       string `json:"ssh_user"`
	AuthType      string `json:"auth_type"`
	Password      string `json:"password"`
	PrivateKey    string `json:"private_key"`
	KeyPassphrase string `json:"key_passphrase"`
}

// TestConnection verifies SSH credentials and reports the host key fingerprint
// before the host is stored, so a user can pin it deliberately.
func (s *Service) TestConnection(ctx context.Context, in TestConnectionInput) (*ConnectionTest, error) {
	if strings.TrimSpace(in.Address) == "" {
		return nil, fmt.Errorf("%w: address is required", ErrValidation)
	}
	if in.SSHPort == 0 {
		in.SSHPort = 22
	}
	s.Red.Add(in.Password)
	s.Red.Add(in.KeyPassphrase)
	auth := sshx.Auth{User: in.SSHUser}
	if in.AuthType == "password" {
		if in.Password == "" {
			return nil, fmt.Errorf("%w: password is required", ErrValidation)
		}
		auth.Password = in.Password
	} else {
		if in.PrivateKey == "" {
			return nil, fmt.Errorf("%w: private_key is required", ErrValidation)
		}
		auth.PrivateKeyPEM = in.PrivateKey
		auth.KeyPassphrase = in.KeyPassphrase
	}
	client := s.Dial(in.Address, in.SSHPort, auth)
	defer s.closeQuietly(client)

	cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	res, err := client.Run(cctx, "id -un && uname -m && (command -v docker || true)", sshx.RunOptions{Timeout: 20 * time.Second})
	if err != nil {
		return &ConnectionTest{OK: false, Message: classifySSHError(err).Error()},
			classifySSHError(err)
	}
	out := strings.TrimSpace(res.Stdout)
	return &ConnectionTest{
		OK:          res.ExitCode == 0,
		Fingerprint: client.HostKeyFingerprint(),
		Message:     "connection established",
		DockerOut:   out,
	}, nil
}
