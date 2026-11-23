# 部署文档

本文档描述如何将 1mate 平台部署到生产环境。涵盖三种部署方式（systemd 二进制 / Docker / Docker Compose）、构建流程、密钥管理、反向代理与升级流程。

> 日常运维与故障排查请看 `docs/runbook.md`；接口定义见 `docs/openapi.yaml`。

---

## 1. 部署架构

```
浏览器 ──HTTPS──▶ 反向代理（Nginx/Caddy/Traefik，可选但推荐）
                        │
                        ▼ 127.0.0.1:8080
                  ┌──────────────┐
                  │    1mate     │  单二进制（前端已内嵌）
                  │  SQLite WAL  │  /var/lib/1mate/1mate.db
                  │  master.key  │  /var/lib/1mate/master.key
                  │  备份文件     │  /var/lib/1mate/backups/
                  └──┬────────┬──┘
                     │        │ WebSocket（Agent 主动外连）
                     │ SSH    │
     ┌───────────────┼──┐     └──▶ ┌──────────────────┐
     ▼               ▼  ▼          │ 内网主机（无公网）  │
Linux 主机 A    Linux 主机 B        │ 1mate-agent 客户端 │
(SSH 直连)      (SSH 直连)          │ Docker+Compose     │
                                   └──────────────────┘
```

平台自身为**无状态控制面**（数据都在自己的数据目录），支持两种主机接入方式：

- **SSH 模式（默认）**：平台直连目标主机 SSH，目标主机只需 Docker + Compose，不安装任何 1mate 组件。
- **Agent 模式**：目标主机运行轻量 `1mate-agent` 客户端主动外连平台（WebSocket），适用于无公网 IP / NAT 后的内网主机；详见第 12 节与 `docs/agent-mode.md`。

---

## 2. 构建产物

### 2.1 前置要求

| 构建/运行 | 要求 |
|---|---|
| 构建机 | Go ≥ 1.26、Node.js ≥ 22（或使用 Docker 构建，无需本地工具链） |
| 平台运行机 | Linux amd64/arm64，无需 Go/Node，只需一个二进制 + 数据目录 |
| 目标主机（SSH 模式） | Linux amd64/arm64，SSH 可达；armv7 不支持（TeslaMate 官方镜像已放弃） |
| 目标主机（Agent 模式） | Linux amd64/arm64，能访问平台的 HTTP(S) 端口，运行 1mate-agent；无需 SSH / 公网 IP |

### 2.2 本地构建

```sh
make build        # = make web + make backend
# 产物：bin/1mate（前端已通过 go:embed 内嵌，单文件可分发）
```

版本号自动注入：`git describe --tags --always --dirty` → `internal/version`，健康检查 `/api/v1/health` 会返回真实版本而非 `dev`。手工指定：

```sh
make backend VERSION=v1.0.0 COMMIT=$(git rev-parse --short HEAD)
```

### 2.3 交叉编译（在 Windows/macOS 上构建 Linux 产物）

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -trimpath -ldflags "-s -w -X 1mate/internal/version.Version=v1.0.0" \
  -o bin/1mate-linux ./cmd/1mate
# arm64：GOARCH=arm64
```

> Windows PowerShell 下注意 `set` 写法（推荐用 `cmd /c "set CGO_ENABLED=0&&set GOOS=linux&&..."`），
> 否则环境变量不生效会产出本机平台的二进制。

### 2.4 Docker 构建

```sh
docker build -f deploy/Dockerfile -t 1mate:v1.0.0 \
  --build-arg VERSION=v1.0.0 --build-arg COMMIT=$(git rev-parse --short HEAD) .
