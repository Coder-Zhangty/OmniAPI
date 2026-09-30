# 备份与恢复

## 创建备份

```sh
python scripts/gateway.py backup
```

该操作先优雅停止 Nginx：停止接受新连接，并等待已有请求最多 620 秒结束。若代理退出非零，拒绝产出“已完成”备份。
随后停止 New API 写入，通过容器内 pg_dump 导出 PostgreSQL，通过 docker cp 从停止的应用容器导出 `/data`，复制 Compose、Nginx、密钥和镜像锁。
finally 阶段重新启动服务；失败时必须检查恢复后的健康状态。

目录 `backups/YYYY-MM-DD_HH-MM-SS/` 包含：

- `database.dump`：PostgreSQL 自定义格式，含用户、渠道、Token、额度、日志及配置。
- `data.tar`：应用持久化数据。
- `bundle/`：部署配置、环境秘密、恢复所需本地凭据与可选证书。
- `manifest.json`：版本、时间和所有文件 SHA-256；没有 manifest 的备份视为未完成。

Redis 缓存可由数据库重建，恢复时使用全新 Redis 卷，不恢复旧缓存。Session 会话可能需要重新登录。
备份包含真实凭据；目录限制当前用户访问，不进 Git。当前是本地受限明文备份，离机存储前应加密；不要上传到公开网盘。
校验和用于发现损坏，不是抵御恶意篡改的数字签名；只恢复可信来源的备份。

## 隔离恢复

```sh
python scripts/gateway.py restore --backup backups/2026-09-25_22-00-00 --port 18080
```

先核验 manifest，再复制到 `runtime/gw-restore-时间戳/`，使用独立项目名、新数据库卷、新 Redis 卷和新应用卷。不会覆盖原项目或删除任何卷。
恢复检查 pg_restore 退出码、应用健康与管理员登录，并输出 `restore-result.json`。
完整数据一致性演练使用 `python scripts/acceptance.py --mock --recovery`，比较账户、额度、Token、渠道及日志计数，并测试恢复后的用户登录。
恢复程序不自动将真实上游请求发出去；恢复的真实渠道仍可能启用，请只在回环测试，避免重复流量。

## 保留与迁移

默认不自动删除备份或旧卷。删除前人工确认已完成恢复演练。跨机迁移复制整个可信备份目录及脚本，用相同锁定镜像恢复。
如启用了可选 HTTPS，运维恢复后先在 HTTP 回环入口核验，再手动启用 HTTPS overlay；证书恢复不代表公网入口已配置。
