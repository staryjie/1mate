package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"1mate/internal/model"
)

// fakeAgentWS 实现一个最小的 Agent WebSocket 连接，用于协议级测试。
// 写入的帧被记录，读取时返回预先排队的帧；Close 会解除 ReadMessage 阻塞，
// 避免 readLoop goroutine 泄漏。
type fakeAgentWS struct {
	mu     sync.Mutex
	frames []wireFrame
	toSrv  chan []byte
	done   chan struct{}
	once   sync.Once
	closed bool

	// onWrite 允许测试在收到服务器帧时即时回应（模拟真实 Agent）。
	onWrite func(f wireFrame, self *fakeAgentWS)
}

func newFakeAgentWS() *fakeAgentWS {
	return &fakeAgentWS{
		toSrv: make(chan []byte, 1024),
		done:  make(chan struct{}),
	}
}

func (f *fakeAgentWS) WriteJSON(v any) error {
	b, _ := json.Marshal(v)
	var fr wireFrame
	if err := json.Unmarshal(b, &fr); err != nil {
		return err
	}
	f.mu.Lock()
	f.frames = append(f.frames, fr)
	hook := f.onWrite
	f.mu.Unlock()
	if hook != nil {
		hook(fr, f)
	}
	return nil
}

func (f *fakeAgentWS) ReadMessage() (int, []byte, error) {
	select {
	case b := <-f.toSrv:
		return 1, b, nil
	case <-f.done:
		return 0, nil, io.EOF
	}
}

func (f *fakeAgentWS) Close() error {
	f.once.Do(func() {
		f.mu.Lock()
		f.closed = true
		f.mu.Unlock()
		close(f.done)
	})
	return nil
}

// push 把一帧排入「服务器将读到」的队列。
func (f *fakeAgentWS) push(t *testing.T, fr wireFrame) {
	t.Helper()
	b, err := json.Marshal(fr)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	select {
	case f.toSrv <- b:
	default:
		t.Fatal("fake agent frame queue full")
	}
}

// reply 模拟 Agent 回传命令结果。
func (f *fakeAgentWS) reply(t *testing.T, cmdID string, stdout string) {
	t.Helper()
	f.push(t, wireFrame{Type: frameResult, CmdID: cmdID, Stdout: stdout})
}

// waitFrame 等待一个满足条件的出站帧（服务器 → Agent）。
func (f *fakeAgentWS) waitFrame(t *testing.T, pred func(wireFrame) bool, timeout time.Duration) wireFrame {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, fr := range f.frames {
			if pred(fr) {
				f.mu.Unlock()
				return fr
			}
		}
		f.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("expected frame not observed")
	return wireFrame{}
}

func (f *fakeAgentWS) countOf(typ string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, fr := range f.frames {
		if fr.Type == typ {
			n++
		}
	}
	return n
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

	if _, err := h.store.FindHostByAgentToken("wrong-token"); err == nil {
		t.Fatal("expected error for wrong token")
	}
	if _, err := h.store.FindHostByAgentToken(""); err == nil {
		t.Fatal("expected error for empty token")
	}
}

// TestAgentCommandRoundTrip 验证「注册 → 下发命令 → 回传结果 → 匹配等待者」全链路。
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

	type outcome struct {
		res *AgentResult
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		res, err := hub.SendCommand(context.Background(), "agent-test-01", "echo hello", 5)
		ch <- outcome{res, err}
	}()

	sent := ws.waitFrame(t, func(f wireFrame) bool { return f.Type == frameCmd }, 2*time.Second)
	if sent.Cmd != "echo hello" {
		t.Fatalf("expected command 'echo hello', got %q", sent.Cmd)
	}
	if sent.CmdID == "" {
		t.Fatal("command ID should not be empty")
	}

	ws.reply(t, sent.CmdID, "hello\n")

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

// TestAgentStreamingOutput 验证 out 帧被实时回调（G4）。
func TestAgentStreamingOutput(t *testing.T) {
	hub := NewAgentHub(slog.Default())
	ws := newFakeAgentWS()
	hub.Register("agent-stream", "host-s", ws)
	defer hub.Unregister("agent-stream")

	var mu sync.Mutex
	var got []string

	done := make(chan *AgentResult, 1)
	go func() {
		res, err := hub.RunCommand(context.Background(), "agent-stream", "make build", CommandOptions{
			Timeout: 5 * time.Second,
			OnOutput: func(stream, text string) {
				mu.Lock()
				got = append(got, stream+":"+text)
				mu.Unlock()
			},
		})
		if err != nil {
			t.Errorf("run command: %v", err)
		}
		done <- res
	}()

	sent := ws.waitFrame(t, func(f wireFrame) bool { return f.Type == frameCmd }, 2*time.Second)
	ws.push(t, wireFrame{Type: frameOut, CmdID: sent.CmdID, Stream: "stdout", Data: "step 1\n"})
	ws.push(t, wireFrame{Type: frameOut, CmdID: sent.CmdID, Stream: "stderr", Data: "warn\n"})
	ws.push(t, wireFrame{Type: frameResult, CmdID: sent.CmdID, Stdout: "step 1\n"})

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("command did not finish")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("expected 2 streamed chunks, got %v", got)
	}
	if got[0] != "stdout:step 1\n" || got[1] != "stderr:warn\n" {
		t.Fatalf("unexpected streamed output: %v", got)
	}
}

