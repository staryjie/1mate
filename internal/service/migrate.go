package service

import (
	"fmt"
	"strings"

	"1mate/internal/model"
	"1mate/internal/store"
)

// AgentInstallCommand 生成在目标主机上执行的一键 Agent 安装命令。
// 命令会下载 agent 二进制、写入配置与 systemd 单元、启动服务。
//
// serverBaseURL 是平台 HTTP 地址（如 http://192.168.31.7:8080），
// 用于拼接 WS 端点与二进制下载地址；token 是主机的 agent_token。
func (s *Service) AgentInstallCommand(hostID, serverBaseURL string) (string, error) {
	h, err := s.Store.GetHost(hostID)
	if err != nil {
		return "", err
	}
	if h.ConnectionType != model.ConnTypeAgent {
		return "", fmt.Errorf("%w: host is not in Agent mode", ErrValidation)
	}
	if h.AgentToken == "" {
		return "", fmt.Errorf("%w: host has no agent token", ErrValidation)
	}

	base := strings.TrimRight(serverBaseURL, "/")
	if base == "" {
		base = "http://127.0.0.1:8080"
	}
	// WS 端点：把 http(s) 换成 ws(s)。
	wsURL := base
	switch {
	case strings.HasPrefix(wsURL, "https://"):
		wsURL = "wss://" + strings.TrimPrefix(wsURL, "https://")
	case strings.HasPrefix(wsURL, "http://"):
		wsURL = "ws://" + strings.TrimPrefix(wsURL, "http://")
	}
	wsURL += "/api/v1/agent/connect"

	// 注意：token 里可能含 + / =，这里作为环境变量内容写入文件（非 URL），
	// 不需要转义；客户端 connectWS 内部会做 QueryEscape。
	script := fmt.Sprintf(`set -e
# 1mate Agent 一键安装脚本（由平台生成）
install -d -m 0755 /etc/1mate-agent /var/lib/1mate-agent
cat > /etc/1mate-agent/agent.env <<'AGENT_ENV'
AGENT_SERVER_URL=%s
AGENT_TOKEN=%s
AGENT_HOST_ID=%s
AGENT_INTERVAL=5s
AGENT_RETRY_BASE=5s
AGENT_RETRY_MAX=120s
AGENT_ENV
chmod 0600 /etc/1mate-agent/agent.env
cat > /etc/systemd/system/1mate-agent.service <<'AGENT_UNIT'
[Unit]
Description=1mate Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=1mate
Group=1mate
WorkingDirectory=/var/lib/1mate-agent
ExecStart=/usr/local/bin/1mate-agent
Restart=always
RestartSec=5
EnvironmentFile=-/etc/1mate-agent/agent.env
ProtectSystem=strict
ReadWritePaths=/var/lib/1mate-agent
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
AGENT_UNIT
id -u 1mate >/dev/null 2>&1 || useradd -r -s /usr/sbin/nologin -d /var/lib/1mate-agent 1mate
chown -R 1mate:1mate /var/lib/1mate-agent
echo '请将 1mate-agent 二进制放到 /usr/local/bin/1mate-agent 并 chmod +x'
echo '然后执行: systemctl daemon-reload && systemctl enable --now 1mate-agent'
`, wsURL, h.AgentToken, h.AgentID)

	return script, nil
}

// AgentConnectionInfo 返回主机的 Agent 接入信息（供前端展示/复制）。
type AgentConnectionInfo struct {
	AgentID    string `json:"agent_id"`
	TokenSet   bool   `json:"token_set"`
	ServerHint string `json:"server_hint,omitempty"`
}

// AgentInfo 返回指定主机的 Agent 接入摘要。
func (s *Service) AgentInfo(hostID string) (*AgentConnectionInfo, error) {
	h, err := s.Store.GetHost(hostID)
	if err != nil {
		return nil, err
	}
	if h.ConnectionType != model.ConnTypeAgent {
		return nil, fmt.Errorf("%w: host is not in Agent mode", ErrValidation)
	}
	return &AgentConnectionInfo{
		AgentID:  h.AgentID,
		TokenSet: h.AgentToken != "",
	}, nil
}

// FindHostByToken 按 agent_token 反查主机（供认证与迁移工具复用）。
func (s *Service) FindHostByToken(token string) (*model.Host, error) {
	h, err := s.Store.FindHostByAgentToken(token)
	if err != nil {
		if err == store.ErrNotFound {
			return nil, ErrAgentNotConnected
		}
		return nil, err
	}
	return h, nil
}
