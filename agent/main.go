// Agent 客户端 — 独立 Go 二进制，运行在目标主机上，通过 WebSocket 连接到 1mate 服务器。
//
// 线协议（全部 JSON 文本帧，二进制内容以 base64 放在 b64 字段）：
//
//	服务器 → Agent：cmd / cancel / up_begin / up_chunk / up_end / down_begin
//	Agent → 服务器：hello / out / result / up_ack / up_done / up_error /
//	                down_chunk / down_end / pong
//
// 设计要点：
//   - 单写者：所有出站帧都进同一个 channel，由唯一一个 goroutine 写 WS。
//     gorilla/websocket 的写方法不加锁，并发写会交错破坏帧（WebSocket
//     不允许帧交错），因此绝不能在多个 goroutine 里直接 WriteMessage。
//   - 流式输出：命令执行期间的 stdout/stderr 以 out 帧增量推送，同时保留
//     一份（上限 8MB）用于 result 帧，兼容只读最终结果的旧调用方。
//   - 分块传输：up_chunk 逐块确认（up_ack），down_chunk 边产边发，都不受
//     单条命令长度上限（约 128KB）限制，也不会把整个文件读进内存。
//   - sudo：密码只走 sudo_password 字段，由 Agent 用 `sudo -S` 从 stdin 读入，
//     不拼进命令行，也不写日志。
//   - 取消：服务器可下发 cancel 帧中止命令；本地按 cmdID 保存 cancelFunc。
//
// 环境变量：
//
//	AGENT_SERVER_URL   WebSocket 服务器地址，如 ws://127.0.0.1:8080/api/v1/agent/connect
//	AGENT_TOKEN        Agent 认证令牌（与主机记录绑定，必填；通过 X-Agent-Token 请求头发送）
//	AGENT_HOST_ID      主机标识（可选，仅用于日志显示）
//	AGENT_INTERVAL     心跳间隔（秒），默认 30
//	AGENT_RETRY_BASE   重连基础延迟（秒），默认 5
//	AGENT_RETRY_MAX    最大重连延迟（秒），默认 120
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// version 由构建时通过 -ldflags "-X main.version=..." 注入。
var version = "dev"

// 帧类型常量，必须与服务器 internal/service/agent.go 保持一致。
const (
	frameHello     = "hello"
	frameCmd       = "cmd"
	frameOut       = "out"
	frameResult    = "result"
	frameCancel    = "cancel"
	frameUpBegin   = "up_begin"
	frameUpChunk   = "up_chunk"
	frameUpEnd     = "up_end"
	frameUpAck     = "up_ack"
	frameUpDone    = "up_done"
	frameUpError   = "up_error"
	frameDownBegin = "down_begin"
	frameDownChunk = "down_chunk"
	frameDownEnd   = "down_end"
	framePong      = "pong"
)

// 单帧 base64 载荷与 result 帧的保留上限。
const (
	chunkPayloadSize = 32 * 1024
	resultKeepLimit  = 8 << 20 // 8MB
)

// frame 是 Agent 通道上的统一帧格式。
type frame struct {
	Type string `json:"type"`

	Version string `json:"version,omitempty"`
	OS      string `json:"os,omitempty"`
	Arch    string `json:"arch,omitempty"`

	CmdID        string `json:"cmd_id,omitempty"`
	Cmd          string `json:"cmd,omitempty"`
	Stdin        string `json:"stdin,omitempty"`
	SudoPassword string `json:"sudo_password,omitempty"`
	Timeout      int    `json:"timeout,omitempty"`

	Stream string `json:"stream,omitempty"`
	Data   string `json:"data,omitempty"`

	ExitCode int    `json:"exit_code,omitempty"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	Error    string `json:"error,omitempty"`

	XferID string `json:"xfer_id,omitempty"`
	Path   string `json:"path,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Seq    int    `json:"seq,omitempty"`
	B64    string `json:"b64,omitempty"`
}

