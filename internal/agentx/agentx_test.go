package agentx

import (
	"context"
	"errors"
	"strings"
	"testing"

	"1mate/internal/sshx"
)

// mockCommander 实现 AgentCommander，用于单元测试。
type mockCommander struct {
	connected bool
	calls     []string
	results   map[string]sshx.Result // cmd -> result
	err       error
}

func newMockCommander(connected bool) *mockCommander {
	return &mockCommander{
		connected: connected,
		results:   make(map[string]sshx.Result),
	}
}

func (m *mockCommander) SendCommand(ctx context.Context, agentID string, cmd string, timeoutSeconds int) (sshx.Result, error) {
	m.calls = append(m.calls, cmd)
	if m.err != nil {
		return sshx.Result{}, m.err
	}
	if res, ok := m.results[cmd]; ok {
		return res, nil
	}
	return sshx.Result{ExitCode: 0}, nil
}

func (m *mockCommander) IsConnected(agentID string) bool {
	return m.connected
}

func TestClientHostKeyFingerprint(t *testing.T) {
	m := newMockCommander(true)
	c := New("agent-123", m)
	fp := c.HostKeyFingerprint()
	if fp != "agent:agent-123" {
		t.Fatalf("unexpected fingerprint: %q", fp)
	}
}

func TestClientRunOffline(t *testing.T) {
	m := newMockCommander(false)
	c := New("agent-123", m)
	_, err := c.Run(context.Background(), "echo hello", sshx.RunOptions{})
	if !errors.Is(err, ErrAgentOffline) {
		t.Fatalf("expected ErrAgentOffline, got %v", err)
	}
}

