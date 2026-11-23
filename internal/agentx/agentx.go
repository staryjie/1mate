// Package agentx 实现 sshx.Client 接口，底层通过 Agent WebSocket 长连接
// 与目标主机通信。Agent 作为客户端主动连接 1mate 服务器，agentx 充当
// 服务器侧的代理客户端，将 sshx 调用转换为 Agent 帧。
//
// 设计要点：
// - 避免循环依赖：agentx 只定义 AgentCommander / StreamCommander 接口，由
//   internal/service 的 AgentHub 实现；service 导入 agentx，agentx 不导入 service。
// - StreamCommander 是可选增强接口：支持流式输出（G4）与分块文件传输（G2/G3）。
//   当底层实现不支持时自动退化为「整包 base64 走命令」的老路径，保证测试用
//   的假实现（sshx.Fake / 测试桩）无需改动即可继续工作。
// - PtyStart 暂未实现，返回错误；Agent 模式下的 Web 终端需 WS 会话管理。
package agentx

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"1mate/internal/sshx"
)

// ErrAgentOffline 表示 Agent 未连接，无法执行操作。
var ErrAgentOffline = errors.New("agent_offline")

// ErrAgentPTYNotImplemented 表示 Agent 通道暂不支持 PTY。
var ErrAgentPTYNotImplemented = errors.New("PTY over Agent not yet implemented")

// AgentCommander 是向 Agent 下发命令的最小接口。由 internal/service.AgentHub
// 通过适配器实现，避免 agentx 导入 service 造成循环依赖。
type AgentCommander interface {
	// SendCommand 向指定 Agent 下发命令并等待结果。超时为 0 表示使用默认超时。
	SendCommand(ctx context.Context, agentID string, cmd string, timeoutSeconds int) (sshx.Result, error)
	// IsConnected 报告指定 Agent 是否当前在线。
	IsConnected(agentID string) bool
}

// RunOptions 是 agentx 的执行选项：在 sshx.RunOptions 基础上把 sudo 密码与
// stdin 显式传给 Agent（走专用帧字段，不拼进命令行）。
type RunOptions struct {
	Timeout      time.Duration
	Stdin        string
	SudoPassword string
}

// StreamCommander 是 AgentCommander 的可选增强接口。实现它即可获得流式输出
// 与不受命令长度限制的分块文件传输。
type StreamCommander interface {
	// RunStream 执行命令并把增量输出回调给 onOutput(stream, text)。
	RunStream(ctx context.Context, agentID, cmd string, opts RunOptions, onOutput func(stream, text string)) (sshx.Result, error)
	// UploadFile 通过分块协议把 r 写入 path。
	UploadFile(ctx context.Context, agentID, path, mode string, r io.Reader) error
	// DownloadCommand 执行 cmd 并把 stdout 流式写入 w。
	DownloadCommand(ctx context.Context, agentID, cmd string, w io.Writer) (int64, error)
}

// Client 实现 sshx.Client，底层通过 Agent WebSocket 长连接通信。
type Client struct {
	agentID   string
	commander AgentCommander
}

// New 为指定 Agent ID 创建 Client 实例。
func New(agentID string, commander AgentCommander) *Client {
	return &Client{agentID: agentID, commander: commander}
}

// HostKeyFingerprint 返回基于 Agent ID 的指纹（非 SSH 主机密钥）。
func (c *Client) HostKeyFingerprint() string {
	return "agent:" + c.agentID
}

// streamer 返回底层是否支持增强协议。
func (c *Client) streamer() (StreamCommander, bool) {
	sc, ok := c.commander.(StreamCommander)
	return sc, ok
}

// runOptions 把 sshx.RunOptions 转换为 agentx.RunOptions。
func runOptions(opts sshx.RunOptions) RunOptions {
	ro := RunOptions{Timeout: opts.Timeout, Stdin: opts.StdinText}
	if opts.Sudo.NeedsPassword() {
		ro.SudoPassword = opts.Sudo.Password
	}
	return ro
}

// applyPrefix 处理不需要密码的提权前缀（sudo -n）。需要密码时前缀留空，
// 由 Agent 侧用 `sudo -S` 并把密码写入 stdin。
func applyPrefix(cmd string, opts sshx.RunOptions, ro RunOptions) string {
	if ro.SudoPassword != "" {
		return cmd
	}
	if p := opts.Sudo.Prefix(); p != "" {
		return p + cmd
	}
	return cmd
}

// execute 是统一执行入口：优先走增强协议（含流式输出），否则退化为整包命令。
func (c *Client) execute(ctx context.Context, cmd string, opts sshx.RunOptions, onOutput func(stream, text string)) (sshx.Result, error) {
	if !c.commander.IsConnected(c.agentID) {
		return sshx.Result{}, ErrAgentOffline
	}
	ro := runOptions(opts)
	fullCmd := applyPrefix(cmd, opts, ro)

	if sc, ok := c.streamer(); ok {
		return sc.RunStream(ctx, c.agentID, fullCmd, ro, onOutput)
	}

	res, err := c.commander.SendCommand(ctx, c.agentID, fullCmd, int(ro.Timeout.Seconds()))
	if err != nil {
		return res, err
	}
	if onOutput != nil {
		if res.Stdout != "" {
			onOutput("stdout", res.Stdout)
		}
		if res.Stderr != "" {
			onOutput("stderr", res.Stderr)
		}
	}
	return res, nil
}

// Run 在 Agent 主机上执行命令。
func (c *Client) Run(ctx context.Context, cmd string, opts sshx.RunOptions) (sshx.Result, error) {
	return c.execute(ctx, cmd, opts, nil)
}