// Config 从环境变量读取的 Agent 配置。
type Config struct {
	ServerURL string
	Token     string
	HostID    string
	Heartbeat time.Duration
	RetryBase time.Duration
	RetryMax  time.Duration
}

func loadConfig() (*Config, error) {
	serverURL := os.Getenv("AGENT_SERVER_URL")
	if serverURL == "" {
		serverURL = "ws://127.0.0.1:8080/api/v1/agent/connect"
	}
	token := os.Getenv("AGENT_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("AGENT_TOKEN is required")
	}
	// AGENT_HOST_ID 可选：仅用于日志显示，不参与认证与路由。
	hostID := os.Getenv("AGENT_HOST_ID")

	heartbeat := 30 * time.Second
	if v := os.Getenv("AGENT_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			heartbeat = time.Duration(n) * time.Second
		}
	}
	retryBase := 5 * time.Second
	if v := os.Getenv("AGENT_RETRY_BASE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			retryBase = time.Duration(n) * time.Second
		}
	}
	retryMax := 120 * time.Second
	if v := os.Getenv("AGENT_RETRY_MAX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			retryMax = time.Duration(n) * time.Second
		}
	}

	return &Config{
		ServerURL: serverURL,
		Token:     token,
		HostID:    hostID,
		Heartbeat: heartbeat,
		RetryBase: retryBase,
		RetryMax:  retryMax,
	}, nil
}

// connectWS 建立与服务器的 WebSocket 连接。令牌通过 X-Agent-Token 请求头
// 发送（G7）：URL 里不再出现令牌，避免被反向代理访问日志记录。
func connectWS(serverURL, token string) (*websocket.Conn, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("invalid server url: %w", err)
	}
	if u.Scheme == "" {
		u.Scheme = "ws"
	}

	dialer := websocket.DefaultDialer
	headers := http.Header{}
	headers.Set("X-Agent-Token", token)
	headers.Set("Origin", serverURL)

	conn, _, err := dialer.Dial(u.String(), headers)
	if err != nil {
		return nil, fmt.Errorf("dial websocket: %w", err)
	}
	return conn, nil
}

// conn 维护一条 Agent 连接：单写者、活跃命令取消表、上传会话表。
type conn struct {
	ws     *websocket.Conn
	out    chan frame
	closed chan struct{}
	once   sync.Once

	mu      sync.Mutex
	active  map[string]context.CancelFunc
	uploads map[string]*uploadSession
}

type uploadSession struct {
	path string
	mode string
	file *os.File
}

func newConn(ws *websocket.Conn) *conn {
	c := &conn{
		ws:      ws,
		out:     make(chan frame, 256),
		closed:  make(chan struct{}),
		active:  make(map[string]context.CancelFunc),
		uploads: make(map[string]*uploadSession),
	}
	// 单写者：closing out 之后不再写。
	go c.writeLoop()
	return c
}

// writeLoop 是唯一执行 WS 写操作的地方。
func (c *conn) writeLoop() {
	for f := range c.out {
		if err := c.ws.WriteJSON(f); err != nil {
			log.Printf("write frame %s failed: %v", f.Type, err)
			c.close()
			return
		}
	}
}

// send 把一个帧排入写队列。队列满或连接关闭时立即返回错误，避免阻塞
// 命令执行线程（尤其在大文件传输时）。
func (c *conn) send(f frame) error {
	select {
	case c.out <- f:
		return nil
	case <-c.closed:
		return errors.New("connection closed")
	}
}

// close 关闭连接（幂等）。
func (c *conn) close() {
	c.once.Do(func() {
		close(c.closed)
		close(c.out)
		_ = c.ws.Close()
	})
}

// cancel 中止指定命令。
func (c *conn) cancel(cmdID string) {
	c.mu.Lock()
	cancel, ok := c.active[cmdID]
	c.mu.Unlock()
	if ok {
		log.Printf("canceling command %s", cmdID)
		cancel()
	}
}

