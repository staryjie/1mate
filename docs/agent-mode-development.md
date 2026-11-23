# 1mate Agent + SSH 并存模式 — 开发任务清单

> 本文档记录将 1mate 从 pure-agentless SSH 模式过渡到“Agent + SSH 并存”模式的详细开发任务。
> 核心原则：**现有 SSH 功能零退化，Agent 模式为可选并存，两者共享同一套 `sshx.Client` 接口。**

## 一、总体设计（已确认）

| 项目 | 决策 |
|------|------|
| 连接层抽象 | 复用 `internal/sshx.Client` 接口；新增 `internal/agentx.Client` 实现同一接口，底层走 WebSocket → Agent |
| Service 层 | 根据 `Host.ConnectionType`（`"ssh"` \| `"agent"`）选择对应客户端，上层业务无感知 |
| Agent 连接方向 | Agent 作为客户端，主动向 1mate 建立 WebSocket 长连接（反向连接），解决目标主机无公网IP问题 |
| Agent 安装 | 独立静态二进制（CGO_ENABLED=0），systemd 托管，开机自启 |
| 数据库 | `hosts` 表增加 `connection_type` 列，默认 `"ssh"`，现有数据无需迁移 |

---

## 二、开发阶段与里程碑

| 阶段 | 名称 | 预计周次 | 目标 |
|------|------|----------|------|
| Phase 0 | 抽象层与数据库准备 | 第 1–2 周 | `Host.ConnectionType` 字段、数据库迁移、agentx stub，go build 通过，零功能变化 |
| Phase 1 | Agent 服务器端 | 第 2–3 周 | `/api/v1/agent/connect` WebSocket 端点、AgentHub、Agent 认证 |
| Phase 2 | Agent 客户端 | 第 3–4 周 | 独立 agent 二进制、长连接重连、本地命令执行、文件上传下载、PTY |
| Phase 3 | agentx 客户端实现 | 第 4–5 周 | 完整实现 `sshx.Client` 接口，通过 Agent 通道执行所有操作 |
| Phase 4 | Service 层双路径支持 | 第 5–6 周 | 探测、安装 Docker、部署/启停/重启/销毁、备份/恢复、镜像同步、终端，均在 Agent 模式下工作 |
| Phase 5 | 前端支持 | 第 6–7 周 | 主机添加/编辑页面增加“连接方式”选择，列表区分 SSH/Agent，Agent 主机终端可用 |
| Phase 6 | 迁移工具与生产验证 | 第 7–8 周 | SSH→Agent 迁移辅助、文档、端到端 E2E、v1.1.0-rc.1 发布 |

---

## 三、详细开发任务

### Phase 0：抽象层与数据库准备（第 1–2 周）

#### 任务 0.1：Host 模型增加 ConnectionType 字段
- 文件：`internal/model/model.go`
- 内容：在 `Host` 结构体中增加 `ConnectionType string \`json:"connection_type"\`` 字段，默认值 `"ssh"`
- 验收：`go build ./...` 通过，现有 host CRUD 测试不受影响

#### 任务 0.2：数据库迁移脚本
- 文件：`internal/store/migrations/0003_add_connection_type.sql`
- 内容：`ALTER TABLE hosts ADD COLUMN connection_type TEXT NOT NULL DEFAULT 'ssh';`
- 验收：迁移可自动应用，现有数据默认 `ssh`

#### 任务 0.3：store 层读取新列
- 文件：`internal/store/instances.go`（`scanInstance` 函数）
- 内容：`scanInstance` 增加 `connection_type` 列的读取（扫描 30 列 → 31 列，修正占位符数量）
- 验证：`go test ./internal/store/...` 通过

#### 任务 0.4：agentx 包空桩（compile-only）
- 文件：`internal/agentx/agentx.go`
- 内容：定义 `Client` 结构体，实现 `sshx.Client` 接口的所有方法，方法内部先返回 `errors.New("not implemented")`
- 验收：`go build ./...` 通过，现有 sshx 调用零改动

#### 任务 0.5：Phase 0 回归验证
- 命令：`go build ./... && go test ./internal/... && npm run build`（在 web 目录）
- 验收：所有测试通过，前端构建成功，无任何功能变化

---

### Phase 1：Agent 服务器端（第 2–3 周）