# 多阶段：node:22 构建前端 → golang 静态编译 → distroless 运行镜像
```

### 2.5 构建 Agent 客户端（Agent 模式需要）

Agent 是**独立的 Go module**（`agent/go.mod`），与平台二进制分开构建：

```sh
cd agent
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -trimpath -ldflags "-s -w" -o 1mate-agent-linux .
# arm64：GOARCH=arm64
```

> 发布包中已提供 `1mate-agent-vX.Y.Z-linux-{amd64,arm64}.tar.gz`，生产环境建议直接下载发布包，无需自行构建。
> Windows PowerShell 下交叉编译请用 `cmd /c "set CGO_ENABLED=0&&set GOOS=linux&&..."` 写法。

---

## 3. 方式一：systemd 二进制部署（推荐）

### 3.1 创建系统用户与目录

```sh
sudo useradd --system --home-dir /var/lib/1mate --shell /usr/sbin/nologin 1mate
sudo mkdir -p /var/lib/1mate/backups /etc/1mate
sudo chown -R 1mate:1mate /var/lib/1mate
```

### 3.2 生成主密钥

主密钥用于加密所有主机 SSH 凭据与实例 secrets（AES-256-GCM）。**丢失 = 全部凭据不可解密**。

```sh
# 生成 32 字节 base64 密钥（任选其一）
openssl rand -base64 32
```

### 3.3 写入配置文件

```sh
sudo tee /etc/1mate/1mate.env >/dev/null <<'EOF'
ONEMATE_ADDR=127.0.0.1:8080
ONEMATE_DATA_DIR=/var/lib/1mate
ONEMATE_MASTER_KEY=<上一步生成的 base64 密钥>
ONEMATE_SESSION_TTL=168h
ONEMATE_SESSION_IDLE=12h
ONEMATE_WORKERS=4
ONEMATE_SECURE_COOKIE=false
EOF
sudo chown 1mate:1mate /etc/1mate/1mate.env
sudo chmod 640 /etc/1mate/1mate.env
```

| 变量 | 默认 | 说明 |
|---|---|---|
| `ONEMATE_ADDR` | `127.0.0.1:8080` | 监听地址；非回环地址且未开 TLS 时启动会告警 |
| `ONEMATE_DATA_DIR` | `./data` | SQLite、任务日志、备份、主密钥文件所在目录 |
| `ONEMATE_MASTER_KEY` | 自动生成文件 | base64 32B；**设置后优先于密钥文件**，且必须长期稳定 |
| `ONEMATE_SESSION_TTL` | `168h` | 会话绝对有效期 |
| `ONEMATE_SESSION_IDLE` | `12h` | 会话空闲有效期 |
| `ONEMATE_SECURE_COOKIE` | `false` | 走 HTTPS 反代时置 `true`（Cookie 加 Secure） |
| `ONEMATE_WORKERS` | `4` | 任务队列并发上限 |

> **主密钥策略**：建议通过 `ONEMATE_MASTER_KEY` 显式指定并离线备份；若不指定，
> 平台会在数据目录生成 `master.key`（0600）。两种方式不要混用切换，否则旧数据无法解密。

### 3.4 安装二进制与 systemd unit

```sh
sudo cp bin/1mate-linux /usr/local/bin/1mate
sudo chmod 0755 /usr/local/bin/1mate
sudo cp deploy/1mate.service /etc/systemd/system/1mate.service
sudo systemctl daemon-reload
sudo systemctl enable --now 1mate
```

unit 自带加固（`ProtectSystem=strict`、`NoNewPrivileges`、`ReadWritePaths=/var/lib/1mate` 等），无需修改即可使用。

### 3.5 验证

```sh
systemctl is-active 1mate                       # active
curl -fsS http://127.0.0.1:8080/api/v1/health   # {"status":"ok",...,"version":"v1.0.0"}
```

浏览器打开 `http://<主机>:<端口>`（或经反代域名），首次访问引导创建管理员账户。

---

## 4. 方式二：Docker 部署

```sh
docker run -d --name 1mate \
  --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -v 1mate-data:/data \
  -e ONEMATE_ADDR=0.0.0.0:8080 \
  -e ONEMATE_DATA_DIR=/data \
  -e ONEMATE_MASTER_KEY=<base64 32B> \
  -e ONEMATE_SECURE_COOKIE=false \
  1mate:v1.0.0
```