// handle 分发单个入站帧。
func (c *conn) handle(f frame) {
	switch f.Type {
	case frameCmd:
		go c.runCommand(f)
	case frameCancel:
		c.cancel(f.CmdID)
	case frameUpBegin:
		c.uploadBegin(f)
	case frameUpChunk:
		c.uploadChunk(f)
	case frameUpEnd:
		c.uploadEnd(f)
	case frameDownBegin:
		go c.download(f)
	default:
		log.Printf("unknown frame type: %s", f.Type)
	}
}

// runCommand 执行命令，流式回传输出，最后回传 result 帧。
func (c *conn) runCommand(f frame) {
	// 独立 context：允许被 cancel 帧中止，也可带超时。
	var ctx context.Context
	var cancel context.CancelFunc
	if f.Timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(f.Timeout)*time.Second)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	defer cancel()

	c.mu.Lock()
	c.active[f.CmdID] = cancel
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.active, f.CmdID)
		c.mu.Unlock()
	}()

	cmd, stdin, err := buildExec(ctx, f)
	if err != nil {
		_ = c.send(frame{Type: frameResult, CmdID: f.CmdID, ExitCode: -1, Error: err.Error()})
		return
	}
	if stdin != nil {
		cmd.Stdin = stdin
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = c.send(frame{Type: frameResult, CmdID: f.CmdID, ExitCode: -1, Error: err.Error()})
		return
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = c.send(frame{Type: frameResult, CmdID: f.CmdID, ExitCode: -1, Error: err.Error()})
		return
	}
	if err := cmd.Start(); err != nil {
		_ = c.send(frame{Type: frameResult, CmdID: f.CmdID, ExitCode: -1, Error: err.Error()})
		return
	}

	var wg sync.WaitGroup
	var outBuf, errBuf strings.Builder
	keep := func(b *strings.Builder, s string) {
		if b.Len() < resultKeepLimit {
			b.WriteString(s)
		}
	}

	pump := func(r io.Reader, stream string, acc *strings.Builder) {
		defer wg.Done()
		buf := make([]byte, chunkPayloadSize)
		for {
			n, rerr := r.Read(buf)
			if n > 0 {
				text := string(buf[:n])
				keep(acc, text)
				if serr := c.send(frame{Type: frameOut, CmdID: f.CmdID, Stream: stream, Data: text}); serr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}
	wg.Add(2)
	go pump(stdoutPipe, "stdout", &outBuf)
	go pump(stderrPipe, "stderr", &errBuf)
	wg.Wait()

	waitErr := cmd.Wait()

	res := frame{
		Type:     frameResult,
		CmdID:    f.CmdID,
		Stdout:   outBuf.String(),
		Stderr:   errBuf.String(),
		ExitCode: 0,
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		switch {
		case errors.As(waitErr, &exitErr):
			res.ExitCode = exitErr.ExitCode()
		case ctx.Err() == context.DeadlineExceeded:
			res.ExitCode = -1
			res.Error = "command timed out"
		case ctx.Err() == context.Canceled:
			res.ExitCode = -1
			res.Error = "command canceled"
		default:
			res.ExitCode = -1
			res.Error = waitErr.Error()
		}
	}
	_ = c.send(res)
}

// buildExec 构造 exec.Cmd。sudo 密码通过 stdin 送入，不出现在命令行。
func buildExec(ctx context.Context, f frame) (*exec.Cmd, io.Reader, error) {
	if f.SudoPassword != "" {
		// sudo -S 从 stdin 读取密码；-p '' 抑制提示串，避免污染 stdout。
		cmd := exec.CommandContext(ctx, "sudo", "-S", "-p", "", "sh", "-c", f.Cmd)
		var stdin io.Reader
		if f.Stdin != "" {
			stdin = io.MultiReader(strings.NewReader(f.SudoPassword+"\n"), strings.NewReader(f.Stdin))
		} else {
			stdin = strings.NewReader(f.SudoPassword + "\n")
		}
		return cmd, stdin, nil
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", f.Cmd)
	if f.Stdin != "" {
		return cmd, strings.NewReader(f.Stdin), nil
	}
	return cmd, nil, nil
}

// uploadBegin 准备接收一个文件：建目录、创建临时文件。
func (c *conn) uploadBegin(f frame) {
	if f.Path == "" {
		_ = c.send(frame{Type: frameUpError, XferID: f.XferID, Error: "missing path"})
		return
	}
	dir := filepath.Dir(f.Path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			_ = c.send(frame{Type: frameUpError, XferID: f.XferID, Error: err.Error()})
			return
		}
	}
	tmp := f.Path + ".1mate-tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = c.send(frame{Type: frameUpError, XferID: f.XferID, Error: err.Error()})
		return
	}
	c.mu.Lock()
	c.uploads[f.XferID] = &uploadSession{path: f.Path, mode: f.Mode, file: file}
	c.mu.Unlock()
}