func TestClientRunSuccess(t *testing.T) {
	m := newMockCommander(true)
	m.results["echo hello"] = sshx.Result{ExitCode: 0, Stdout: "hello\n"}
	c := New("agent-123", m)
	res, err := c.Run(context.Background(), "echo hello", sshx.RunOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode != 0 || res.Stdout != "hello\n" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if len(m.calls) != 1 || m.calls[0] != "echo hello" {
		t.Fatalf("unexpected calls: %v", m.calls)
	}
}

func TestClientRunWithSudo(t *testing.T) {
	m := newMockCommander(true)
	m.results["sudo -n whoami"] = sshx.Result{ExitCode: 0, Stdout: "root\n"}
	c := New("agent-123", m)
	res, err := c.Run(context.Background(), "whoami", sshx.RunOptions{Sudo: sshx.Sudo{Mode: "nopasswd"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Stdout != "root\n" {
		t.Fatalf("unexpected stdout: %q", res.Stdout)
	}
	if len(m.calls) != 1 || m.calls[0] != "sudo -n whoami" {
		t.Fatalf("unexpected calls: %v", m.calls)
	}
}

func TestClientRunStreaming(t *testing.T) {
	m := newMockCommander(true)
	m.results["ls -la"] = sshx.Result{ExitCode: 0, Stdout: "file1\nfile2\n", Stderr: ""}
	c := New("agent-123", m)
	var output []string
	res, err := c.RunStreaming(context.Background(), "ls -la", sshx.RunOptions{}, func(s string) {
		output = append(output, s)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("unexpected exit code: %d", res.ExitCode)
	}
	if len(output) != 1 || output[0] != "file1\nfile2\n" {
		t.Fatalf("unexpected sink output: %v", output)
	}
}

func TestClientUpload(t *testing.T) {
	m := newMockCommander(true)
	c := New("agent-123", m)
	content := "hello world\n"
	err := c.Upload(context.Background(), "/tmp/test.txt", content, "0644")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(m.calls), m.calls)
	}
	// 验证命令包含 base64 编码的内容和正确的路径
	cmd := m.calls[0]
	if !strings.Contains(cmd, "base64 -d") {
		t.Fatalf("command should use base64 decoding: %s", cmd)
	}
	if !strings.Contains(cmd, "/tmp/test.txt") {
		t.Fatalf("command should contain target path: %s", cmd)
	}
	if !strings.Contains(cmd, "mv -f") {
		t.Fatalf("command should use atomic mv: %s", cmd)
	}
}

func TestClientUploadReader(t *testing.T) {
	m := newMockCommander(true)
	c := New("agent-123", m)
	content := "streamed content\n"
	err := c.UploadReader(context.Background(), "/tmp/stream.txt", strings.NewReader(content), "0644", sshx.RunOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(m.calls), m.calls)
	}
}

func TestClientDownload(t *testing.T) {
	m := newMockCommander(true)
	m.results["cat -- '/tmp/data.txt'"] = sshx.Result{ExitCode: 0, Stdout: "file content\n"}
	c := New("agent-123", m)
	var buf strings.Builder
	n, err := c.Download(context.Background(), "/tmp/data.txt", &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.String() != "file content\n" {
		t.Fatalf("unexpected downloaded content: %q", buf.String())
	}
	if n != int64(len("file content\n")) {
		t.Fatalf("unexpected byte count: %d", n)
	}
}

func TestClientDownloadFailure(t *testing.T) {
	m := newMockCommander(true)
	m.results["cat -- '/tmp/missing.txt'"] = sshx.Result{ExitCode: 1, Stderr: "No such file"}
	c := New("agent-123", m)
	var buf strings.Builder
	_, err := c.Download(context.Background(), "/tmp/missing.txt", &buf)
	if err == nil {
		t.Fatal("expected error for failed download")
	}
}

func TestClientDownloadWithCommand(t *testing.T) {
	m := newMockCommander(true)
	m.results["pg_dump -U teslamate teslamate_db"] = sshx.Result{ExitCode: 0, Stdout: "backup data\n"}
	c := New("agent-123", m)
	var buf strings.Builder
	n, err := c.DownloadWithCommand(context.Background(), "pg_dump -U teslamate teslamate_db", sshx.RunOptions{}, &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.String() != "backup data\n" {
		t.Fatalf("unexpected stream content: %q", buf.String())
	}
	if n != int64(len("backup data\n")) {
		t.Fatalf("unexpected byte count: %d", n)
	}
}

func TestClientRunWithStdin(t *testing.T) {
	m := newMockCommander(true)
	c := New("agent-123", m)
	input := "image data\n"
	err := c.RunWithStdin(context.Background(), "docker load", strings.NewReader(input), sshx.RunOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(m.calls), m.calls)
	}
	cmd := m.calls[0]
	if !strings.Contains(cmd, "base64 -d") {
		t.Fatalf("command should use base64 decoding for stdin: %s", cmd)
	}
	if !strings.Contains(cmd, "docker load") {
		t.Fatalf("command should contain target command: %s", cmd)
	}
}

func TestClientRunWithStdinFailure(t *testing.T) {
	m := newMockCommander(true)
	m.err = errors.New("command execution failed")
	c := New("agent-123", m)
	err := c.RunWithStdin(context.Background(), "docker load", strings.NewReader("data"), sshx.RunOptions{})
	if err == nil {
		t.Fatal("expected error for failed stdin command")
	}
}

func TestClientPtyStartOffline(t *testing.T) {
	m := newMockCommander(false)
	c := New("agent-123", m)
	_, err := c.PtyStart(context.Background(), "xterm", 24, 80, sshx.RunOptions{})
	if !errors.Is(err, ErrAgentOffline) {
		t.Fatalf("expected ErrAgentOffline, got %v", err)
	}
}

func TestClientPtyStartNotImplemented(t *testing.T) {
	m := newMockCommander(true)
	c := New("agent-123", m)
	_, err := c.PtyStart(context.Background(), "xterm", 24, 80, sshx.RunOptions{})
	if !errors.Is(err, ErrAgentPTYNotImplemented) {
		t.Fatalf("expected ErrAgentPTYNotImplemented, got %v", err)
	}
}

func TestClientClose(t *testing.T) {
	m := newMockCommander(true)
	c := New("agent-123", m)
	if err := c.Close(); err != nil {
		t.Fatalf("Close should not error: %v", err)
	}
}

func TestClientConcurrentRun(t *testing.T) {
	m := newMockCommander(true)
	m.results["cmd1"] = sshx.Result{ExitCode: 0, Stdout: "result1"}
	m.results["cmd2"] = sshx.Result{ExitCode: 0, Stdout: "result2"}
	c := New("agent-123", m)

	done := make(chan error, 2)
	go func() {
		_, err := c.Run(context.Background(), "cmd1", sshx.RunOptions{})
		done <- err
	}()
	go func() {
		_, err := c.Run(context.Background(), "cmd2", sshx.RunOptions{})
		done <- err
	}()

	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent run failed: %v", err)
		}
	}
	if len(m.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d: %v", len(m.calls), m.calls)
	}
}