镜像基于 distroless、以 nonroot 运行；数据持久化在 `1mate-data` 卷（SQLite、密钥、备份）。

## 5. 方式三：Docker Compose

`deploy/docker-compose.yml` 已就绪（构建上下文指向项目根目录）：

```sh
docker compose -f deploy/docker-compose.yml up -d --build
```

生产使用建议把 `environment` 中的敏感项改为 `env_file` 引用并限制权限：

```yaml
    env_file:
      - /etc/1mate/1mate.env
```

---

## 6. 反向代理与 TLS（推荐）

平台与 TeslaMate/Grafana 均不建议直接暴露公网。以 Nginx 为例：

```nginx
server {
    listen 443 ssl;
    server_name 1mate.example.com;

    ssl_certificate     /etc/letsencrypt/live/1mate.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/1mate.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;

        # SSE 必需：任务日志实时流走 /api/v1/jobs/{id}/events
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_read_timeout 3600s;
        proxy_buffering off;
    }
}
```

开启反代后同步修改：

1. `/etc/1mate/1mate.env` → `ONEMATE_SECURE_COOKIE=true`，`ONEMATE_ADDR` 保持 `127.0.0.1:8080`；
2. `sudo systemctl restart 1mate`。

> **SSE 注意**：任务日志为 Server-Sent Events，代理必须关闭缓冲（`proxy_buffering off`）并放宽读超时，否则日志会"卡住"。

TeslaMate 实例同理：创建实例时 `bind_address` 填 `127.0.0.1`，由反代转发 TeslaMate `:4000` 与 Grafana `:3000`（子路径 `/grafana`），并自行加访问控制。

---

## 7. 目标主机准备（被管理的主机）

平台对目标主机的要求（添加主机前核对）：

| 项目 | 要求 |
|---|---|
| 系统 | Linux amd64/arm64；自动安装支持 Ubuntu 22.04/24.04/24.10/26.04、Debian 12/13、RHEL 9 系、Fedora 41+ |
| SSH | 端口 22（或自定义），私钥或密码认证 |
| Docker | 可缺失——平台提供一键安装（幂等脚本，始终优先 Compose v2，绝不安装/卸载 v1） |
| sudo | 非 root 用户需 sudo（免密或密码）；密码经 stdin 传递，不落日志 |
| 网络 | 国内主机建议安装时选择阿里云镜像源，Docker 拉取镜像可配置代理 |

主机录入后平台自动执行能力探测（`host_probe` 任务），能力面板展示发行版 / Docker / Compose 版本 / 端口占用等。

**Docker 镜像拉取代理**（目标主机无法直连 Docker Hub 时）：

```sh
sudo mkdir -p /etc/systemd/system/docker.service.d
sudo tee /etc/systemd/system/docker.service.d/http-proxy.conf >/dev/null <<'EOF'
[Service]
Environment="HTTP_PROXY=http://<代理地址>:<端口>"
Environment="HTTPS_PROXY=http://<代理地址>:<端口>"
Environment="NO_PROXY=127.0.0.1,localhost"
EOF
sudo systemctl daemon-reload && sudo systemctl restart docker
```

---

## 8. 升级平台

### 8.1 systemd 方式

```sh
# 构建或下载新二进制后
sudo systemctl stop 1mate
sudo cp bin/1mate-linux /usr/local/bin/1mate
sudo chmod 0755 /usr/local/bin/1mate
sudo systemctl start 1mate
curl -fsS http://127.0.0.1:8080/api/v1/health   # 确认 version 已更新
```

平台重启期间：
- 运行中的任务会被标记 `interrupted`（可在任务中心确认）；
- 已部署的 TeslaMate 实例**不受影响**（跑在目标主机上，与平台无关）。

### 8.2 Docker 方式

```sh
docker pull 1mate:v1.0.1
docker stop 1mate && docker rm 1mate
docker run ... 1mate:v1.0.1    # 参数同首次部署，数据卷复用
```

### 8.3 升级前备份（强烈建议）