#### 任务 1.1：AgentHub 服务
- 文件：`internal/service/agent.go`（新建）
- 内容：
  - `AgentHub` 结构体：`agentConns sync.Map`（agentID → `*agentConn`）
  - `Register(agentID string, conn *websocket.Conn)`：注册 Agent 连接
  - `Unregister(agentID string)`：注销
  - `SendCommand(agentID string, cmd Command) (Result, error)`：向指定 Agent 下发命令并等待返回
  - 命令协议：JSON 帧 `{cmd_id, cmd, timeout}` → 返回 `{cmd_id, exit_code, stdout, stderr}`
- 验收：并发安全，支持多 Agent 同时在线

#### 任务 1.2：WebSocket 连接端点
- 文件：`internal/api/handlers.go`（新增 `handleAgentConnect`）、`internal/api/router.go`（注册路由）
- 路由：`GET /api/v1/agent/connect`
- 鉴权：需要已登录会话（Cookie），跳过 CSRF（WebSocket 无法携带 CSRF Header）
- Agent 认证：连接时携带查询参数 `agent_token`，服务端校验与 Host 绑定的 token
- 验收：无 token 的连接被拒绝（403），有效 token 可建立 WS 连接（101）

#### 任务 1.3：Agent 连接生命周期
- 内容：Agent 断线时自动从 AgentHub 移除；1mate 主动下发命令时若 Agent 离线返回 `agent_offline` 错误
- 验收：单元测试覆盖注册/注销/发送命令/离线场景

#### 任务 1.4：单元测试
- 文件：`internal/service/agent_test.go`
- 内容：测试 AgentHub 注册、注销、发送命令、并发安全
- 验收：`go test ./internal/service/...` 全部通过

---

### Phase 2：Agent 客户端（第 3–4 周）

#### 任务 2.1：Agent 独立模块
- 文件：`agent/main.go`（新建模块 `agent/`）
- 内容：独立 Go main 包，编译为单静态二进制 `CGO_ENABLED=0 go build -o agent ./agent/`
- 验收：产生 ~10MB ELF 二进制，无外部依赖

#### 任务 2.2：Agent 配置读取
- 内容：读取环境变量 `1MATE_SERVER_URL`、`1MATE_AGENT_TOKEN`、`1MATE_HOST_ID`
- 验收：缺少必要配置时退出并打印错误信息

#### 任务 2.3：WebSocket 长连接
- 内容：Agent 主动连接 1mate `/api/v1/agent/connect`，携带 `agent_token`；断线自动重连（指数退避，最大 5 分钟）
- 验收：网络断开后自动恢复连接

#### 任务 2.4：命令执行
- 内容：收到 1mate 下发的命令后，使用 `os/exec` 在本地执行，捕获 stdout/stderr/exit code，通过 WS 返回
- 验收：可执行 `docker ps`、`whoami`、`uname -a` 等命令并返回正确结果

#### 任务 2.5：文件上传/下载
- 内容：支持 WS 二进制帧传输文件；Agent 本地写文件（原子写入 tmp+mv）；读取文件内容通过 WS 返回
- 验收：可传输几 MB 大小的文件

#### 任务 2.6：PTY 支持（用于 Web 终端）
- 内容：使用 `github.com/creack/pty` 在本地启动伪终端，WS 双向转发终端 I/O
- 验收：Web 终端在 Agent 主机上可交互使用

#### 任务 2.7：systemd 单元
- 文件：`deploy/agent.service`（新建）
- 内容：systemd 服务单元，开机自启，重启策略 `on-failure`
- 验收：`systemctl enable --now agent` 可运行

---

### Phase 3：agentx 客户端实现（第 4–5 周）

#### 任务 3.1：agentx.Client 结构体
- 文件：`internal/agentx/agentx.go`
- 内容：Client 持有 `agentID string` 和 `hub *service.AgentHub`（或通过接口解耦）
- 实现 `sshx.Client` 接口的全部方法
- 验收：`go build ./...` 通过

#### 任务 3.2：Run / RunStreaming
- 内容：通过 WS 发送命令，等待 Agent 返回结果，构造 `sshx.Result`
- 验收：与 SSH Dialer 行为一致

#### 任务 3.3：Upload / UploadReader
- 内容：将文件内容通过 WS 发给 Agent，Agent 本地写文件；支持原子写入（tmp+mv）
- 验收：文件可正确写入目标路径

#### 任务 3.4：Download / DownloadWithCommand
- 内容：Agent 读取文件或命令输出，通过 WS 返回流；支持大文件流式传输
- 验收：`pg_dump` 流式备份在 Agent 模式下可用

