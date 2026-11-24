# Agent 模式功能缺口清单（Backlog）

本文档记录 Agent 模式（v1.1.0-rc.1）目前**尚未实现或实现不完整**的功能点，逐条给出编号、证据（代码位置）、影响与状态。修复进度直接更新本表「状态」列。

> 编号规则：`G<n>`。状态：`待修复` / `修复中` / `已修复` / `不修复（设计取舍）`。

## 总览

| 编号 | 缺口 | 严重度 | 状态 |
|---|---|---|---|
| G1 | WebSocket 并发写无锁，存在帧损坏风险 | P0 | 已修复 |
| G2 | 文件上传走单条命令，超过 ~96KB 必然失败（恢复备份不可用） | P0 | 已修复 |
| G3 | 文件/命令输出全量缓冲进内存，大备份 OOM、镜像导出不可用 | P0 | 已修复 |
| G4 | 无流式输出，部署/拉镜像日志长时间无响应 | P1 | 已修复 |
| G5 | 命令不可取消，平台取消任务后 Agent 仍在执行 | P1 | 已修复 |
| G6 | Agent 模式无法提权（SudoMode 被强制 none，sudo 密码无通道） | P1 | 已修复 |
| G7 | agent_token 走 URL query，会被反代访问日志记录 | P1 | 已修复 |
| G8 | PTY / Web 终端未实现（Agent 主机显示“暂不支持”警告） | P2 | 已部分修复 |
| G9 | 无 Agent 版本上报，平台看不到客户端版本，升级需手工替换 | P2 | 已修复 |
| G10 | 无 TLS 自定义（自签证书的 wss:// 连不上，不能配 CA） | P2 | 已修复 |
| G11 | 服务端读循环无读超时/pong 处理，半开连接被误判为在线 | P2 | 已修复 |
| G12 | Agent 端无命令白名单/审计，收到即执行 | P2 | 设计取舍（文档已警告） |
| G13 | 无系统指标上报（CPU/内存/磁盘） | P3 | 待修复 |

---

## G1 — WebSocket 并发写无锁

**证据**
- gorilla/websocket v1.5.3 的 `WriteMessage` 内部不加锁（已核对 `conn.go` 源码；官方要求应用层保证同一时刻只有一个写者）。
- 服务端 `internal/service/agent.go` 的 `SendCommand` 直接调用 `ac.conn.WriteJSON(command)`；多个 HTTP 处理器（镜像预检、测试连接）与后台任务可同时对同一台主机下发命令。
- Agent 端 `agent/main.go` 每收到一条命令即 `go handleCommand(conn, cmd)`，各自调用 `sendResult` → `conn.WriteMessage`；心跳 `heartbeat()` 也写同一连接，且互斥锁 `sendMu` 只覆盖心跳、未覆盖 `sendResult`。

**影响**：并发写会交错破坏 WebSocket 帧，导致结果丢失、命令莫名其妙失败，甚至连接被判为异常而重连。

**修复方案**：两端各引入单一写者（写锁包装）。

---

## G2 — 文件上传超过 ~96KB 必然失败

**证据**
- `internal/agentx/agentx.go` 的 `Upload` 把整个文件 base64 后拼进一条命令：
  `mkdir -p ... && echo '<base64>' | base64 -d > ...`
- Agent 端以 `sh -c "<整条命令>"` 执行（`agent/main.go` `runCommand`）。
- Linux 单个命令行参数上限 `MAX_ARG_STRLEN = 32 页 = 131072` 字节；base64 膨胀 4/3，因此**文件超过约 96KB 即触发 E2BIG**。

**影响**：`UploadReader`（恢复备份上传 dump）、compose/.env 之外的任何较大文件在 Agent 模式不可用。v1.1.0-rc.1 的 E2E 备份能通过，是因为空库 dump 极小。

**修复方案**：改为分块上传协议（`up_begin` / `up_chunk` / `up_end`，每块 32KB 以内，逐块确认）。

---

## G3 — 全量缓冲进内存

**证据**
- `agentx.DownloadWithCommand` 先 `Run` 把整段 stdout 收进 `sshx.Result.Stdout`（单个 JSON 字符串），再 `w.Write`。
- `agentx.RunWithStdin` 与 `UploadReader` 用 `io.ReadAll` 全量读入后 base64。

**影响**：GB 级 `pg_dump` 会内存暴涨并撑爆单帧消息上限；`docker save` 镜像同步在 Agent 模式实际不可用。

**修复方案**：下载改为分块（`down_begin` / `down_chunk` / `down_end`），边收边写；上传同理（见 G2）。

---

## G4 — 无流式输出

**证据**：`agentx.RunStreaming` 先 `Run` 取完整结果，再把 `Stdout`/`Stderr` 一次性交给 sink。

**影响**：部署（`docker compose up -d`，超时 60 分钟）、镜像拉取等长任务，界面在命令结束前**没有任何日志**，用户以为卡死。

**修复方案**：命令执行过程中以 `out` 帧增量回传 stdout/stderr。

---

## G5 — 命令不可取消

**证据**：服务端 `Manager.Cancel` 只取消本地等待；协议里没有 cancel 消息，Agent 用 `context.Background()` 执行（`runCommand`）。

**影响**：用户取消任务后 Agent 侧 `docker compose up` 仍在跑，可能与后续任务冲突。

**修复方案**：新增 `cancel` 帧，Agent 维护 cmdID → cancelFunc 映射。