```sh
sqlite3 /var/lib/1mate/1mate.db ".backup '/backup/1mate-$(date +%F).db.bak'"
cp /var/lib/1mate/master.key /backup/master.key.bak   # 与库分开存放
```

---

## 9. 备份与灾难恢复

### 9.1 需要备份的内容

| 内容 | 位置 | 重要性 |
|---|---|---|
| SQLite 数据库 | `<data>/1mate.db` | 主机、实例、凭据密文、任务历史 |
| **主密钥** | env 变量 或 `<data>/master.key` | **丢失 = 所有凭据不可解密** |
| 实例备份文件 | `<data>/backups/<实例>/<时间戳>.dump` | TeslaMate 数据（也可在实例页随时重建） |

### 9.2 恢复流程

1. 重装平台 → 恢复 `1mate.db` + 主密钥（同一次生成的两者必须配对）；
2. `systemctl start 1mate` → 用原管理员账户登录；
3. 主机凭据自动解密恢复，无需重录。

若主密钥丢失：换新密钥 → 重录全部主机凭据 → 重新部署实例（数据卷仍在主机上；实例的 `ENCRYPTION_KEY` 需从书面记录恢复，否则 TeslaMate 无法解密既有车辆数据）。

---

## 10. 安全清单（上线前逐项核对）

- [ ] `ONEMATE_ADDR` 绑定 `127.0.0.1`，公网访问一律走 TLS 反代
- [ ] `ONEMATE_SECURE_COOKIE=true`（HTTPS 下）
- [ ] `ONEMATE_MASTER_KEY` 已离线备份（与数据库分开存放）
- [ ] `/etc/1mate/1mate.env` 权限 640、属主 1mate:1mate
- [ ] 管理员密码非弱口令（≥10 位，argon2id 存储，登录限流 5 次/分/IP）
- [ ] 目标主机 SSH 指纹已在"测试连接"时人工核对（TOFU 固定，变更需重新信任）
- [ ] 反代已配置 SSE 支持（`proxy_buffering off`）
- [ ] 审计日志可用（登录、密钥查看、安装动作均有记录）

---

## 11. 常见部署问题

| 现象 | 原因 | 处置 |
|---|---|---|
| 服务起不来 `203/EXEC` | 二进制架构不匹配（如 Windows PE 装到 Linux） | `file /usr/local/bin/1mate` 应为 ELF；重新交叉编译 |
| 健康检查 404 | 端口/地址不对 | 核对 `ONEMATE_ADDR` 与防火墙 |
| 解密报错 `wrong master key or corrupted data` | 主密钥与数据库不配对 | 恢复配对的 master.key，切勿混用 env 与文件两种密钥来源 |
| 登录后立即 401 | 反代未透传 Cookie / Secure Cookie 走了 HTTP | 检查 `X-Forwarded-Proto` 与 `ONEMATE_SECURE_COOKIE` |
| 任务日志无实时输出 | 反代缓冲了 SSE | 加 `proxy_buffering off` |
| 探测报 `unsupported` | 目标发行版超出支持列表 | 见 `docs/runbook.md` 主机排错表 |

更多运行期故障排查见 `docs/runbook.md`。

---

## 12. Agent 客户端部署（Agent 模式主机）

Agent 模式适用于**没有公网 IP、位于 NAT/防火墙后**的内网主机：由主机上的 `1mate-agent` 客户端主动外连平台，平台再通过该连接下发运维命令。平台侧无需任何额外组件（v1.1.0+ 已内建 AgentHub 与 `/api/v1/agent/connect` 端点）。

> 完整接入与排错指南见 `docs/agent-mode.md`。本节给出生产部署的最小步骤。

### 12.1 平台侧：创建 Agent 主机