// TestAgentUploadChunks 验证分块上传逐块确认（G2）。
func TestAgentUploadChunks(t *testing.T) {
	hub := NewAgentHub(slog.Default())
	ws := newFakeAgentWS()
	// 每收到一个数据块就回 ack，模拟真实 Agent。
	ws.onWrite = func(f wireFrame, self *fakeAgentWS) {
		switch f.Type {
		case frameUpChunk:
			self.push(t, wireFrame{Type: frameUpAck, XferID: f.XferID, Seq: f.Seq})
		case frameUpEnd:
			self.push(t, wireFrame{Type: frameUpDone, XferID: f.XferID})
		}
	}
	hub.Register("agent-up", "host-u", ws)
	defer hub.Unregister("agent-up")

	// 3.5 个块，验证最后一块不足额也能正确收尾。
	payload := bytes.Repeat([]byte("a"), agentChunkSize*3+1234)
	err := hub.Upload(context.Background(), "agent-up", "/tmp/big.bin", "0644", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	begin := ws.waitFrame(t, func(f wireFrame) bool { return f.Type == frameUpBegin }, 2*time.Second)
	if begin.Path != "/tmp/big.bin" || begin.Mode != "0644" {
		t.Fatalf("unexpected begin frame: %+v", begin)
	}
	if n := ws.countOf(frameUpChunk); n != 4 {
		t.Fatalf("expected 4 chunks, got %d", n)
	}
	if n := ws.countOf(frameUpEnd); n != 1 {
		t.Fatalf("expected 1 up_end, got %d", n)
	}

	// 拼回内容应与原始数据一致。
	ws.mu.Lock()
	var reassembled []byte
	for _, f := range ws.frames {
		if f.Type == frameUpChunk {
			data, derr := base64.StdEncoding.DecodeString(f.B64)
			if derr != nil {
				t.Fatalf("decode chunk: %v", derr)
			}
			reassembled = append(reassembled, data...)
		}
	}
	ws.mu.Unlock()
	if !bytes.Equal(reassembled, payload) {
		t.Fatalf("reassembled payload mismatch: %d vs %d bytes", len(reassembled), len(payload))
	}
}

// TestAgentDownloadStreams 验证下载边收边写（G3）。
func TestAgentDownloadStreams(t *testing.T) {
	hub := NewAgentHub(slog.Default())
	ws := newFakeAgentWS()
	ws.onWrite = func(f wireFrame, self *fakeAgentWS) {
		if f.Type != frameDownBegin {
			return
		}
		for i, part := range []string{"chunk-1;", "chunk-2;", "chunk-3"} {
			self.push(t, wireFrame{
				Type:   frameDownChunk,
				XferID: f.XferID,
				Seq:    i,
				B64:    base64.StdEncoding.EncodeToString([]byte(part)),
			})
		}
		self.push(t, wireFrame{Type: frameDownEnd, XferID: f.XferID})
	}
	hub.Register("agent-down", "host-d", ws)
	defer hub.Unregister("agent-down")

	var sink bytes.Buffer
	n, err := hub.Download(context.Background(), "agent-down", "pg_dump -Fc db", &sink)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	want := "chunk-1;chunk-2;chunk-3"
	if sink.String() != want {
		t.Fatalf("expected %q, got %q", want, sink.String())
	}
	if n != int64(len(want)) {
		t.Fatalf("expected %d bytes, got %d", len(want), n)
	}
}

// TestAgentCancelOnContext 验证取消会向 Agent 下发 cancel 帧（G5）。
func TestAgentCancelOnContext(t *testing.T) {
	hub := NewAgentHub(slog.Default())
	ws := newFakeAgentWS()
	hub.Register("agent-cancel", "host-c", ws)
	defer hub.Unregister("agent-cancel")

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := hub.RunCommand(ctx, "agent-cancel", "sleep 600", CommandOptions{Timeout: 30 * time.Second})
		errc <- err
	}()

	sent := ws.waitFrame(t, func(f wireFrame) bool { return f.Type == frameCmd }, 2*time.Second)
	cancel()

	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("expected context error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("command did not abort")
	}

	cancelFrame := ws.waitFrame(t, func(f wireFrame) bool { return f.Type == frameCancel }, 2*time.Second)
	if cancelFrame.CmdID != sent.CmdID {
		t.Fatalf("cancel for %q, expected %q", cancelFrame.CmdID, sent.CmdID)
	}
}

// TestAgentDisconnectFailsWaiters 验证连接断开时等待者立即失败而不是挂到超时。
func TestAgentDisconnectFailsWaiters(t *testing.T) {
	hub := NewAgentHub(slog.Default())
	ws := newFakeAgentWS()
	hub.Register("agent-gone", "host-g", ws)

	errc := make(chan error, 1)
	go func() {
		_, err := hub.RunCommand(context.Background(), "agent-gone", "sleep 600", CommandOptions{Timeout: 30 * time.Second})
		errc <- err
	}()

	ws.waitFrame(t, func(f wireFrame) bool { return f.Type == frameCmd }, 2*time.Second)
	hub.Unregister("agent-gone")

	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("expected error after disconnect")
		}
		if !strings.Contains(err.Error(), "disconnected") {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiter was not released on disconnect")
	}
}