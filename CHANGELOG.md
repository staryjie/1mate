# 更新日志（Changelog）

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 格式，
版本号遵循 [语义化版本（SemVer）](https://semver.org/lang/zh-CN/)。

## [未发布]

## [v1.0.0] - 2025-11-23

正式发布版本：基于 v1.0.0-rc.1，修复 WebSocket 终端升级失败、镜像同步面板状态不同步、终端断开后空白页等问题；完善 API 文档与开发文档，已在真实 Linux 主机（Debian 13 + fnos）完成完整端到端验证。

### 新增
- **WebSocket 终端**：主机详情页「终端」Tab，浏览器端 SSH PTY 连接，支持断开 / 重新连接，会话异常关闭日志。
- **镜像同步**：主机间流式 `docker save | docker load`，镜像预检（`precheck`），`syncedImages` 持久化到 `localStorage` 页面刷新不丢失，缺失计算基于合并结果。
- **任务重试**：`POST /jobs/{id}/retry`，任务详情页与列表页均支持一键重试。
- **导入只读接管**：导入的实例不再重写远程 compose/.env，仅执行 `docker compose up -d`，保留原始配置。
- **文档**：`docs/deployment.md`、`docs/development.md` 增加版本 tag 打标、跨平台编译、发布清单；`openapi.yaml` 补充 WebSocket terminal 端点及 `change-password`、`import` 端点。

### 修复
- WebSocket 升级失败：`statusWriter` 未实现 `http.Hijacker`，现已补全，终端可正常建立连接。
- 终端断开后页面空白、无法重新连接：增加 `wasConnectedRef`，断开后显示「重新连接」按钮。
- 镜像同步面板刷新后空白：`syncedImages` 持久化；`missing` 基于 `mergedResults` 计算，已同步镜像不再显示为缺失。
- WebSocket CheckOrigin 放宽为任意来源（会话 Cookie 仍做鉴权）。
- 导入实例 secrets 被明文覆盖回数据库的严重缺陷。
- 探测脚本输出丢失导致主机误判 unsupported。
- root 登录 shell 为 zsh 时单引号转义破坏远端命令。
- 备份 / 恢复误用 DB 用户名当作数据库名。
- 升级防护空操作（新旧 PG 版本同字段比较）。

### 已知限制
- 单管理员设计（多用户为后续规划）。
- 不内建 TLS，公网暴露需走反向代理（见部署文档）。
- 不支持 linux/armv7（TeslaMate 官方镜像已放弃 32 位 ARM）。

[v1.0.0]: https://github.com/staryjie/1mate/releases/tag/v1.0.0
[v1.0.0-rc.1]: https://github.com/staryjie/1mate/releases/tag/v1.0.0-rc.1
