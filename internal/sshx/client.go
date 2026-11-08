package sshx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Dialer creates real SSH clients. One dialer per host id, reused so the
// underlying TCP connection is not re-established for every command.
type Dialer struct {
	addr    string
	port    int
	auth    Auth
	mu      sync.Mutex
	client  *ssh.Client
	finger  string
	timeout time.Duration
}

func New(addr string, port int, auth Auth) *Dialer {
	if port <= 0 {
		port = 22
	}
	return &Dialer{addr: addr, port: port, auth: auth, timeout: 15 * time.Second}
}

func (d *Dialer) connect(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.client != nil {
		return nil
	}
	cfg, err := d.clientConfig()
	if err != nil {
		return err
	}
	target := net.JoinHostPort(d.addr, strconv.Itoa(d.port))
	dialer := net.Dialer{Timeout: d.timeout}
	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return fmt.Errorf("ssh connect %s: %w", target, err)
	}
	// Bound the handshake even if the context has no deadline.
	_ = conn.SetDeadline(time.Now().Add(d.timeout))
	c, chans, reqs, err := ssh.NewClientConn(conn, target, cfg)
	if err != nil {
		conn.Close()
		return fmt.Errorf("ssh handshake %s: %w", target, err)
	}
	_ = conn.SetDeadline(time.Time{})
	d.client = ssh.NewClient(c, chans, reqs)
	return nil
}

func (d *Dialer) clientConfig() (*ssh.ClientConfig, error) {
	var methods []ssh.AuthMethod
	if d.auth.PrivateKeyPEM != "" {
		var signer ssh.Signer
		var err error
		if d.auth.KeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(d.auth.PrivateKeyPEM), []byte(d.auth.KeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(d.auth.PrivateKeyPEM))
		}
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if d.auth.Password != "" {
		methods = append(methods, ssh.Password(d.auth.Password))
	}
	if len(methods) == 0 {
		return nil, errors.New("no ssh authentication method provided")
	}
	pinned := strings.TrimSpace(d.auth.KnownHosts)
	cfg := &ssh.ClientConfig{
		User:    d.auth.User,
		Auth:    methods,
		Timeout: d.timeout,
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			fp := Fingerprint(key)
			d.finger = fp
			if pinned == "" {
				return nil
			}
			if fp != pinned {
				return fmt.Errorf("%w: expected %s, got %s", ErrHostKeyChanged, pinned, fp)
			}
			return nil
		},
	}
	return cfg, nil
}

// Fingerprint renders the SHA256 fingerprint in the OpenSSH style.
func Fingerprint(key ssh.PublicKey) string {
	sum := sha256.Sum256(key.Marshal())
	return "SHA256:" + strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "=")
}

func (d *Dialer) HostKeyFingerprint() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.finger
}

func (d *Dialer) Run(ctx context.Context, cmd string, opts RunOptions) (Result, error) {
	var buf bytes.Buffer
	return d.run(ctx, cmd, opts, &buf)
}

func (d *Dialer) RunStreaming(ctx context.Context, cmd string, opts RunOptions, sink func(string)) (Result, error) {
	if sink == nil {
		return d.Run(ctx, cmd, opts)
	}
	return d.run(ctx, cmd, opts, &lineSink{sink: sink})
}

type lineSink struct {
	sink func(string)
}

func (s *lineSink) Write(p []byte) (int, error) {
	if len(p) > 0 {
		s.sink(string(p))
	}
	return len(p), nil
}

func (d *Dialer) run(ctx context.Context, cmd string, opts RunOptions, out io.Writer) (Result, error) {
	if err := d.connect(ctx); err != nil {
		return Result{ExitCode: -1}, err
	}
	session, err := d.client.NewSession()
	if err != nil {
		d.reset()
		return Result{ExitCode: -1}, fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	full := BuildCommand(cmd, opts)
	session.Stdout = out
	session.Stderr = out
	stdin := opts.StdinText
	if opts.Sudo.NeedsPassword() {
		// The password goes only over stdin; it is never part of the
		// command string, so it cannot leak into logs or process listings.
		if stdin != "" {
			stdin = opts.Sudo.Password + "\n" + stdin
		} else {
			stdin = opts.Sudo.Password + "\n"
		}
	}
	if stdin != "" {
		session.Stdin = strings.NewReader(stdin)
	}

	done := make(chan error, 1)
	go func() { done <- session.Run(full) }()

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		session.Close()
		<-done
		return Result{ExitCode: -1}, ctx.Err()
	case <-time.After(timeout):
		_ = session.Signal(ssh.SIGKILL)
		session.Close()
		<-done
		return Result{ExitCode: -1}, fmt.Errorf("command timed out after %s", timeout)
	case err := <-done:
		res := Result{ExitCode: 0}
		if err != nil {
			var exitErr *ssh.ExitError
			if errors.As(err, &exitErr) {
				res.ExitCode = exitErr.ExitStatus()
			} else {
				d.reset()
				return Result{ExitCode: -1}, err
			}
		}
		return res, nil
	}
}

