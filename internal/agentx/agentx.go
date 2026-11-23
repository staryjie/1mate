// Package agentx 实现 sshx.Client 接口，底层通过 Agent WebSocket 长连接
// 与目标主机通信。Agent 作为客户端主动连接 1mate 服务器，agentx 充当
// 服务器侧的代理客户端，将 sshx 调用转换为 Agent 命令。
//
// 设计要点：
// - 避免循环依赖：agentx 只定义 AgentCommander 接口，由 internal/service
//   的 AgentHub 实现；service 导入 agentx，agentx 不导入 service。
// - 所有方法均通过 SendCommand 向 Agent 下发 shell 命令，Agent 在本地
//   执行并通过 WebSocket 返回结果。
// - Upload/Download 等文件操作通过 base64 编码在命令中传输，适合小文件；
//   大文件（如 Docker 镜像）需要 WebSocket 二进制帧支持，留作后续阶段。
// - PtyStart 暂未实现，返回错误；Web 终端在 Agent 模式下的支持需 WS
//   会话管理，留作后续阶段。
package agentx

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"1mate/internal/sshx"
)

// ErrAgentOffline 表示 Agent 未连接，无法执行操作。
var ErrAgentOffline = errors.New("agent_offline")

// ErrAgentPTYNotImplemented 表示 Agent 通道暂不支持 PTY。
var ErrAgentPTYNotImplemented = errors.New("PTY over Agent not yet implemented")

// AgentCommander 是向 Agent 下发命令的接口。由 internal/service.AgentHub
// 通过适配器实现，避免 agentx 导入 service 造成循环依赖。
type AgentCommander interface {
	// SendCommand 向指定 Agent 下发命令并等待结果。超时为 0 表示无超时。
	SendCommand(ctx context.Context, agentID string, cmd string, timeoutSeconds int) (sshx.Result, error)
	// IsConnected 报告指定 Agent 是否当前在线。
	IsConnected(agentID string) bool
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

// Run 在 Agent 主机上执行命令。
func (c *Client) Run(ctx context.Context, cmd string, opts sshx.RunOptions) (sshx.Result, error) {
	if !c.commander.IsConnected(c.agentID) {
		return sshx.Result{}, ErrAgentOffline
	}
	fullCmd := cmd
	if prefix := opts.Sudo.Prefix(); prefix != "" {
		fullCmd = prefix + cmd
	}
	return c.commander.SendCommand(ctx, c.agentID, fullCmd, int(opts.Timeout.Seconds()))
}

// RunStreaming 执行命令并通过 sink 流式输出结果。
func (c *Client) RunStreaming(ctx context.Context, cmd string, opts sshx.RunOptions, sink func(string)) (sshx.Result, error) {
	res, err := c.Run(ctx, cmd, opts)
	if err != nil {
		return res, err
	}
	if sink != nil {
		if res.Stdout != "" {
			sink(res.Stdout)
		}
		if res.Stderr != "" {
			sink(res.Stderr)
		}
	}
	return res, nil
}

// quoteShell 使用单引号安全地引用 shell 参数。
func quoteShell(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// Upload 将内容写入 Agent 主机的路径（原子写入：tmp + mv）。
// 内容通过 base64 编码传输，避免 shell 注入。
func (c *Client) Upload(ctx context.Context, path, content, mode string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	qPath := quoteShell(path)
	cmd := fmt.Sprintf("mkdir -p -- $(dirname %s) && echo '%s' | base64 -d > %s.1mate-tmp && chmod %s %s.1mate-tmp && mv -f %s.1mate-tmp %s",
		qPath, encoded, qPath, mode, qPath, qPath, qPath)
	res, err := c.Run(ctx, cmd, sshx.RunOptions{})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("upload failed: %s", res.Stderr)
	}
	return nil
}

// UploadReader 将 r 流式写入 path，不先缓冲到内存（这里为兼容命令通道，
// 实际会先读取全部内容再上传；大文件请使用 WS 二进制帧）。
func (c *Client) UploadReader(ctx context.Context, path string, r io.Reader, mode string, opts sshx.RunOptions) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return c.Upload(ctx, path, string(data), mode)
}

// Download 将远程文件内容写入 w，返回写入字节数。
func (c *Client) Download(ctx context.Context, path string, w io.Writer) (int64, error) {
	qPath := quoteShell(path)
	cmd := fmt.Sprintf("cat -- %s", qPath)
	res, err := c.Run(ctx, cmd, sshx.RunOptions{})
	if err != nil {
		return 0, err
	}
	if res.ExitCode != 0 {
		return 0, fmt.Errorf("download failed: %s", res.Stderr)
	}
	n, err := w.Write([]byte(res.Stdout))
	return int64(n), err
}

// DownloadWithCommand 将命令的 stdout 流式写入 w，非零退出码返回错误。
// 常用于 pg_dump 流式备份。
func (c *Client) DownloadWithCommand(ctx context.Context, cmd string, opts sshx.RunOptions, w io.Writer) (int64, error) {
	res, err := c.Run(ctx, cmd, opts)
	if err != nil {
		return 0, err
	}
	if res.ExitCode != 0 {
		return 0, fmt.Errorf("command failed with exit %d: %s", res.ExitCode, res.Stderr)
	}
	n, err := w.Write([]byte(res.Stdout))
	return int64(n), err
}

// RunWithStdin 将 r 的内容作为 stdin 输入给 cmd 执行。
// 内容通过 base64 编码通过管道传入。
func (c *Client) RunWithStdin(ctx context.Context, cmd string, r io.Reader, opts sshx.RunOptions) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	qCmd := quoteShell(cmd)
	fullCmd := fmt.Sprintf("echo '%s' | base64 -d | %s", encoded, qCmd)
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
