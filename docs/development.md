# 开发文档

本文档面向 1mate 的开发者与二次开发贡献者：整体架构、模块职责、关键设计决策、开发环境搭建、测试与调试方法。

> 部署运维见 `docs/deployment.md`；日常运维见 `docs/runbook.md`；API 定义见 `docs/openapi.yaml`。

---

## 1. 总体架构

1mate 是一个 **控制面（control plane）** 服务：自身为 Go 单二进制（内嵌 React 前端），通过 **agentless SSH** 管理任意数量的 Linux 主机上的 TeslaMate Docker Compose 栈。目标主机上不安装任何 1mate 组件。

```
┌─────────────────────────── 1mate 单二进制 ───────────────────────────┐
│                                                                      │
│  api/ ──▶ service/ ──▶ sshx/ ──SSH──▶ 目标主机（docker compose ...） │
│   ▲         │    │                                                    │
│   │         │    └──▶ cryptox/ （凭据/密钥加解密）                     │
│  webui      │                                                         │
│ (go:embed)  └──▶ store/ ──SQLite──▶ 1mate.db                         │
│                                                                      │
│  job/ （持久任务队列，service 的动作全部经它调度）                     │
│  probe/ provision/ compose/ （探测脚本/安装脚本/compose 模板生成器）  │
└──────────────────────────────────────────────────────────────────────┘
```

**请求流程（以部署实例为例）**：
1. `POST /api/v1/instances/{id}/actions/deploy` → `api/handlers.go` 校验会话 + CSRF，投递任务；
2. `job/manager.go` 持久化 job 记录（SQLite），获取 per-instance 互斥锁与全局 worker 信号量；
3. `service/lifecycle.go` `RunDeployJob` 编排：渲染 compose 文件 → 上传 → `config -q` 校验 → `up -d` → 失败回滚旧文件；
4. 全程日志经 `redact` 脱敏后写入 `job.LogSink`（文件 + SSE 双写），前端实时显示。

---

## 2. 模块职责

| 模块 | 职责 | 关键文件 |
|---|---|---|
| `cmd/1mate` | 入口：装配 config/store/cryptox/auth/job/service/api，注册全部任务处理器，优雅停机 | `main.go` |
| `internal/config` | 环境变量解析（`ONEMATE_*`），非回环监听告警 | `config.go` |
| `internal/store` | SQLite（WAL）+ 嵌入式迁移；**单写连接**避免 SQLITE_BUSY；10 张表 | `store.go`、`migrations/0001_init.sql` |
| `internal/model` | 领域模型与枚举：`ComposeKind`（none/plugin_v2/standalone_v2/standalone_v1）、状态机、任务种类 | `model.go` |
| `internal/cryptox` | AES-256-GCM 密封箱，AAD 绑定 `表:列:行ID`；argon2id 密码哈希；随机 token | `cryptox.go` |
| `internal/auth` | 会话管理（HttpOnly Cookie + CSRF 头）、登录限流（5 次/分/IP）、改密（改后吊销全部会话） | `session.go`、`password.go` |
| `internal/sshx` | SSH 客户端抽象：`Client` 接口 + 真实 Dialer + 测试 Fake；TOFU 指纹固定；sudo 密码走 stdin | `client.go`、`sshx.go` |
| `internal/probe` | 只读探测脚本（输出 `IM_*=` 行）+ 解析器 → `Capabilities`；主机状态判定 | `probe.go` |
| `internal/provision` | 幂等 Docker/Compose 安装脚本生成器（repo/compose_only/script 策略，镜像源，版本固定） | `provision.go` |
| `internal/compose` | compose v1/v2 模板渲染、`.env` 生成、secret 脱敏预览、slug 校验、命令选择 | `compose.go` |
| `internal/job` | 持久任务队列：SQLite 记录、per-host/per-instance 互斥、全局信号量、SSE 日志流、崩溃恢复 | `manager.go`、`stream.go` |
| `internal/service` | 核心编排：主机/实例/备份 CRUD 与生命周期任务；错误分类（SSH 错误→用户可读码） | `hosts.go`、`instances.go`、`lifecycle.go`、`backups.go` |
| `internal/api` | HTTP 路由（net/http PathValue）、SSE、SPA 静态服务、安全响应头 | `router.go`、`handlers.go` |
| `internal/redact` | 统一日志/API 脱敏器 | `redact.go` |
| `internal/webui` | `go:embed` 前端产物；未构建时返回 nil（纯 API 模式） | `webui.go` |