#### 任务 3.5：RunWithStdin
- 内容：将 stdin 数据通过 WS 发给 Agent，Agent 写入目标进程 stdin
- 用途：`docker load` 镜像流传输
- 验收：镜像同步在 Agent 模式下可用

#### 任务 3.6：PtyStart
- 内容：Agent 本地启动 PTY，WS 双向转发终端 I/O
- 验收：Web 终端在 Agent 主机上可用

#### 任务 3.7：HostKeyFingerprint
- 内容：返回 Agent 标识（非 SSH 指纹，用 agentID）
- 验收：不报错

#### 任务 3.8：错误处理
- 内容：Agent 离线时返回 `agent_offline` 错误；网络错误区分于执行错误
- 验收：前端可区分“主机离线”和“命令执行失败”

#### 任务 3.9：单元测试
- 文件：`internal/agentx/agentx_test.go`
- 内容：使用 mock AgentHub 测试所有方法
- 验收：`go test ./internal/agentx/...` 通过

---

### Phase 4：Service 层双路径支持（第 5–6 周）

#### 任务 4.1：Service 双客户端工厂
- 文件：`internal/service/service.go`
- 内容：Service 持有 `sshx.Client` 工厂和 `agentx.Client` 工厂；`Connect(host *model.Host) (sshx.Client, error)` 根据 `ConnectionType` 返回对应客户端
- 验收：SSH 主机返回 `sshx.Dialer`，Agent 主机返回 `agentx.Client`

#### 任务 4.2：ProbeHost 支持 Agent
- 文件：`internal/service/hosts.go`
- 内容：Agent 模式下通过 agentx 上传探测脚本并运行，与 SSH 相同逻辑
- 验收：Agent 主机可探测 OS/Docker/Compose/端口占用

#### 任务 4.3：InstallDocker 支持 Agent
- 文件：`internal/service/provision.go`
- 内容：Agent 模式下通过 agentx 上传并运行安装脚本
- 验收：Agent 主机可安装 Docker 和 Compose

#### 任务 4.4：实例生命周期支持 Agent
- 文件：`internal/service/lifecycle.go`
- 内容：deploy/start/stop/restart/upgrade/destroy 在 Agent 模式下通过 agentx 执行 docker compose 命令
- 验收：Agent 主机可管理实例全生命周期

#### 任务 4.5：备份/恢复支持 Agent
- 文件：`internal/service/backups.go`
- 内容：Agent 模式下通过 `DownloadWithCommand` 执行 `pg_dump -Fc`，流式备份
- 验收：Agent 主机可备份/恢复数据库

#### 任务 4.6：镜像同步支持 Agent
- 文件：`internal/service/images.go`
- 内容：Agent 模式下 Agent 本地 `docker save | docker load`
- 验收：Agent 间镜像同步可用

#### 任务 4.7：TestConnection 支持 Agent
- 文件：`internal/service/hosts.go`
- 内容：Agent 模式下通过 Agent 心跳检测连接状态
- 验收：Agent 主机可测试连接

#### 任务 4.8：回归测试
- 内容：所有现有 SSH 路径的单元测试仍通过
- 验收：`go test ./internal/service/...` 全部通过

---

### Phase 5：前端支持（第 6–7 周）

#### 任务 5.1：API 类型更新
- 文件：`web/src/api/types.ts`
- 内容：`Host` 类型增加 `connection_type` 字段；新增 `AgentHostInput` 类型
- 验收：TypeScript 类型正确

#### 任务 5.2：API 接口更新
- 文件：`web/src/api/handlers.go`（后端）、`web/src/api/hooks.ts`（前端）
- 内容：hosts CRUD 增加 `connection_type` 字段；Agent 模式下不要求 SSH 凭据，改为要求 `agent_token`
- 验收：API 接受两种模式的主机创建/编辑

#### 任务 5.3：主机添加/编辑页面
- 文件：`web/src/pages/HostDetailPage.tsx`
- 内容：添加“连接方式”单选（SSH / Agent）；Agent 模式下显示 Agent Token 输入框，隐藏 SSH 密码/密钥字段
- 验收：前端可切换两种连接模式

#### 任务 5.4：主机列表区分
- 文件：`web/src/pages/HostsPage.tsx`
- 内容：主机列表显示连接方式图标（SSH / Agent）
- 验收：列表可区分两种模式的主机