// uploadChunk 写入一块数据并回 ack。
func (c *conn) uploadChunk(f frame) {
	c.mu.Lock()
	sess, ok := c.uploads[f.XferID]
	c.mu.Unlock()
	if !ok {
		_ = c.send(frame{Type: frameUpError, XferID: f.XferID, Error: "unknown transfer"})
		return
	}
	data, err := base64.StdEncoding.DecodeString(f.B64)
	if err != nil {
		_ = c.send(frame{Type: frameUpError, XferID: f.XferID, Error: "bad base64: " + err.Error()})
		return
	}
	if _, err := sess.file.Write(data); err != nil {
		_ = c.send(frame{Type: frameUpError, XferID: f.XferID, Error: err.Error()})
		return
	}
	_ = c.send(frame{Type: frameUpAck, XferID: f.XferID, Seq: f.Seq})
}

// uploadEnd 收尾：关文件、chmod、原子改名。
func (c *conn) uploadEnd(f frame) {
	c.mu.Lock()
	sess, ok := c.uploads[f.XferID]
	delete(c.uploads, f.XferID)
	c.mu.Unlock()
	if !ok {
		_ = c.send(frame{Type: frameUpError, XferID: f.XferID, Error: "unknown transfer"})
		return
	}

	tmp := sess.path + ".1mate-tmp"
	if err := sess.file.Close(); err != nil {
		_ = c.send(frame{Type: frameUpError, XferID: f.XferID, Error: err.Error()})
		return
	}
	mode := sess.mode
	if mode == "" {
		mode = "0644"
	}
	if perm, perr := strconv.ParseUint(strings.TrimPrefix(mode, "0o"), 8, 32); perr == nil {
		if err := os.Chmod(tmp, os.FileMode(perm)); err != nil {
			_ = c.send(frame{Type: frameUpError, XferID: f.XferID, Error: err.Error()})
			return
		}
	}
	if err := os.Rename(tmp, sess.path); err != nil {
		_ = c.send(frame{Type: frameUpError, XferID: f.XferID, Error: err.Error()})
		return
	}
	_ = c.send(frame{Type: frameUpDone, XferID: f.XferID})
}

// download 执行命令并把 stdout 分块推给服务器（用于 pg_dump / docker save 等）。
func (c *conn) download(f frame) {
	cmd, stdin, err := buildExec(context.Background(), frame{Cmd: f.Cmd, SudoPassword: f.SudoPassword})
	if err != nil {
		_ = c.send(frame{Type: frameDownEnd, XferID: f.XferID, ExitCode: -1, Error: err.Error()})
		return
	}
	if stdin != nil {
		cmd.Stdin = stdin
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = c.send(frame{Type: frameDownEnd, XferID: f.XferID, ExitCode: -1, Error: err.Error()})
		return
	}
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		_ = c.send(frame{Type: frameDownEnd, XferID: f.XferID, ExitCode: -1, Error: err.Error()})
		return
	}

	buf := make([]byte, chunkPayloadSize)
	seq := 0
	for {
		n, rerr := stdoutPipe.Read(buf)
		if n > 0 {
			if serr := c.send(frame{
				Type:   frameDownChunk,
				XferID: f.XferID,
				Seq:    seq,
				B64:    base64.StdEncoding.EncodeToString(buf[:n]),
			}); serr != nil {
				_ = cmd.Process.Kill()
				return
			}
			seq++
		}
		if rerr != nil {
			break
		}
	}

	waitErr := cmd.Wait()
	end := frame{Type: frameDownEnd, XferID: f.XferID, ExitCode: 0}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			end.ExitCode = exitErr.ExitCode()
		} else {
			end.ExitCode = -1
		}
		end.Error = stderrBuf.String()
	}
	_ = c.send(end)
}