---

## 3. 关键设计决策

### 3.1 加密与主密钥
- 所有敏感数据（主机 SSH 凭据、实例 secrets）经 `cryptox.Box` 加密存储，AAD 绑定 `表名:列:行ID`，防串用。
- 主密钥来源：`ONEMATE_MASTER_KEY` env（优先）或数据目录 `master.key` 文件（0600，自动生成）。**两种来源不可混用切换**。
- ⚠️ **历史教训（v1.0 前修复的核心 bug）**：`Service.GetInstance` 返回的是**解密后的明文** secrets。任何"读出来改一下再存回去"的代码路径都会把明文覆盖回数据库。正确做法：从 `Store.GetInstance`（密文）加载、原地改字段、保存，再 `LoadInstanceSecrets` 解密返回。见 `lifecycle.go`（部署后更新 SHA）与 `instances.go`（UpdateInstance）。

### 3.2 SSH 执行模型
- 命令经用户登录 shell 执行：**不要假设 bash**（实测遇到过 root shell 是 zsh，`sh -c` 包裹 + 单引号转义 `'\''` 会被 zsh 拒绝）。
- 多行脚本（探测/安装）通过 Upload 到 `/tmp/1mate-probe-<随机>.sh` 后 `sh <file>` 执行，避免转义问题。
- sudo 密码只经 stdin 传递，绝不进命令行参数与日志。
- 主机指纹 TOFU 固定：首次连接记录，后续变更（`ErrHostKeyChanged`）必须人工重新信任。

### 3.3 任务队列
- 任务记录持久化到 `jobs` 表，平台崩溃重启后将 running 任务标记 `interrupted`。
- 互斥粒度：per-host + per-instance，防止同一主机/实例并发操作；全局 worker 上限 `ONEMATE_WORKERS`（默认 4）。
- 日志：`LogSink` 脱敏后双写（`<data>/jobs/<id>.log` 文件 + SSE stream），单文件 20MB 截断。

### 3.4 compose 方言兼容
- 主机探测锁定 Compose 种类与版本；实例创建时锁定 `compose_major`（1 或 2）。
- v2 用 `name:` 语法 + `docker compose` 命令；v1 用 `version:` 语法 + `docker-compose` 命令。
- 导入已有实例时 compose 文件名兼容 `compose.yaml` / `compose.yml` / `docker-compose.yml`。

### 3.5 错误分类
- SSH 层错误经 `service.classifySSHError` 归类为用户可读码（`host_unreachable` / `auth_failed` 等），关键词匹配支持中英文。

---

## 4. 数据模型

SQLite 迁移文件：`internal/store/migrations/0001_init.sql`，共 10 张表：

| 表 | 用途 |
|---|---|
| `users` | 管理员账户（argon2id） |
| `sessions` | 会话（token 哈希存储） |
| `credentials` | 主机 SSH 凭据（密文） |
| `hosts` | 主机（地址、sudo 模式、指纹、状态） |
| `host_capabilities` | 探测结果（OS/Docker/Compose/端口占用） |
| `instances` | 实例（配置 + secrets 密文列 + `compose_major` 锁定） |
| `jobs` | 任务记录（种类、状态、日志文件、错误） |
| `backups` | 备份记录（文件路径、sha256、大小） |
| `audit_logs` | 审计（登录、改密、密钥查看、安装等） |
| `settings` | 平台设置（require_compose_v2 等） |

迁移机制：启动时按序执行，`CREATE TABLE IF NOT EXISTS` 幂等；新增迁移追加 `0002_*.sql` 并保持已发布库兼容。

---

## 5. 开发环境

### 5.1 依赖