#### 任务 5.5：实例创建支持 Agent 主机
- 文件：`web/src/pages/InstanceWizardPage.tsx`
- 内容：创建实例时，Agent 主机同样可选作为部署目标
- 验收：实例创建不受连接方式影响

#### 任务 5.6：Agent 模式 Web 终端
- 文件：`web/src/components/Terminal.tsx`
- 内容：Agent 模式下通过 agentx.PtyStart 建立 WebSocket 终端
- 验收：Agent 主机终端可用

#### 任务 5.7：前端构建验证
- 命令：`npm run build`（在 web 目录）
- 验收：`tsc -b` 和 `vite build` 均通过，无编译错误

---

### Phase 6：迁移工具与生产验证（第 7–8 周）

#### 任务 6.1：SSH→Agent 迁移辅助
- 文件：`internal/service/migrate.go`（新建）
- 内容：生成 Agent 安装命令、导出实例配置、生成目标主机上的 Agent 安装脚本
- 验收：可一键生成目标主机上的 Agent 安装命令

#### 任务 6.2：Agent 模式文档
- 文件：`docs/agent-mode.md`（新建）
- 内容：Agent 模式架构、安装步骤、与 SSH 模式的区别、故障排查、安全说明
- 验收：文档完整，可供运维参考

#### 任务 6.3：端到端生产验证
- 环境：ser5-vm-317（已有 SSH 主机） + fnos（新增 Agent 主机）
- 验收链路：
  1. Agent 主机添加 → 探测成功（OS/Docker/Compose）
  2. 安装 Docker/Compose（如需要）
  3. 部署 TeslaMate 实例
  4. 启动/停止/重启实例
  5. 备份/恢复数据库
  6. Web 终端连接
  7. 镜像同步（SSH 主机 → Agent 主机）
- 验收：全链路验证通过，无阻塞问题

#### 任务 6.4：性能与稳定性测试
- 内容：Agent 模式下并发 10 个主机执行命令，24 小时连接稳定性观察
- 验收：无连接泄漏，WS 长连接稳定

#### 任务 6.5：发布 v1.1.0-rc.1
- 内容：git tag、GitHub Release、发布二进制（amd64 + arm64）
- 验收：Release 页面可下载，版本号正确显示

---

## 四、兼容性与风险控制

### 不破坏现有功能的原则
- 所有现有 `sshx.Client` 调用**零改动**
- `Host.ConnectionType` 默认 `"ssh"`，现有数据库记录无需迁移即可继续工作
- Agent 模式为可选，用户可继续只使用 SSH

### 主要风险与对策

| 风险 | 影响 | 对策 |
|------|------|------|
| Agent 离线 | Agent 主机的实例操作不可用 | 返回 `agent_offline` 错误，前端显示重试；SSH 主机不受影响 |
| WS 连接被防火墙阻断 | Agent 无法接入 | 支持配置 WS 端口（默认复用 1mate 端口 8080） |
| Agent token 泄露 | 他人可冒充 Agent | token 与 Host 绑定，支持在 1mate 端废弃并重新生成 |
| 文件传输大小限制 | 镜像同步等大文件操作慢 | WS 二进制帧分片传输，支持大文件 |
| Agent 版本与 1mate 不匹配 | 协议不兼容 | Agent 上报版本，1mate 拒绝不兼容版本，提示升级 |

### 测试策略
- `sshx.Fake` 继续用于 SSH 路径测试（零改动）
- 新增 `agentx.Fake` 用于 Agent 路径测试
- 集成测试：在本地运行 1mate + Agent，模拟完整操作链路
- 所有新增代码必须有对应单元测试，覆盖率不低于现有水平

---

## 五、立即可执行的第一步（Phase 0）

本周可立即开始：

1. 修改 `internal/model/model.go`，给 `Host` 结构体增加 `ConnectionType` 字段
2. 创建 `internal/store/migrations/0003_add_connection_type.sql`
3. 修改 `internal/store/instances.go` 的 `scanInstance` 增加列读取
4. 运行 `go build ./... && go test ./internal/...` 确认无回归

这些改动不影响任何现有功能，只是为 Agent 模式铺路。完成后即可进入 Phase 1。

---

## 六、相关文件清单

