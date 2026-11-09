# 运维手册（Runbook）

## 1. 平台自身

### 启动 / 升级
```sh
# 构建
make build
# 部署：替换二进制并重启
sudo systemctl restart 1mate
```

平台状态检查：
```sh
curl -fsS http://127.0.0.1:8080/api/v1/health
```

### 备份平台自身
```sh
sqlite3 /var/lib/1mate/1mate.db ".backup '/backup/1mate.db.bak'"
cp /var/lib/1mate/master.key /backup/master.key.bak   # 与库分离存放
cp -r /var/lib/1mate/backups /backup/1mate-backups    # 如未用独立备份盘
```

**主密钥丢失 = 平台内所有密铁不可解密**。恢复流程：换新主密钥 → 重新录入全部主机
凭据 → 重新部署实例（数据卷仍在主机上，`ENCRYPTION_KEY` 从审计或书面记录恢复）。

## 2. 主机生命周期

### 添加主机
1. Web 控制台 → 主机 → 添加主机。
2. 填写地址、SSH 用户与凭据；点击"测试连接"记录并核对指纹。
3. 保存后自动排队 `host_probe` 任务，能力面板等待刷新。

### 仅显示 "no_docker"
一键安装（推荐 `repo` 策略；国内网络加 `Aliyun` 镜像）。安装排错：

| 阶段（1MATE_ERROR） | 根因 | 处置 |
|---|---|---|
| `E_NO_PRIVILEGE` | 非 root 且无 sudo | 配置 sudo_mode 或改用 root 登录 |
| `E_SUDO_PASSWORD` | 密码缺失 | 表单补填 sudo 密码 |
| `E_UNSUPPORTED_DISTRO` | 发行版/EOL 不支持 | 按官方文档手工安装 Docker |
| `E_REPO` | 仓库不可达 | 换 `Aliyun` / `AzureChinaCloud` 镜像 |
| `E_INSTALL_ENGINE` | 包安装失败 | 看 job 日志具体 apt/dnf 输出 |
| `E_VERIFY` | 装完仍无 v2 | 检查 `docker compose version`、PATH |

### ssh / 认证失败
- `host_key_changed`：主机重装或中间人。人工核对指纹后在编辑主机里重新信任。
- docker socket 拒绝：编辑主机设置 `sudo_mode`（nopasswd / password），平台自动加
  sudo 前缀；或手工 `usermod -aG docker <user>` 后重连。

### 探测与真实状态对账
任何实例异常点击"对账"：`compose ps --format json` + 远端 compose 文件哈希比对，
文件被手改会提示 redeploy 收敛。

## 3. 实例生命周期

### 部署失败
任务日志位于 `<data>/jobs/<job_id>.log`（可在任务中心查看）。常见：
- 端口被占：平台预检 `ss -ltn`；换端口后重新部署。
- 磁盘不足：`df` 预检失败则清理后重试。
- `compose config` 报错：生成的文件与旧版镜像不兼容时回滚原文件，需升级引擎。

### 中断的任务
平台重启会标记 `interrupted`；操作实例前先"对账"确认远端状态。

### compose v1 主机
仍可部署（实例锁定 compose_major=1，使用旧语法 + `docker-compose` 命令），界面
黄色告警。建议：主机详情 → 安装 Compose v2 → 重新部署；此后自动切换 v2 命令。
设置页可开启"强制 Compose v2"拒绝 v1 硬阻断。

## 4. 备份与恢复

```sh
# 手动（近似于平台执行逻辑）
ssh <host> "cd /opt/1mate/instances/<slug> && \
  docker compose -p 1mate-<slug> exec -T database pg_dump -U teslamate -Fc teslamate_<slug>" \
  > teslamate.dump
# 恢复
docker compose -p 1mate-<slug> stop teslamate
docker compose -p 1mate-<slug> exec -T database \
  pg_restore --clean --if-exists --no-owner -U teslamate -d teslamate_<slug> < teslamate.dump
docker compose -p 1mate-<slug> up -d
```

恢复到全新实例时必须恢复相同 `ENCRYPTION_KEY`（实例详情 → 查看加密密钥）。

### PostgreSQL 大版本迁移（如 17 → 18）
平台拒绝在位升级。流程：
1. 旧实例：立即备份。
2. 新建实例（选目标 PG 大版本，新 slug）。
3. 新实例 UI → 上传备份 → 恢复。
4. 校验无误后在旧实例"危险区"销毁（选保留卷以留后路）。

## 5. 升级 TeslaMate
实例详情 → "备份并升级镜像"：先 `pg_dump`，再 `pull && up -d`。如需升级到特定
版本，先在"修改配置"改 `image_tag` 再执行。

## 6. 暴露到公网
平台自身和 TeslaMate 都不建议直接暴露：
- 平台：放 Nginx/Caddy/Traefik 后终止 TLS；开启 `ONEMATE_SECURE_COOKIE=true`。
- 实例：`bind_address=127.0.0.1`，由反代转发 dmimes：TeslaMate `:4000`、
  Grafana `:3000` 子路径 `/grafana`（见 docs 中的 Traefik 示例），并加访问控制。
- Tesla 令牌默认在实例 UI 内录入；平台不代持账户密码。

## 7. 事故响应清单
- 平台起不来：`journalctl -u 1mate -n 100`；检查 master.key 权限与磁盘。
- 密钥解密报错：主密钥与库不匹配；立即停止写入并恢复备份的 master.key。
- 任务一直 running：平台崩溃后遗留 → 重启即标记 interrupted。
- 大量主机 unreachable：排查平台出口网络（SSH 可达性），非主机故障。
