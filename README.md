# 1mate

TeslaMate 多实例一键部署平台。通过 **SSH（无代理直连）** 或 **Agent（内网主机主动外连）** 管理 Linux 主机，探测并按需安装 Docker 与 Docker Compose v2，在 Web 界面完成实例的创建、部署、启停、升级、备份与销毁。

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

## 能力概览

- **双模主机接入**：
  - **SSH 模式**（默认）：平台直连主机 SSH，凭据加密存储、主机指纹（TOFU）固定；
  - **Agent 模式**：无公网 / NAT 后的内网主机运行轻量 `1mate-agent` 客户端主动外连平台，只需配置 `AGENT_SERVER_URL` + `AGENT_TOKEN`，无需公网 IP 与 SSH 端口。两种模式共用同一套操作接口，探测/部署/启停/备份能力一致。详见 `docs/agent-mode.md`。
- **主机能力探测**：一键探测发行版 / 架构 / Docker 客户端与服务端 / Compose 种类与版本 / systemd / 磁盘 / 端口占用。
- **Docker / Compose 一键安装**：识别 v1（`docker-compose`）与 v2（`docker compose`）；
  缺失时从官方仓库或 get.docker.com 脚本安装，**始终优先 Compose v2 插件**，二进制
  兜底也使用 v2；绝不安装或卸载 Compose v1；支持阿里云 / Azure 中国镜像源与版本固定。
  脚本幂等，已满足条件时零改动；执行前可预览全文，执行需显式确认。
- **实例编排**：按主机探测结果自动选择命令形式与 compose 文件方言（v2 `name:` 或
  v1 `version:`）；端口冲突双重预检（平台内 + 远端监听端口）；deploy / start / stop /
  restart / upgrade / reconcile / destroy 全通过带锁的持久任务队列执行，日志经 SSE 实时
  推流，脱敏落盘。
- **Web 终端**：主机详情页提供浏览器端 SSH PTY 终端，支持断开/重连（SSH 模式）。
- **镜像同步**：主机间流式 `docker save | docker load`，部署前镜像预检；Docker Hub 不可达时可从能拉取镜像的主机同步到目标主机。
- **任务重试**：失败/中断的持久任务可在任务中心一键重试（`POST /jobs/{id}/retry`）。
- **导入接管**：可导入已有的 TeslaMate 实例，只读接管（不改写远程 compose/.env），支持启停/备份/删除。
- **数据保护**：`pg_dump -Fc` 流式备份到平台侧，恢复前自动安全备份；PostgreSQL 大
  版本变更直接拒绝并提示 dump/restore 流程；`ENCRYPTION_KEY` 支持审计后的明文查看。
- **安全**：AES-256-GCM 绑定行级 AAD；argon2id 密码；会话 Cookie HttpOnly + CSRF
  头校验；登录限流；全部日志与 API 输出经统一脱敏器；审计日志覆盖登录、变更、
  密钥查看与安装动作；支持用户自助修改密码。

## 快速开始（开发）

```sh
make web          # 构建前端，产物嵌入 internal/webui/dist
make dev-api      # 启动后端 http://127.0.0.1:8080
make dev-web      # 可选：前端热更新代理 /api -> :8080
```

首次访问会引导创建管理员账户。

## 构建与部署

完整部署文档（systemd / Docker / Compose 三种方式、主密钥管理、反向代理、升级与恢复）见 **`docs/deployment.md`**。要点：

```sh
make build        # bin/1mate 单二进制（已内嵌前端）
```

服务器只需一个二进制与数据目录：

```sh
export ONEMATE_ADDR=127.0.0.1:8080
export ONEMATE_DATA_DIR=/var/lib/1mate
export ONEMATE_SECURE_COOKIE=true   # 走 HTTPS 反代时开启
./1mate
```

环境变量见 `deploy/1mate.service` 与下表：

| 变量 | 默认 | 说明 |
|---|---|---|
| `ONEMATE_ADDR` | `127.0.0.1:8080` | 监听地址；非回环地址时启动会告警 |
| `ONEMATE_DATA_DIR` | `./data` | SQLite、任务日志、备份、主密钥目录 |
| `ONEMATE_MASTER_KEY` | 自动生成 | base64 32B；优先于数据目录中的密钥文件 |
| `ONEMATE_LOG_LEVEL` | info | debug / info / warn / error |
| `ONEMATE_SESSION_TTL` | 168h | 会话绝对有效期 |
| `ONEMATE_SESSION_IDLE` | 12h | 会话空闲有效期 |
| `ONEMATE_SECURE_COOKIE` | false | 反代 TLS 时置 true |
| `ONEMATE_WORKERS` | 4 | 任务并发上限 |

