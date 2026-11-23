# Agent 模式接入指南

1mate 默认通过 SSH 无代理管理远程主机。对于**没有公网地址、位于 NAT 或防火墙之后**的内网主机，可以使用 Agent 模式：在目标主机上运行一个轻量级 `1mate-agent` 客户端，由它主动向平台发起 WebSocket 长连接，平台再通过该连接下发运维命令。

## 一、架构与原理

```
┌──────────────┐         WebSocket（Agent 主动外连）        ┌──────────────────┐
│  目标主机     │  ──────────────────────────────────────▶  │  1mate 平台       │
│  1mate-agent │  ◀──────────────────────────────────────  │  AgentHub         │
│              │      命令下发 / 结果回传                     │  agentx.Client    │
└──────────────┘                                            └──────────────────┘
```

- **连接方向**：Agent 主动连接平台，目标主机无需开放入站端口、无需公网 IP。
- **统一接口**：Agent 模式下平台仍通过同一套 `sshx.Client` 接口操作主机（`internal/agentx` 实现了该接口），因此探测、部署、备份、镜像同步等业务逻辑零改动。
- **认证**：Agent 是独立进程，没有浏览器登录会话。接入时通过高熵随机的 `agent_token` 做机器认证；服务端用 token 反查主机，因此**客户端只需配置一个 token**。
- **命令执行**：平台下发 shell 命令，Agent 在本地 `sh -c` 执行并回传 stdout/stderr/退出码。

### 与 SSH 模式的区别

| 能力 | SSH 模式 | Agent 模式 |
|------|----------|------------|
| 连接方向 | 平台 → 主机（需主机可达 SSH 端口） | 主机 → 平台（主动外连） |
| 主机要求 | 开放 SSH、可配凭据 | 能访问平台 HTTP(S)、运行 Agent 二进制 |
| 认证方式 | SSH 私钥/密码 + 主机指纹 | agent_token（机器认证） |
| 凭据管理 | 平台加密存储 SSH 凭据 | 无需 SSH 凭据 |
| Web 终端 | ✅ 支持（PTY） | ⚠️ 暂不支持（交互式 PTY 未实现） |
| 探测/部署/备份/镜像同步/启停 | ✅ | ✅ |
| 大文件传输 | SSH 流式 | base64 编码（超大镜像建议先在本地拉取） |

## 二、平台侧配置

1. 在「主机」页面点击「添加主机」，连接方式选择 **Agent（内网主机主动连接）**。
2. 填写名称；Agent ID 与接入令牌可留空自动生成。
3. 保存后，平台会**一次性明文展示接入令牌**，请妥善保存（之后不再显示完整令牌，只能看到"已配置"状态）。
4. 在主机详情页「连接信息」Tab 可查看 Agent ID 与令牌配置状态。

## 三、目标主机侧安装

### 方式一：一键安装命令（推荐）

在主机详情页生成安装命令（API：`GET /api/v1/hosts/{id}/agent-install-command?server_base_url=http://<平台地址>:8080`），命令会：
- 写入 `/etc/1mate-agent/agent.env`（含 token 等配置，权限 0600）
- 写入 systemd 单元 `/etc/systemd/system/1mate-agent.service`
- 创建 `1mate` 系统用户与数据目录

然后手动将 `1mate-agent` 二进制放到 `/usr/local/bin/1mate-agent` 并赋可执行权限，最后：

```bash
systemctl daemon-reload
systemctl enable --now 1mate-agent
systemctl status 1mate-agent
```

### 方式二：手动配置

1. 从发布页下载对应架构的 `1mate-agent` 二进制，放到 `/usr/local/bin/1mate-agent` 并 `chmod +x`。
2. 创建 `/etc/1mate-agent/agent.env`：

```env
AGENT_SERVER_URL=wss://<平台域名或IP>/api/v1/agent/connect
AGENT_TOKEN=<平台生成的一次性令牌>
AGENT_HOST_ID=<可选，仅用于日志>
AGENT_INTERVAL=30
AGENT_RETRY_BASE=5
AGENT_RETRY_MAX=120
```

3. 使用 `deploy/agent.service` 作为 systemd 单元模板，启用服务：

```bash
install -d -m 0755 /etc/1mate-agent /var/lib/1mate-agent
cp deploy/agent.service /etc/systemd/system/1mate-agent.service
useradd -r -s /usr/sbin/nologin -d /var/lib/1mate-agent 1mate
chown -R 1mate:1mate /var/lib/1mate-agent
systemctl daemon-reload
systemctl enable --now 1mate-agent
```

> 若平台启用了 HTTPS，WebSocket 地址使用 `wss://`；HTTP 使用 `ws://`。

## 四、验证接入

1. 启动 Agent 后，查看日志确认连接成功：

```bash
journalctl -u 1mate-agent -f
# 正常示例：connected to wss://... / retrying in 5s ...
```

2. 回到平台主机列表，该主机状态应从"未知"变为可探测。点击「探测」，确认 OS/Docker/Compose 信息正常返回。
3. 若无响应，检查下方故障排查。

## 五、故障排查

| 现象 | 可能原因 | 处理 |
|------|----------|------|
| Agent 日志 `invalid agent_token` / 401 | token 不匹配或主机非 Agent 模式 | 确认平台主机连接类型为 Agent，重新复制 token |
| Agent 日志 `connection refused` | 平台地址不可达或协议错误 | 检查 `AGENT_SERVER_URL`（HTTP→`ws://`，HTTPS→`wss://`）及端口 |
| 平台主机一直"未知" | Agent 未连上 | `systemctl status 1mate-agent` 看是否运行；检查网络出站 80/443 |
| 探测返回空/超时 | Agent 未注册到 AgentHub | 重启 Agent；平台日志搜索 "agent connected" |
| 令牌泄露 | 明文存于 agent.env | 立即在平台轮换令牌（编辑主机重新生成），更新目标主机配置 |

## 六、安全说明

- `agent_token` 是高熵随机值（32 字节），等同于主机凭据；**不要提交到代码仓库或在公共渠道明文传输**。
- `agent.env` 权限应为 `0600`，仅 `1mate` 用户可读。
- Agent 以非 root 的 `1mate` 用户运行；需要 Docker 等特权操作时，由平台下发带 sudo 的命令（目标主机需为 `1mate` 用户配置相应 sudo 权限）。
- WebSocket 连接建议经 TLS（`wss://`）加密，避免令牌与命令明文传输。
- Agent 会将收到的命令在本地 `sh -c` 执行，因此**只有可信的主机才应接入**，且平台账号体系需做好访问控制。

## 七、限制与后续

完整的缺口清单（含编号、代码证据、影响与修复状态）见 **`docs/agent-mode-gaps.md`**。当前主要限制：

- Web 交互式终端（PTY）暂不支持 Agent 模式，如需交互式操作请改用 SSH 连接。
- 文件传输与长命令输出在 v1.1.0-rc.1 中采用单条命令内联传输，受 Linux 单参数长度上限（约 128KB）限制：**较大的备份恢复、镜像同步在 Agent 模式不可用**；修复计划见缺口 G2/G3。
- Agent 主机暂不支持 sudo 提权（G6），因此 Agent 进程需以 root（或 root 等价）身份运行才能安装 Docker 与编排容器。
- 24 小时长连接稳定性与并发压测见 Phase 6 测试记录。
