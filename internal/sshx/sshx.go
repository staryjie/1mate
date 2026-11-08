package sshx

import (
	"context"
	"errors"
	"time"
)

// ErrHostKeyChanged is returned when the remote host presents a key that does
// not match the pinned fingerprint. It must never be auto-accepted.
var ErrHostKeyChanged = errors.New("host key fingerprint changed")

// ErrNoPrivilege means the action needs root and no usable sudo path exists.
var ErrNoPrivilege = errors.New("no privilege escalation available")

type Auth struct {
	User           string
	Password       string
	PrivateKeyPEM  string
	KeyPassphrase  string
	KnownHosts     string // pinned SHA256 fingerprint, empty means trust-on-first-use
}

// Result is the output of a remote command.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Client runs commands on one remote host. Implementations must be safe for
// concurrent use across goroutines.
type Client interface {
	// HostKeyFingerprint returns the fingerprint observed on this connection.
	HostKeyFingerprint() string
	// Run executes a command with sudo prefixing applied by the caller.
	Run(ctx context.Context, cmd string, opts RunOptions) (Result, error)
	// RunStreaming executes a command and streams combined output through sink.
	RunStreaming(ctx context.Context, cmd string, opts RunOptions, sink func(string)) (Result, error)
	// Upload writes content to a path atomically (tmp + mv).
	Upload(ctx context.Context, path, content string, mode string) error
	// Download streams a remote file to the writer, returning bytes copied.
	Download(ctx context.Context, path string, w interface{ Write([]byte) (int, error) }) (int64, error)
	Close() error
}

// Sudo carries how to elevate. Password is never logged.
type Sudo struct {
	Mode     string // none | root | nopasswd | password
	Password string
}

func (s Sudo) Prefix() string {
	switch s.Mode {
	case "nopasswd":
		return "sudo -n "
	case "root", "password":
		return ""
	}
	return ""
}

func (s Sudo) NeedsPassword() bool { return s.Mode == "password" && s.Password != "" }

// RunOptions tunes a single command execution.
type RunOptions struct {
	Timeout   time.Duration
	Sudo      Sudo
	Env       map[string]string
	StdinText string
	// Prefix is prepended verbatim, e.g. the docker sudo prefix.
	Prefix string
}

// Fake records commands and returns scripted results. Tests use it to assert
// the exact command sequence without a remote machine.
type Fake struct {
	Fingerprint string
	// Handler may return a result and error for a command. When nil the fake
	// returns exit 0 with empty output.
	Handler func(cmd string) (Result, error)
	Calls   []string
	Uploads map[string]string
}

func NewFake(fingerprint string) *Fake {
	return &Fake{Fingerprint: fingerprint, Uploads: map[string]string{}}
}

func (f *Fake) HostKeyFingerprint() string { return f.Fingerprint }

func (f *Fake) Run(ctx context.Context, cmd string, opts RunOptions) (Result, error) {
	f.Calls = append(f.Calls, cmd)
	if f.Handler != nil {
		return f.Handler(cmd)
	}
	return Result{ExitCode: 0}, nil
}

func (f *Fake) RunStreaming(ctx context.Context, cmd string, opts RunOptions, sink func(string)) (Result, error) {
	res, err := f.Run(ctx, cmd, opts)
	if sink != nil {
		if res.Stdout != "" {
			sink(res.Stdout)
		}
		if res.Stderr != "" {
			sink(res.Stderr)
		}
	}
	return res, err
}

func (f *Fake) Upload(ctx context.Context, path, content, mode string) error {
	f.Calls = append(f.Calls, "upload "+path)
	f.Uploads[path] = content
	return nil
}

func (f *Fake) Download(ctx context.Context, path string, w interface{ Write([]byte) (int, error) }) (int64, error) {
	f.Calls = append(f.Calls, "download "+path)
	data, ok := f.Uploads[path]
	if !ok {
		return 0, errors.New("fake: no such file " + path)
	}
	n, err := w.Write([]byte(data))
	return int64(n), err
}

func (f *Fake) Close() error { return nil }