systemd 与 Docker 交付示例见 `deploy/`。

## 远程主机要求

| 项目 | SSH 模式 | Agent 模式 |
|---|---|---|
| 系统 | Linux amd64 / arm64 | Linux amd64 / arm64 |
| 连接方式 | 平台可 SSH 到达主机（私钥/密码认证） | 主机可访问平台 HTTP(S)，无需公网 IP / SSH 端口 |
| Docker / Compose | 可缺失，平台一键安装 | 同左（Agent 下发安装命令） |
| 提权 | 非 root 用户需 sudo（免密/密码） | 运行 Agent 的用户需相应 Docker/sudo 权限 |
| Web 终端 | ✅ 支持 | ⚠️ 暂不支持（PTY 未实现） |

- Docker / Compose 自动安装支持 Ubuntu 22.04/24.04/24.10/26.04、Debian 12/13、RHEL 9 系、Fedora 41+；其他发行版给出手工命令，始终优先 Compose v2。
- sudo 密码经 stdin 传递，不落日志。
- armv7 / 32 位主机：TeslaMate 官方镜像不再支持，平台直接拒绝。
- Agent 模式接入详见 `docs/agent-mode.md`；服务端与 Agent 部署详见 `docs/deployment.md`。

## API

完整的接口定义：`docs/openapi.yaml`。要点：

- 所有接口在 `/api/v1` 下；未登录返回 401，缺 CSRF 头返回 403。
- 写操作一律返回 `{job_id}`，用 `GET /jobs/{id}/events`（SSE）看进度。
- 错误体统一为 `{code, message}`，错误码含 `validation_failed`、`conflict`、
  `forbidden`、`host_unreachable` 等。
- 安装类接口要求 `accept_system_changes: true`，否则 400。

## 目录结构

```
cmd/1mate/          平台入口与任务注册
agent/              独立 Agent 客户端（module agent，部署在内网主机）
internal/api/       HTTP 路由、WebSocket 终端/Agent、SSE、静态资源
internal/agentx/    Agent 通道（实现 sshx.Client，走 Agent WebSocket）
internal/auth/      argon2id、会话、CSRF、限流
internal/compose/   compose v1/v2 模板与命令选择
internal/cryptox/   AES-256-GCM 密封箱
internal/job/       持久任务队列、锁、日志流
internal/model/     领域模型
internal/probe/     能力探测脚本与解析
internal/provision/ Docker/Compose 安装脚本生成
internal/service/   主机、实例、备份、生命周期编排、AgentHub
internal/store/     SQLite + migrations
internal/sshx/      SSH 客户端抽象（真实 + Fake）
internal/webui/     嵌入的前端产物
web/                React 控制台源码
deploy/             systemd unit、Dockerfile、agent.service
docs/               openapi.yaml、runbook、deployment、development、agent-mode
```

## 安全说明

- 密钥不出现在任何 API 响应、日志或前端存储；查看明文密钥是显式动作并写入审计。
- 远端命令全部由固定模板生成，用户输入仅做白名单参数；shell 变量单引号转义。
- sudo 密码仅通过 stdin 提供，绝不进入命令行参数。
- 主机 SSH 指纹首次记录后固定，变更必须人工重新信任。
- 升级 / 恢复 / 销毁等破坏性操作均要求显式参数或输入实例 slug 确认。

## 文档索引

| 文档 | 内容 |
|---|---|
| `docs/deployment.md` | **服务端部署**（systemd / Docker / Compose）、升级、备份恢复、反向代理；**Agent 客户端部署**见第 12 节 |
| `docs/agent-mode.md` | Agent 模式完整接入指南（架构、配置、安装、验证、排错、安全） |
| `docs/runbook.md` | 日常运维、故障排查与升级流程 |
| `docs/development.md` | 开发文档（架构、设计决策、测试与调试、打 tag / 跨平台编译 / 发布流程） |
| `docs/agent-mode-development.md` | Agent 模式开发计划与阶段进度 |
| `docs/openapi.yaml` | 完整 API 接口定义（25+ 端点） |

当前版本：v1.0.0（正式版）、v1.1.0-rc.1（Agent 模式候选版）。发布记录见 `CHANGELOG.md` 与 GitHub Releases。
