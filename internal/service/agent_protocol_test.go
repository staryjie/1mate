package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"1mate/internal/model"
)

// fakeAgentWS 实现一个最小的 Agent WebSocket 连接，用于协议级测试。
// 写入的命令被记录，读取时返回预先排队的结果帧。
type fakeAgentWS struct {
	mu       sync.Mutex
	commands []AgentCommand
	results  chan []byte
	closed   bool
}

func newFakeAgentWS() *fakeAgentWS {
	return &fakeAgentWS{results: make(chan []byte, 8)}
}

func (f *fakeAgentWS) WriteJSON(v any) error {
	b, _ := json.Marshal(v)
	var cmd AgentCommand
	_ = json.Unmarshal(b, &cmd)
	f.mu.Lock()
	f.commands = append(f.commands, cmd)
	f.mu.Unlock()
	return nil
}

func (f *fakeAgentWS) ReadMessage() (int, []byte, error) {
	b := <-f.results
	return 1, b, nil
}

func (f *fakeAgentWS) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

// reply 模拟 Agent 回传命令结果。
func (f *fakeAgentWS) reply(t *testing.T, cmdID string, stdout string) {
	t.Helper()
	b, _ := json.Marshal(AgentResult{ID: cmdID, ExitCode: 0, Stdout: stdout})
	select {
	case f.results <- b:
	default:
		t.Fatal("result channel full")
	}
}

// TestFindHostByAgentToken 验证 token 反查主机（协议修复的核心：机器认证）。
func TestFindHostByAgentToken(t *testing.T) {
	h := newHarness(t)
	host, err := h.svc.CreateHost(HostInput{
		Name:           "agent-host",
		Address:        "192.168.0.114",
		ConnectionType: model.ConnTypeAgent,
		AgentToken:     "test-token-with-16chars",
	})
	if err != nil {
		t.Fatalf("create agent host: %v", err)
	}
	if host.ConnectionType != model.ConnTypeAgent {
		t.Fatalf("expected agent connection type, got %s", host.ConnectionType)
	}

	// 正确 token 应能反查到主机。
	found, err := h.store.FindHostByAgentToken("test-token-with-16chars")
	if err != nil {
		t.Fatalf("find by token: %v", err)
	}
	if found.ID != host.ID {
		t.Fatalf("expected host %s, got %s", host.ID, found.ID)
	}
	if found.AgentID != host.AgentID {
		t.Fatalf("expected agent id %s, got %s", host.AgentID, found.AgentID)
	}

	// 错误 token / 空 token 应查不到。
	if _, err := h.store.FindHostByAgentToken("wrong-token"); err == nil {
		t.Fatal("expected error for wrong token")
	}
	if _, err := h.store.FindHostByAgentToken(""); err == nil {
		t.Fatal("expected error for empty token")
	}
}

// TestAgentCommandRoundTrip 验证「注册 → 下发命令 → 回传结果 → 匹配 pending」全链路。
func TestAgentCommandRoundTrip(t *testing.T) {
	hub := NewAgentHub(slog.Default())
	ws := newFakeAgentWS()
	hub.Register("agent-test-01", "host-01", ws)
	defer hub.Unregister("agent-test-01")

	if !hub.IsConnected("agent-test-01") {
		t.Fatal("agent should be connected")
	}
	if got := hub.HostIDForAgent("agent-test-01"); got != "host-01" {
		t.Fatalf("expected host-01, got %s", got)
	}

	// 在 goroutine 中发送命令（会阻塞等待结果），随后模拟 Agent 回传。
	type result struct {
		res *AgentResult
		err error
	}
	ch := make(chan result, 1)
	go func() {
		res, err := hub.SendCommand(context.Background(), "agent-test-01", "echo hello", 5)
		ch <- result{res, err}
	}()

	// 等待命令写入 fake ws。
	var cmd AgentCommand
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ws.mu.Lock()
		if len(ws.commands) > 0 {
			cmd = ws.commands[0]
			ws.mu.Unlock()
			break
		}
		ws.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	if cmd.Cmd != "echo hello" {
		t.Fatalf("expected command 'echo hello', got %q", cmd.Cmd)
	}
	if cmd.ID == "" {
		t.Fatal("command ID should not be empty")
	}

	// 模拟 Agent 回传结果。
	ws.reply(t, cmd.ID, "hello\n")

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("send command: %v", r.err)
		}
		if r.res.ExitCode != 0 || r.res.Stdout != "hello\n" {
			t.Fatalf("unexpected result: %+v", r.res)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("command did not return in time")
	}
}

// TestAgentNotConnected 验证未连接的 Agent 发送命令返回明确错误。
func TestAgentNotConnected(t *testing.T) {
	hub := NewAgentHub(slog.Default())
	_, err := hub.SendCommand(context.Background(), "no-such-agent", "echo x", 2)
	if err != ErrAgentNotConnected {
		t.Fatalf("expected ErrAgentNotConnected, got %v", err)
	}
}
