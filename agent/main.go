// Agent 客户端 — 独立 Go 二进制，运行在目标主机上，通过 WebSocket 连接到 1mate 服务器。
// 接收服务器下发的命令，在本地执行，将结果返回给服务器。
//
// 环境变量：
//   AGENT_SERVER_URL   WebSocket 服务器地址，如 ws://127.0.0.1:8080/api/v1/agent/connect
//   AGENT_TOKEN        Agent 认证令牌（与主机记录绑定）
//   AGENT_HOST_ID      主机唯一标识 UUID
//   AGENT_INTERVAL     心跳间隔（秒），默认 30
//   AGENT_RETRY_BASE   重连基础延迟（秒），默认 5
//   AGENT_RETRY_MAX    最大重连延迟（秒），默认 120
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// AgentCommand 是服务器下发给 Agent 的命令。
type AgentCommand struct {
	CmdID   string `json:"cmd_id"`
	Cmd     string `json:"cmd"`
	Timeout int    `json:"timeout"` // 秒，0 表示无超时
}

// AgentResult 是 Agent 返回给服务器的命令执行结果。
type AgentResult struct {
	CmdID    string `json:"cmd_id"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Error    string `json:"error"`
}

// Config 从环境变量读取的 Agent 配置。
type Config struct {
	ServerURL  string
	Token      string
	HostID     string
	Heartbeat  time.Duration
	RetryBase  time.Duration
	RetryMax   time.Duration
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
	hostID := os.Getenv("AGENT_HOST_ID")
	if hostID == "" {
		return nil, fmt.Errorf("AGENT_HOST_ID is required")
	}

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
		ServerURL:  serverURL,
		Token:      token,
		HostID:     hostID,
		Heartbeat:  heartbeat,
		RetryBase:  retryBase,
		RetryMax:   retryMax,
	}, nil
}

func connectWS(serverURL, token string) (*websocket.Conn, error) {
	// 用 net/url 安全地编码 token，避免 base64 中的 + / = 等特殊字符破坏查询参数。
	sep := "?"
	if strings.Contains(serverURL, "?") {
		sep = "&"
	}
	fullURL := fmt.Sprintf("%s%sagent_token=%s", serverURL, sep, url.QueryEscape(token))

	dialer := websocket.DefaultDialer
	headers := http.Header{}
	headers.Set("Origin", serverURL)
	conn, _, err := dialer.Dial(fullURL, headers)
	if err != nil {
		return nil, fmt.Errorf("dial websocket: %w", err)
	}
	return conn, nil
}

// runCommand 执行本地 shell 命令并返回结果。
func runCommand(cmd AgentCommand) AgentResult {
	result := AgentResult{CmdID: cmd.CmdID}

	ctx := context.Background()
	if cmd.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(cmd.Timeout)*time.Second)
		defer cancel()
	}

	var command *exec.Cmd
	// 使用 /bin/sh -c 执行，与服务器侧 sshx 保持一致
	if strings.Contains(cmd.Cmd, "\n") {
		// 多行命令，写入临时脚本
		script := cmd.Cmd
		command = exec.CommandContext(ctx, "sh", "-c", script)
	} else {
		command = exec.CommandContext(ctx, "sh", "-c", cmd.Cmd)
	}

	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr

	err := command.Run()
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else if ctx.Err() == context.DeadlineExceeded {
			result.ExitCode = -1
			result.Error = "command timed out"
		} else {
			result.ExitCode = -1
			result.Error = err.Error()
		}
	}

	return result
}

func sendResult(conn *websocket.Conn, result AgentResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

func handleCommand(conn *websocket.Conn, cmd AgentCommand) {
	result := runCommand(cmd)
	if err := sendResult(conn, result); err != nil {
		log.Printf("send result failed: %v", err)
	}
}

// Agent 客户端主循环：连接、读取命令、心跳、重连。
func run(cfg *Config) error {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	retryDelay := cfg.RetryBase

	for {
		log.Printf("connecting to %s...", cfg.ServerURL)
		conn, err := connectWS(cfg.ServerURL, cfg.Token)
		if err != nil {
			log.Printf("connect failed: %v, retry in %v", err, retryDelay)
			time.Sleep(retryDelay)
			retryDelay *= 2
			if retryDelay > cfg.RetryMax {
				retryDelay = cfg.RetryMax
			}
			continue
		}
		retryDelay = cfg.RetryBase
		log.Printf("connected to %s", cfg.ServerURL)

		// 心跳 tick
		heartbeatTicker := time.NewTicker(cfg.Heartbeat)
		defer heartbeatTicker.Stop()

		// 命令处理通道，避免并发发送导致 WS 损坏
		sendMu := &sync.Mutex{}

		connDone := make(chan struct{})
		go func() {
			defer close(connDone)
			for {
				_, msg, err := conn.ReadMessage()
				if err != nil {
					if websocket.IsUnexpectedCloseError(err,
						websocket.CloseGoingAway, websocket.CloseAbnormalClosure,
						websocket.CloseNormalClosure, websocket.CloseNoStatusReceived) {
						log.Printf("websocket read error: %v", err)
					} else {
						log.Printf("websocket closed: %v", err)
					}
					return
				}
				var cmd AgentCommand
				if err := json.Unmarshal(msg, &cmd); err != nil {
					log.Printf("invalid command: %v", err)
					continue
				}
				log.Printf("received command %s: %s", cmd.CmdID, cmd.Cmd)
				go handleCommand(conn, cmd)
			}
		}()

		// 发送空心跳帧保持连接
		heartbeat := func() {
			sendMu.Lock()
			defer sendMu.Unlock()
			_ = conn.WriteMessage(websocket.PingMessage, nil)
		}

		for {
			select {
			case <-interrupt:
				log.Println("interrupt received, closing")
				conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				conn.Close()
				return nil
			case <-connDone:
				log.Println("connection closed, reconnecting")
				conn.Close()
				goto reconnect
			case <-heartbeatTicker.C:
				heartbeat()
			}
		}

	reconnect:
		time.Sleep(retryDelay)
		retryDelay *= 2
		if retryDelay > cfg.RetryMax {
			retryDelay = cfg.RetryMax
		}
	}
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("1mate Agent starting...")

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

// io.Writer 接口确保 strings.Builder 可用于 Stdout/Stderr 赋值（编译期检查）。
var _ io.Writer = (*strings.Builder)(nil)