- Go ≥ 1.24（`go.mod`）
- Node.js ≥ 22 + npm（前端）
- Windows 下交叉编译注意：PowerShell 不认 `set VAR=x&&...`，用 `cmd /c "set CGO_ENABLED=0&&set GOOS=linux&&set GOARCH=amd64&&go build ..."`，否则产出本机平台二进制（曾因此部署 `203/EXEC`）。

### 5.2 启动开发环境

```sh
make web          # 构建前端 → internal/webui/dist（go:embed 需要）
make dev-api      # 后端 http://127.0.0.1:8080
make dev-web      # 可选：Vite 热更新，代理 /api → :8080
```

首次访问 Web UI 引导创建管理员。

### 5.3 常用命令

```sh
make build        # 完整构建 bin/1mate
make test         # Go + 前端全部测试
make vet          # go vet
make lint         # vet + 前端 eslint
```

---

## 6. 测试

| 层 | 位置 | 覆盖 |
|---|---|---|
| Go 单元测试 | `internal/*/`*_test.go` | auth/compose/cryptox/probe/provision/redact/service（service 用 sshx.Fake 模拟远端） |
| 前端 | `web/src/**/*.test.ts(x)`（vitest） | 工具函数、StatusTags 组件 |

```sh
make test-go                       # go test ./...
cd web && npm run test -- --run    # vitest
```

**service 测试约定**：远端行为用 `sshx.Fake` 的 `Handler` 脚本化（按命令返回 stdout/exit code）。例如 `setProbeOutput` 匹配命令中的 `1mate-probe-` 路径返回探测输出。新增 service 功能时优先在此层测试，不需要真实主机。

**真实主机 E2E**（人工）：参照 `docs/deployment.md` 部署到测试机，走一遍 创建实例 → compose 预览 → 启停 → 备份 → 销毁。

---

## 6.1 版本管理与打 Tag

### 6.1.1 版本号规则（语义化版本 SemVer）

格式 `vMAJOR.MINOR.PATCH`：

| 位 | 何时递增 | 示例 |
|---|---|---|
| MAJOR | 不兼容的 API/数据模型变更（如迁移破坏旧库、接口删除改名） | v1.0.0 → v2.0.0 |
| MINOR | 向后兼容的新功能 | v1.0.0 → v1.1.0 |
| PATCH | 向后兼容的缺陷修复 | v1.1.0 → v1.1.1 |

预发布版：`v1.1.0-rc.1` / `v1.1.0-beta.1`（正式版发出前验证用）。

### 6.1.2 打 Tag 流程

```sh
# 0. 前置确认：工作区干净、测试全绿
git status --short                 # 应无未提交变更
make test                          # Go + 前端全部测试
make vet

# 1. 更新 CHANGELOG.md（若有）：把 "未发布" 段落改为本次版本号与日期

# 2. 打 annotated tag（annotated 而非 lightweight：带作者/日期/说明，release 语义）
git tag -a v1.0.0 -m "1mate v1.0.0: 首个正式版本"

# 3. 推送 tag（注意：push 默认不带 tag，必须显式推）
git push origin master
git push origin v1.0.0             # 或 git push origin --tags（谨慎：会把所有本地 tag 推上去）

# 4. 删除 tag（打错了）
git tag -d v1.0.0                  # 删本地
git push origin :refs/tags/v1.0.0  # 删远端
```

**打 tag 后马上验证版本注入**（tag 名会经 `git describe` 进入二进制）：

```sh
make backend                       # 构建，VERSION 自动 = git describe --tags --always --dirty
./bin/1mate &                      # 启动
curl -fsS http://127.0.0.1:8080/api/v1/health   # "version":"v1.0.0" 而非 "dev"
```

> `git describe` 规则：从 HEAD 向上找最近的 tag——HEAD 恰好在 tag 上 → `v1.0.0`；在 tag 后有 N 个提交 → `v1.0.0-N-g<hash>`；工作区有未提交变更 → 追加 `-dirty`。所以 health 端点显示的版本号可以精确定位构建来源。

### 6.1.3 版本注入机制

`Makefile` 通过 `-ldflags -X` 在**编译期**把版本写进 `internal/version` 包的变量：

```make
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X 1mate/internal/version.Version=$(VERSION) \
	-X 1mate/internal/version.Commit=$(COMMIT) \
	-X 1mate/internal/version.Date=$(DATE)