---

## G6 — 无法提权

**证据**
- `internal/service/hosts.go` `updateAgentHost` 强制 `h.SudoMode = model.SudoNone`。
- `sshx.Sudo.Prefix()` 对 `password` 模式返回空串；SSH 通道靠 stdin 传密码，Agent 通道没有等价机制。
- `agentx.Run` 只应用 `opts.Sudo.Prefix()`，忽略 `opts.Env`、`opts.StdinText`、`opts.Prefix`。

**影响**：Agent 以非 root 用户运行时，安装 Docker、执行 `docker compose` 全部失败；平台也不允许给 Agent 主机配置 sudo 模式。

**修复方案**：允许 Agent 主机设置 `nopasswd`；`password` 模式由平台把密码放进独立的 `sudo_password` 字段（不拼进命令），Agent 用 `sudo -S` 并从内存写 stdin。

---

## G7 — token 在 URL query 中

**证据**：`agent/main.go` `connectWS` 拼 `?agent_token=...`；`handleAgentConnect` 用 `r.URL.Query().Get("agent_token")`。

**影响**：反向代理访问日志、浏览器/网关审计会记录完整令牌。

**修复方案**：改用握手请求头 `X-Agent-Token`，服务端两种都接受以兼容旧客户端。

---

## G8 — PTY / Web 终端

**证据**：`agentx.PtyStart` 返回 `ErrAgentPTYNotImplemented`。

**影响**：Agent 主机没有交互式终端，只能靠任务日志排障。

**修复方案**：平台端已对 Agent 模式主机在 Web 终端 Tab 显示“暂不支持”警告（commit 于 2026-11-24），引导用户使用 SSH 模式主机的 Web 终端。真 PTY 需要 WS 上的 PTY 会话复用（pty 请求/数据/尺寸/关闭帧）并引入 `github.com/creack/pty` 依赖，工作量较大，列入后续开发计划。

---

## G9 — 无版本上报

**证据**：`agent/main.go` 无 `--version`，握手不带版本；服务端 `AgentHub` 不记录版本。

**影响**：平台无法判断某台主机跑的是哪个 Agent 版本，升级靠人工核对。

**修复方案**：握手首帧发 `hello{version,os,arch}`，服务端记录并在主机视图展示。

---

## G10 — 无 TLS 自定义（已修复）

**证据（修复前）**：`connectWS` 用 `websocket.DefaultDialer`，不支持 `wss://`。

**影响（修复前）**：平台使用自签证书时 `wss://` 直接失败，无法配置 CA 或跳过校验。

**修复**：
- `Config` 新增 `CACertFile` / `InsecureSkipVerify` 字段（`agent/main.go`）。
- `loadConfig` 读取环境变量 `AGENT_CA_FILE`、`AGENT_INSECURE_SKIP_VERIFY`。
- `connectWS(cfg *Config)`：`wss://` 时构建自定义 `tls.Config`（`InsecureSkipVerify`、`MinVersion TLS1.2`，`AGENT_CA_FILE` 非空则加载 PEM 到 `RootCAs`）；`ws://` 连接时 `tlsConfig` 为 nil。
- 使用自定义 `websocket.Dialer`（`TLSClientConfig=tlsConfig`）。

**环境变量**：
- `AGENT_CA_FILE`：自定义 CA 证书 PEM 文件路径，用于 `wss://` 连接的服务器证书验证。
- `AGENT_INSECURE_SKIP_VERIFY`：`true`/`1` 时跳过服务器证书验证（仅用于测试/自签名证书，生产环境不建议开启）。

---

## G11 — 半开连接误判在线（已修复）

**证据**：服务端 `readLoop` 只阻塞在 `ReadMessage`，未设置 read deadline，也未注册 pong 处理。

**影响**：网络静默中断（拔网线、NAT 超时）时 `IsConnected` 长时间返回 true，任务会先超时才报错。

**修复**：
- Agent 端：心跳帧改为 `websocket.PingMessage`（原 `framePong` 语义错误），`SetPongHandler` + `SetReadDeadline(pongWait=90s)`，`AGENT_INTERVAL` 不得超过 `pongWait`，否则读取超时会误触发重连。
- 服务端：`agentConnection` 增加 `lastSeen` 字段，每帧更新；新增 `LastSeen()` 和 `IsConnectedFresh(agentID, maxAge)` 方法；`handleAgentConnect` 设置 WebSocket 读超时和 pong 处理器；`handleListHosts` / `handleGetHost` 返回 `last_seen` 字段。
- 前端：`Host` 类型增加 `last_seen`；HostsPage 新增「连接新鲜度」列，Agent 主机显示已连接/可能已断开/未上报。

---

## G12 — Agent 端无命令白名单

**证据**：`runCommand` 直接 `sh -c`。

**影响**：拿到 Agent 通道即等同拿到主机 shell。

**状态**：设计取舍——Agent 本质是远程执行代理，白名单会破坏"统一 sshx 接口"的设计。保留文档警告；若需收紧，后续可加"仅允许平台签发的命令模板"。

---

## G13 — 无系统指标上报

**证据**：Agent 只发 Ping。

**影响**：主机列表看不到 Agent 主机的负载/磁盘（SSH 模式靠探测脚本拿一次快照）。

**修复方案**：心跳帧携带轻量指标（load/内存/磁盘），服务端落库展示。