### 需要新建
| 文件 | 用途 |
|------|------|
| `internal/agentx/agentx.go` | Agent 传输客户端（实现 sshx.Client） |
| `internal/agentx/agentx_test.go` | agentx 单元测试 |
| `internal/service/agent.go` | AgentHub 服务（WebSocket 连接管理） |
| `internal/service/agent_test.go` | AgentHub 单元测试 |
| `agent/main.go` | Agent 客户端主程序（独立模块） |
| `deploy/agent.service` | Agent systemd 单元 |
| `docs/agent-mode.md` | Agent 模式架构与使用文档 |

### 需要修改
| 文件 | 主要改动 |
|------|----------|
| `internal/model/model.go` | Host 增加 `ConnectionType` 字段 |
| `internal/store/migrations/0003_*.sql` | 数据库迁移 |
| `internal/store/instances.go` | scanInstance 读取新列 |
| `internal/api/handlers.go` | agent WebSocket 端点，hosts CRUD 增加字段 |
| `internal/api/router.go` | 注册 agent 路由 |
| `internal/service/service.go` | Connect(host) 双路径选择 |
| `internal/service/hosts.go` | ProbeHost/TestConnection 支持 Agent |
| `internal/service/provision.go` | InstallDocker 支持 Agent |
| `internal/service/lifecycle.go` | 实例操作支持 Agent |
| `internal/service/backups.go` | 备份/恢复支持 Agent |
| `internal/service/images.go` | 镜像同步支持 Agent |
| `web/src/api/types.ts` | Host 类型增加字段 |
| `web/src/api/hooks.ts` | API 接口增加字段 |
| `web/src/pages/HostDetailPage.tsx` | 连接方式选择 |
| `web/src/pages/HostsPage.tsx` | 列表显示模式 |
| `web/src/pages/InstanceWizardPage.tsx` | 实例创建支持 Agent 主机 |
| `web/src/components/Terminal.tsx` | Agent 模式终端支持 |

### 零改动（保持）
| 文件/目录 | 原因 |
|-----------|------|
| `internal/sshx/` | 完全保留，现有 SSH 功能不变 |
| `internal/job/` | 任务队列逻辑不变 |
| `internal/compose/` | Compose 模板不变 |
| `internal/cryptox/` | 加密逻辑不变 |
| `internal/store/` 除新列外 | 零改动 |

---

## 七、进度跟进

| 阶段 | 状态 | 开始日期 | 完成日期 | 备注 |
|------|------|----------|----------|------|
| Phase 0 | ✅ 完成 | 2025-11-23 | 2025-11-23 | ConnectionType 字段 + 迁移 + store；commit 2ffbd21 |
| Phase 1 | ✅ 完成 | 2025-11-23 | 2025-11-23 | AgentHub + WS 端点 + 认证 + 4 测试；commit d68b0a7 |
| Phase 2 | ✅ 完成 | 2025-11-23 | 2025-11-23 | 独立 agent 客户端 + systemd 单元；commit 3a3f8c6 |
| Phase 3 | ✅ 完成 | 2025-11-23 | 2025-11-23 | agentx.Client(sshx.Client) + 16 测试；commit aa991eb |
| Phase 4 | ✅ 完成 | 2025-11-23 | 2025-11-23 | Service.Connect 双路径；commit 84a16a8 |
| Phase 5 | ✅ 完成 | 2025-11-23 | 2025-11-23 | 前端 Agent 模式支持；commit 99b9aee |
| Phase 6 | 🔶 部分完成 | 2025-11-23 | — | 6.1/6.2 完成；6.3 真实主机 E2E ✅ 全链路通过（探测/创建/部署/启停/备份）；6.4 稳定性长期观察中；6.5 待发布 |

> 更新方式：每完成一个任务，在对应行打 ✅；遇到阻塞，在备注中说明。
>
> **Phase 6 已知修复的阻塞性 bug**：
> 1. `agent/connect` 错误使用 `requireAuth`（Agent 无登录会话）→ 改为 token 机器认证；
> 2. 协议不匹配（客户端未传 host_id）→ 服务端用 token 反查主机，客户端只需 token；
> 3. token 未 URL 编码（`+` `/` `=` 破坏查询参数）→ `url.QueryEscape`；
> 4. Agent 主机无 SSH 凭据违反 `credential_id` 外键 → 占位凭据记录（`CredAgentToken`）。

---

*本文档在 plan mode 下制定，待用户审查通过后方可开始实施。任何阶段发现重大阻塞可随时调整阶段划分。*