```

`internal/version/version.go` 里是三个默认值 `dev` / `none` / `unknown`——直接 `go build` 不带 ldflags 时 health 显示 `dev`，便于识别"非正式构建"。

**原则：版本号只在编译期确定，运行时不可变。** 不要在代码里写死版本，一切以 `internal/version` 为单一来源。

### 6.1.4 Docker 镜像 tag

```sh
docker build -f deploy/Dockerfile -t 1mate:v1.0.0 -t 1mate:latest \
  --build-arg VERSION=v1.0.0 --build-arg COMMIT=$(git rev-parse --short HEAD) .
```

`--build-arg VERSION` 必须显式传（Dockerfile 内 `ARG VERSION=dev`），否则镜像里也是 `dev`。

---

## 6.2 跨平台编译

### 6.2.1 支持矩阵

| 目标平台 | GOOS/GOARCH | CGO | 用途 |
|---|---|---|---|
| Linux amd64 | `linux/amd64` | 0 | 绝大多数服务器（主力目标） |
| Linux arm64 | `linux/arm64` | 0 | ARM 服务器 / 树莓派 5 / Apple Silicon 虚拟机 |
| macOS (开发) | `darwin/arm64` `darwin/amd64` | 0 | 本地开发调试 |
| Windows (开发) | `windows/amd64` | 0 | 本地开发调试 |
| ~~linux/armv7~~ | — | — | **不支持**：TeslaMate 官方镜像已放弃 32 位 ARM，probe 会直接拒绝 |

1mate 是纯 Go + `CGO_ENABLED=0`（SQLite 用纯 Go 驱动），可以无限制交叉编译，产物是**静态二进制**，任何同架构 Linux 上直接跑，无运行时依赖。

### 6.2.2 Linux/macOS 下交叉编译

```sh
# Linux amd64（发布主力产物）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w -X 1mate/internal/version.Version=$(git describe --tags --always)" \
  -o bin/1mate-linux-amd64 ./cmd/1mate

# Linux arm64
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -ldflags "-s -w -X 1mate/internal/version.Version=$(git describe --tags --always)" \
  -o bin/1mate-linux-arm64 ./cmd/1mate

# 一条命令构建两个（Go ≥1.15 支持 GOARCH 列表不适用 build，用循环）
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch \
    go build -trimpath -ldflags "-s -w" -o bin/1mate-linux-$arch ./cmd/1mate
done
```

ldflags 说明：`-s -w` 去符号表（体积减 ~30%）；`-trimpath` 去掉本机路径（可复现构建 + 不泄露构建机目录）。

### 6.2.3 Windows 下交叉编译（⚠️ 三个经典坑）

**坑 1：PowerShell 临时环境变量语法失效。** `$env:GOOS="linux"; go build ...` 在某些调用链下不生效，产物是 Windows PE。**推荐用 cmd 包一层**：

```powershell
cmd /c "set CGO_ENABLED=0&&set GOOS=linux&&set GOARCH=amd64&&go build -trimpath -ldflags `"-s -w`" -o bin/1mate-linux ./cmd/1mate"
```

**坑 2：忘了验证产物。** 编译完必须确认 ELF 头（Windows PE 装到 Linux 服务器会 `systemd 203/EXEC` 起不来）：

```powershell
Get-Content bin/1mate-linux -TotalCount 1 -Encoding Byte -AsByteStream |
  Select-Object -First 4 | ForEach-Object { $_.ToString("X2") }
# 正确输出：7F 45 4C 46  (即 \x7fELF)
```

（历史教训：本项目曾因坑 1 部署 Windows PE 到服务器导致 `203/EXEC`。）

**坑 3：`&&` 在 PowerShell 5.x 里不是命令连接符。** 上面命令整体包在 `cmd /c "..."` 里所以没问题；拆开写就要改用 `;`。

