# 更新日志（Changelog）

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 格式，
版本号遵循 [语义化版本（SemVer）](https://semver.org/lang/zh-CN/)。

## [未发布]

## [v1.0.0-rc.1] - 2025-11-21

首个候选发布版本：TeslaMate 多实例一键部署平台核心功能完备，已在真实 Linux 主机（Debian 13）完成端到端验证。

### 新增
- **主机管理**：SSH 凭据 AES-256-GCM 加密存储、TOFU 指纹固定、一键能力探测（发行版 / Docker / Compose / 端口占用）、状态判定与分类错误提示。
- **Docker / Compose 一键安装**：幂等安装脚本（repo / compose_only / script 三策略），始终优先 Compose v2，绝不安装或卸载 v1；支持阿里云 / Azure 中国镜像源与版本固定；执行需显式确认 `accept_system_changes`。
- **实例生命周期**：创建（4 步向导 + 端口双重预检）、部署（渲染 → 校验 → up → 失败回滚）、启动 / 停止 / 重启 / 对账 / 升级 / 销毁，全部经持久任务队列执行，日志 SSE 实时推流并脱敏落盘。
- **实例导入**：从主机上已有 TeslaMate Compose 目录导入纳管，兼容 compose.yaml / compose.yml / docker-compose.yml 与多种 env 变量名。
- **备份与恢复**：`pg_dump -Fc` 流式备份（sha256 校验）、恢复前自动安全备份、PostgreSQL 大版本变更硬拒绝。
- **账号**：首次访问初始化引导、登录限流（5 次/分/IP）、CSRF 防护、修改密码（改后吊销全部会话）。
- **Web 控制台**：总览、主机详情（能力面板 + 安装器）、实例列表 / 详情（启停按钮状态联动、复制链接、危险区）、任务中心（SSE 日志）、设置。
- **文档**：部署文档（`docs/deployment.md`）、运维手册（`docs/runbook.md`）、开发文档（`docs/development.md`）、API 定义（`docs/openapi.yaml`）。
- MIT 许可证。

### 修复
- 部署 / 更新实例后 secrets 被明文覆盖回数据库的严重缺陷（现从存储层加载密文修改保存）。
- 探测脚本输出丢失导致主机误判 unsupported（SSH stdout 未回传）。
- root 登录 shell 为 zsh 时单引号转义破坏远端命令（改为脚本文件执行 / 去除 sh -c 包裹）。
- 备份 / 恢复误用 DB 用户名当作数据库名。
- 升级防护空操作（新旧 PG 版本同字段比较）。
- 探测临时文件并发冲突（加随机后缀）。
- 实例运行命令忽略实例自身 compose_major 锁定。
- 镜像拉取超时不足（放宽至 60 分钟）。

### 已知限制
- 单管理员设计（多用户为后续规划）。
- 不内建 TLS，公网暴露需走反向代理（见部署文档）。
- 不支持 linux/armv7（TeslaMate 官方镜像已放弃 32 位 ARM）。

[v1.0.0-rc.1]: https://github.com/your-org/1mate/releases/tag/v1.0.0-rc.1