// RunStreaming 执行命令并通过 sink 流式输出结果。执行期间产生的 stdout/stderr
// 增量会立即回调 sink，而不是等命令结束后一次性返回（G4）。
func (c *Client) RunStreaming(ctx context.Context, cmd string, opts sshx.RunOptions, sink func(string)) (sshx.Result, error) {
	var onOutput func(stream, text string)
	if sink != nil {
		onOutput = func(_, text string) {
			if text != "" {
				sink(text)
			}
		}
	}
	return c.execute(ctx, cmd, opts, onOutput)
}

// quoteShell 使用单引号安全地引用 shell 参数。
func quoteShell(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// Upload 将内容写入 Agent 主机的路径。增强协议下走分块上传（无大小限制、
// 原子写入 tmp+mv）；退化路径下按整包 base64 拼进命令。
func (c *Client) Upload(ctx context.Context, path, content, mode string) error {
	return c.UploadReader(ctx, path, strings.NewReader(content), mode, sshx.RunOptions{})
}

// UploadReader 将 r 写入 path。增强协议下内容边读边发（G2），不会整体驻留内存。
func (c *Client) UploadReader(ctx context.Context, path string, r io.Reader, mode string, opts sshx.RunOptions) error {
	if !c.commander.IsConnected(c.agentID) {
		return ErrAgentOffline
	}
	if sc, ok := c.streamer(); ok {
		return sc.UploadFile(ctx, c.agentID, path, mode, r)
	}

	// 退化路径：整包 base64 走单条命令（仅适合小文件）。
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	qPath := quoteShell(path)
	cmd := fmt.Sprintf("mkdir -p -- $(dirname %s) && echo '%s' | base64 -d > %s.1mate-tmp && chmod %s %s.1mate-tmp && mv -f %s.1mate-tmp %s",
		qPath, encoded, qPath, mode, qPath, qPath, qPath)
	res, err := c.Run(ctx, cmd, opts)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("upload failed: %s", res.Stderr)
	}
	return nil
}

// Download 将远程文件内容写入 w，返回写入字节数。
func (c *Client) Download(ctx context.Context, path string, w io.Writer) (int64, error) {
	if !c.commander.IsConnected(c.agentID) {
		return 0, ErrAgentOffline
	}
	if sc, ok := c.streamer(); ok {
		return sc.DownloadCommand(ctx, c.agentID, fmt.Sprintf("cat -- %s", quoteShell(path)), w)
	}
	res, err := c.Run(ctx, fmt.Sprintf("cat -- %s", quoteShell(path)), sshx.RunOptions{})
	if err != nil {
		return 0, err
	}
	if res.ExitCode != 0 {
		return 0, fmt.Errorf("download failed: %s", res.Stderr)
	}
	n, werr := w.Write([]byte(res.Stdout))
	return int64(n), werr
}

// DownloadWithCommand 将命令的 stdout 流式写入 w，非零退出码返回错误。
// 常用于 pg_dump 流式备份：增强协议下数据边产生边写入本地文件，不在内存中
// 累积（G2/G3）。
func (c *Client) DownloadWithCommand(ctx context.Context, cmd string, opts sshx.RunOptions, w io.Writer) (int64, error) {
	if !c.commander.IsConnected(c.agentID) {
		return 0, ErrAgentOffline
	}
	ro := runOptions(opts)
	fullCmd := applyPrefix(cmd, opts, ro)
	if sc, ok := c.streamer(); ok {
		n, err := sc.DownloadCommand(ctx, c.agentID, fullCmd, w)
		if err != nil {
			return n, fmt.Errorf("command failed: %w", err)
		}
		return n, nil
	}
	res, err := c.Run(ctx, fullCmd, opts)
	if err != nil {
		return 0, err
	}
	if res.ExitCode != 0 {
		return 0, fmt.Errorf("command failed with exit %d: %s", res.ExitCode, res.Stderr)
	}
	n, werr := w.Write([]byte(res.Stdout))
	return int64(n), werr
}

// RunWithStdin 将 r 的内容作为 stdin 输入给 cmd 执行。增强协议下 stdin 走
// 专用帧字段（G6），退化路径下用 base64 管道。
func (c *Client) RunWithStdin(ctx context.Context, cmd string, r io.Reader, opts sshx.RunOptions) error {
	if !c.commander.IsConnected(c.agentID) {
		return ErrAgentOffline
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	opts.StdinText = string(data)

	if _, ok := c.streamer(); ok {
		res, err := c.execute(ctx, cmd, opts, nil)
		if err != nil {
			return err
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("stdin command failed with exit %d: %s", res.ExitCode, res.Stderr)
		}
		return nil
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	qCmd := quoteShell(cmd)
	fullCmd := fmt.Sprintf("printf '%%s' '%s' | base64 -d | %s", encoded, qCmd)
	res, err := c.Run(ctx, fullCmd, opts)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("stdin command failed: %s", res.Stderr)
	}
	return nil
}

// PtyStart 在 Agent 主机上启动交互式 PTY 会话。
// 暂未实现：PTY over WebSocket 需要会话管理和二进制帧支持，留作后续阶段。
func (c *Client) PtyStart(ctx context.Context, term string, rows, cols int, opts sshx.RunOptions) (sshx.PtySession, error) {
	if !c.commander.IsConnected(c.agentID) {
		return nil, ErrAgentOffline
	}
	return nil, ErrAgentPTYNotImplemented
}

// Close 关闭 Agent 客户端（WS-based 客户端无需释放资源，空实现）。
func (c *Client) Close() error {
	return nil
}

// 编译期接口检查。
var _ sshx.Client = (*Client)(nil)