### 6.2.4 验证产物清单

发版前对每个产物检查：

```sh
file bin/1mate-linux-amd64
# 期望: ELF 64-bit LSB executable, x86-64, statically linked, Go BuildID=..., not stripped
ls -lh bin/           # 静态单文件，约 19-20MB（含内嵌前端）
./bin/1mate-linux-amd64 &   # 在对应平台试跑
curl -fsS http://127.0.0.1:8080/api/v1/health   # 版本号正确
```

### 6.2.5 发布产物命名建议

```
1mate-v1.0.0-linux-amd64.tar.gz     # tar 包内含二进制 + README + LICENSE
1mate-v1.0.0-linux-arm64.tar.gz
1mate_v1.0.0_checksums.txt          # sha256sum 供下载方校验
```

打包示例：

```sh
VERSION=v1.0.0
tar -czf 1mate-$VERSION-linux-amd64.tar.gz -C bin 1mate-linux-amd64
sha256sum 1mate-$VERSION-*.tar.gz > 1mate_$VERSION_checksums.txt
```

---

## 6.3 发版检查清单（Release Checklist）

- [ ] `git status` 干净，所有变更已提交
- [ ] `make test` 全绿（Go + 前端）
- [ ] `make vet` 无告警
- [ ] CHANGELOG.md 已更新本版本条目
- [ ] 真实主机 E2E 通过（创建→部署→启停→备份→销毁，参照 deployment.md）
- [ ] 打 annotated tag，推送
- [ ] `make backend` 后 health 端点显示新版本号（非 `dev`）
- [ ] 跨平台产物构建并通过 `file` 验证（ELF 头）
- [ ] Docker 镜像 `--build-arg VERSION` 正确传入
- [ ] GitHub Release（若有）：附产物 + checksums + CHANGELOG 摘录

---

## 7. 添加功能的典型路径

以"给实例动作加一种"为例：

1. `internal/model/model.go`：确认/扩展动作枚举与 `AllJobKinds`；
2. `internal/service/lifecycle.go`：实现 `Run<动作>Job`（经 `job.Manager.Submit`）；
3. `internal/api/handlers.go` `handleInstanceAction`：加 case（需要新参数就扩展 `actionReq`）；
4. `internal/api/router.go`：注册路由（如需新端点）；
5. `docs/openapi.yaml`：补接口定义；
6. `web/src/api/types.ts` + `hooks.ts`：类型与 mutation；
7. `web/src/pages/*`：UI；
8. `go test` + `tsc -b && vite build`；
9. 交叉编译部署到测试机验证（见 `docs/deployment.md` 第 8 节）。

---

## 8. 调试技巧

- **平台起不来**：`journalctl -u 1mate -n 100`；本地 `go run ./cmd/1mate` 看启动日志。
- **任务失败**：任务中心看 SSE 日志，或直接读 `<data>/jobs/<job_id>.log`（已脱敏）。
- **解密报错**：先确认主密钥来源一致（env vs master.key 文件），再确认记录是否被旧版本代码写入明文（长度 32/24 而非 80/72 的 base64 是明文特征）。
- **远端命令异常**：临时在 fake 不可覆盖处加真实 SSH 测试——`ssh <host> 'echo $SHELL'` 确认登录 shell；`cat -A /tmp/1mate-probe-*.sh` 看上传内容是否被转义破坏。
- **前端改动不生效**：`make web` 后需重新 `go build`（前端是编译期内嵌，非运行时读盘）。

---

## 9. 代码规范

- 注释、文档、提交信息当前约定使用中文（commit message 用 conventional commits 前缀 + 中文描述亦可，历史混用）。
- 错误返回统一 `fmt.Errorf("%w: ...", ErrValidation)` 等哨兵错误包装，由 `api/respond.go` 映射为 `{code, message}`。
- 新增用户输入一律先 `Normalize()` 再 `Validate()`；shell 参数走白名单 + `sshx.Quote`。
- 敏感值（密码、密钥、token）绝不进日志：写日志/API 输出前过 `redact`。