func (d *Dialer) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.client != nil {
		d.client.Close()
		d.client = nil
	}
}

func (d *Dialer) Upload(ctx context.Context, path, content, mode string) error {
	if mode == "" {
		mode = "0600"
	}
	dir := path
	if i := strings.LastIndex(path, "/"); i > 0 {
		dir = path[:i]
	}
	script := fmt.Sprintf(
		"set -e; mkdir -p -- %s; umask 077; cat > %s.1mate-tmp; chmod %s %s.1mate-tmp; mv -f %s.1mate-tmp %s",
		Quote(dir), Quote(path), mode, Quote(path), Quote(path), Quote(path))
	return d.runWithInput(ctx, script, content, 2*time.Minute)
}

func (d *Dialer) runWithInput(ctx context.Context, script, stdin string, timeout time.Duration) error {
	res, err := d.Run(ctx, script, RunOptions{StdinText: stdin, Timeout: timeout})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("remote write failed (exit %d): %s", res.ExitCode, strings.TrimSpace(res.Stdout))
	}
	return nil
}

func (d *Dialer) Download(ctx context.Context, path string, w io.Writer) (int64, error) {
	return d.DownloadWithCommand(ctx, "cat -- "+Quote(path), RunOptions{Timeout: 2 * time.Hour}, w)
}

func (d *Dialer) DownloadWithCommand(ctx context.Context, cmd string, opts RunOptions, w io.Writer) (int64, error) {
	if err := d.connect(ctx); err != nil {
		return 0, err
	}
	session, err := d.client.NewSession()
	if err != nil {
		d.reset()
		return 0, err
	}
	defer session.Close()
	stdout, err := session.StdoutPipe()
	if err != nil {
		return 0, err
	}
	var stderr bytes.Buffer
	session.Stderr = &stderr
	full := BuildCommand(cmd, opts)
	if opts.Sudo.NeedsPassword() {
		session.Stdin = strings.NewReader(opts.Sudo.Password + "\n")
	}
	if err := session.Start(full); err != nil {
		return 0, err
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Hour
	}
	type copyResult struct {
		n   int64
		err error
	}
	copied := make(chan copyResult, 1)
	go func() {
		n, err := io.Copy(w, stdout)
		copied <- copyResult{n, err}
	}()
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		session.Close()
		<-copied
		return 0, ctx.Err()
	case <-time.After(timeout):
		_ = session.Signal(ssh.SIGKILL)
		session.Close()
		<-copied
		return 0, fmt.Errorf("stream timed out after %s", timeout)
	case cr := <-copied:
		if err := session.Wait(); err != nil && cr.err == nil {
			return cr.n, fmt.Errorf("remote command failed: %v: %s", err, strings.TrimSpace(stderr.String()))
		}
		return cr.n, cr.err
	}
}

func (d *Dialer) UploadReader(ctx context.Context, path string, r io.Reader, mode string, opts RunOptions) error {
	if mode == "" {
		mode = "0600"
	}
	if err := d.connect(ctx); err != nil {
		return err
	}
	session, err := d.client.NewSession()
	if err != nil {
		d.reset()
		return err
	}
	defer session.Close()
	dir := path
	if i := strings.LastIndex(path, "/"); i > 0 {
		dir = path[:i]
	}
	script := fmt.Sprintf(
		"set -e; mkdir -p -- %s; umask 077; cat > %s.1mate-tmp; chmod %s %s.1mate-tmp; mv -f %s.1mate-tmp %s",
		Quote(dir), Quote(path), mode, Quote(path), Quote(path), Quote(path))
	prelude := ""
	if opts.Sudo.NeedsPassword() {
		prelude = opts.Sudo.Password + "\n"
	}
	session.Stdin = io.MultiReader(strings.NewReader(prelude), r)
	var stderr bytes.Buffer
	session.Stdout = &stderr
	session.Stderr = &stderr
	if err := session.Run(BuildCommand(script, opts)); err != nil {
		return fmt.Errorf("remote write failed: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (d *Dialer) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.client == nil {
		return nil
	}
	err := d.client.Close()
	d.client = nil
	return err
}

// BuildCommand joins the caller prefix, the sudo prefix and the command. When
// sudo needs a password the command is wrapped so sudo reads it from stdin.
func BuildCommand(cmd string, opts RunOptions) string {
	inner := cmd
	if p := opts.Sudo.Prefix(); p != "" {
		inner = p + inner
	}
	full := opts.Prefix + inner
	if opts.Sudo.NeedsPassword() {
		full = opts.Prefix + "sudo -S -p '' sh -c " + Quote(inner)
	}
	return full
}

// Quote single-quotes a value for POSIX shells.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