1. 在 Web 控制台「主机」页点击「添加主机」，连接方式选 **Agent（内网主机主动连接）**。
2. 填写名称；Agent ID 与接入令牌可留空（自动生成）。
3. 保存后平台会**一次性明文展示接入令牌**——请立即复制保存（之后不再显示完整令牌）。
4. 也可在主机详情页「连接信息」Tab 查看 Agent ID 与令牌配置状态；通过 API `GET /api/v1/hosts/{id}/agent-install-command` 获取一键安装命令。

### 12.2 目标主机侧：安装 Agent

**方式 A：一键安装命令（推荐）**

复制平台生成的安装命令在内网主机执行，它会写入 `/etc/1mate-agent/agent.env`（0600）与 systemd 单元：

```sh
# 命令由平台生成，形如（server_base_url 填平台地址）：
curl -fsSL http://<平台地址>:8080/...   # 详见主机详情页安装命令
# 然后放置二进制：
sudo cp 1mate-agent-linux /usr/local/bin/1mate-agent
sudo chmod 0755 /usr/local/bin/1mate-agent
sudo systemctl daemon-reload
sudo systemctl enable --now 1mate-agent
```

**方式 B：手动配置**

1. 从 GitHub Releases 下载 `1mate-agent-vX.Y.Z-linux-{amd64,arm64}.tar.gz`，解压后：

```sh
sudo cp 1mate-agent /usr/local/bin/1mate-agent
sudo chmod 0755 /usr/local/bin/1mate-agent
```

2. 写配置文件（平台 HTTPS 用 `wss://`，HTTP 用 `ws://`）：

```sh
sudo install -d -m 0755 /etc/1mate-agent /var/lib/1mate-agent
sudo tee /etc/1mate-agent/agent.env >/dev/null <<'EOF'
AGENT_SERVER_URL=wss://<平台域名>/api/v1/agent/connect
AGENT_TOKEN=<平台生成的一次性令牌>
AGENT_INTERVAL=30
AGENT_RETRY_BASE=5
AGENT_RETRY_MAX=120
EOF
sudo chown root:root /etc/1mate-agent/agent.env
sudo chmod 0600 /etc/1mate-agent/agent.env
```

3. 安装 systemd 单元并启动（模板见 `deploy/agent.service`）：

```sh
sudo cp deploy/agent.service /etc/systemd/system/1mate-agent.service
sudo id -u 1mate >/dev/null 2>&1 || sudo useradd -r -s /usr/sbin/nologin -d /var/lib/1mate-agent 1mate
sudo chown -R 1mate:1mate /var/lib/1mate-agent
sudo systemctl daemon-reload
sudo systemctl enable --now 1mate-agent
```

### 12.3 Agent 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `AGENT_SERVER_URL` | `ws://127.0.0.1:8080/api/v1/agent/connect` | 平台 WS 端点，HTTP→`ws://`，HTTPS→`wss://` |
| `AGENT_TOKEN` | 必填 | 平台生成的一次性接入令牌（机器认证） |
| `AGENT_HOST_ID` | 空 | 可选，仅用于日志显示，不参与认证/路由 |
| `AGENT_INTERVAL` | `30` | 心跳间隔（秒） |
| `AGENT_RETRY_BASE` | `5` | 断线重连基础延迟（秒，指数退避） |
| `AGENT_RETRY_MAX` | `120` | 最大重连延迟（秒） |

### 12.4 验证

```sh
sudo journalctl -u 1mate-agent -f
# 正常：connected to wss://...；断线会自动重连（retrying in Ns）
```

回到平台主机列表点击「探测」，若返回发行版 / Docker / Compose 信息即接入成功。

### 12.5 Agent 模式注意事项

- Agent 以 `1mate` 用户运行；安装 Docker 等特权操作需为目标用户配置相应 sudo 权限（与 SSH 模式一致）。
- Agent 模式下 Web 交互终端（PTY）暂不可用，需要交互式操作请用 SSH 模式。
- 大文件（如 Docker 镜像）走 base64 编码传输，超大镜像建议目标主机本地拉取。
- `agent_token` 等同主机凭据，请离线保存、勿提交代码仓库；泄露时在平台编辑主机轮换令牌并同步更新 `agent.env`。