// run 是 Agent 客户端主循环：连接、读取帧、心跳、重连。
func run(cfg *Config) error {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	retryDelay := cfg.RetryBase

	for {
		log.Printf("connecting to %s ...", cfg.ServerURL)
		ws, err := connectWS(cfg.ServerURL, cfg.Token)
		if err != nil {
			log.Printf("connect failed: %v, retry in %v", err, retryDelay)
			select {
			case <-interrupt:
				return nil
			case <-time.After(retryDelay):
			}
			retryDelay *= 2
			if retryDelay > cfg.RetryMax {
				retryDelay = cfg.RetryMax
			}
			continue
		}
		retryDelay = cfg.RetryBase
		log.Printf("connected to %s (version=%s)", cfg.ServerURL, version)

		c := newConn(ws)

		// 服务器的 ping 由我们自己应答应用层 pong 帧；这里禁用 gorilla 的
		// 自动 pong，避免它在非写者 goroutine 里直接写 WS 破坏帧序。
		ws.SetPingHandler(func(string) error { return nil })

		// 上报版本与平台信息。
		_ = c.send(frame{Type: frameHello, Version: version, OS: runtime.GOOS, Arch: runtime.GOARCH})

		connDone := make(chan struct{})
		go func() {
			defer close(connDone)
			for {
				_, msg, rerr := ws.ReadMessage()
				if rerr != nil {
					if websocket.IsUnexpectedCloseError(rerr,
						websocket.CloseGoingAway, websocket.CloseAbnormalClosure,
						websocket.CloseNormalClosure, websocket.CloseNoStatusReceived) {
						log.Printf("websocket read error: %v", rerr)
					} else {
						log.Printf("websocket closed: %v", rerr)
					}
					return
				}
				var f frame
				if jerr := json.Unmarshal(msg, &f); jerr != nil {
					log.Printf("invalid frame: %v", jerr)
					continue
				}
				c.handle(f)
			}
		}()

		heartbeatTicker := time.NewTicker(cfg.Heartbeat)
		stopped := false
		for !stopped {
			select {
			case <-interrupt:
				log.Println("interrupt received, closing")
				c.close()
				stopped = true
			case <-connDone:
				log.Println("connection closed, reconnecting")
				c.close()
				stopped = true
			case <-heartbeatTicker.C:
				if serr := c.send(frame{Type: framePong}); serr != nil {
					c.close()
					stopped = true
				}
			}
		}
		heartbeatTicker.Stop()
		c.close()

		if stopped {
			select {
			case <-interrupt:
				return nil
			default:
			}
		}
		time.Sleep(retryDelay)
		retryDelay *= 2
		if retryDelay > cfg.RetryMax {
			retryDelay = cfg.RetryMax
		}
	}
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("1mate Agent starting (version=%s)", version)

	var verbose bool
	flag.BoolVar(&verbose, "v", false, "verbose logging")
	flag.Parse()

	if verbose {
		log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
	}

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	log.Printf("config loaded: server=%s host=%s heartbeat=%v", cfg.ServerURL, cfg.HostID, cfg.Heartbeat)

	if err := run(cfg); err != nil {
		log.Fatalf("agent exited: %v", err)
	}
}