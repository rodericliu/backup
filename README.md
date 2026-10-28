# backup

MySQL 从库备份平台：`backup-server`（中心 Web 平台）与 `backup-agent`（部署在每台备份服务器）。

需求见 [docs/prd.md](docs/prd.md)。

## backup-agent

在每台备份服务器上，对 podman 容器中的 Percona Server 从库执行 xtrabackup 流式全量 / 增量备份，在容器内经 zstd 压缩后写入备份目录。同一台主机同一时刻只运行一个备份任务。

可以只靠本地配置独立运行（`server_url` 留空），也可以接入 backup-server，由 server 下发项目配置。

### 前置条件

- Ubuntu 24.04，podman 4.x
- 每个从库容器内有 xtrabackup 2.4、mysql 客户端、`zstd`，并把宿主机的备份根目录（默认 `/data/backup`）挂载到容器内的**相同路径**
- 每个从库都已创建备份账号（PRD §10.4）；需要增量的从库开启 `innodb_track_changed_pages = ON`（PRD §10.5）

### 安装

```bash
make build                                    # bin/backup-agent（linux/amd64）
install -m 755 bin/backup-agent /usr/local/bin/
install -d -m 700 /etc/backup-agent
install -m 600 deploy/agent.yaml.example /etc/backup-agent/agent.yaml   # 编辑项目与密码
backup-agent check                            # 校验配置并打印未来 24h 排班
install -m 644 deploy/backup-agent.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now backup-agent
```

- 修改项目配置后执行 `systemctl reload backup-agent`（SIGHUP）即可生效；其他配置项需要重启。
- 日志：`/var/log/backup-agent/backup-agent-YYYY-MM-DD.log`（JSON，按天轮转，保留 30 天）。
- 本地状态：`/var/lib/backup-agent/cache.db`（SQLite）。

### 接入 backup-server

1. 在 Web 上生成注册 token，写入 `/etc/backup-agent/register.token`（0600）
2. 把 server 的 `ca.crt` 拷贝到 `/etc/backup-agent/tls/ca.crt`
3. 在 `agent.yaml` 中设置 `server_url: https://<server>:<agent_listen 端口>`，`systemctl restart backup-agent`
4. agent 注册后主机出现在 Web 上；在 Web 上添加项目，agent 在下一次心跳（≤15s）拉取配置

- 配置了 `server_url` 后，`agent.yaml` 中的 `projects` 被忽略；server 下发的配置缓存在本地 SQLite，server 不可用时按缓存继续备份，恢复后按顺序补传任务结果和事件
- 首次接入、尚未拉取到任何配置时 agent 不执行备份；从独立运行切换时，从重启到在 Web 上建好项目之间没有备份，建议在两次全量之间操作
- **项目名必须与独立运行时一致**：本地的备份链按项目名延续，增量基准和保留清理不受影响；接入前的历史任务会补传，并在建立同名项目时自动关联
- 主机被吊销后，在 Web 上生成新的注册 token 写入 `register.token`，agent 会自动重新注册（沿用原主机记录），无需重启

### 备份目录

```
<backup_root>/<项目>/<YYYYMMDD-HHMM>-chain/
    00-full/  backup.xbstream.zst  xtrabackup_checkpoints  xtrabackup_info  xtrabackup_slave_info  backup.log  meta.json
    01-incr/  ...
```

## backup-server

中心 Web 平台（P2 进行中）。需要一个由你创建的 PostgreSQL 库和账号，表结构在启动时自动迁移。

```bash
make build                                         # bin/backup-server
install -m 755 bin/backup-server /usr/local/bin/
useradd --system --no-create-home backup-server
install -d -m 750 -o root -g backup-server /etc/backup-server
backup-server cert init                            # /etc/backup-server/ca/ca.crt、ca.key（自签 CA）
backup-server cert issue -host backup-server.internal -host 10.20.0.10   # /etc/backup-server/tls/
backup-server keygen                               # /etc/backup-server/master.key，加密 MySQL 密码，务必备份
install -m 640 -g backup-server deploy/server.yaml.example /etc/backup-server/server.yaml   # 编辑数据库等
chown backup-server: /etc/backup-server/master.key /etc/backup-server/tls/server.key   # 保持 0600
backup-server check                                # 校验配置、证书与主密钥
install -m 644 deploy/backup-server.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now backup-server
```

- `listen` 提供 Web UI 与管理 API，`agent_listen` 提供 Agent API（可以只对备份服务器开放）；留空则共用 `listen`
- `GET /healthz` 在两个端口上都可用，数据库不可用时返回 503
- `ca.crt` 分发给每个 agent（`server_ca`），`ca.key` 不要离开 CA 目录
- 数据库密码也可以通过环境变量 `BACKUP_DB_PASSWORD` 提供

## 开发

需要 Go 1.27+ 和 zstd（测试中假的 podman 直接在本机执行容器内命令）。

backup-server 的存储测试需要 PostgreSQL：设置 `BACKUP_TEST_DATABASE_URL` 为一个可以建库的账号，每个测试会创建并删除独立的临时库；未设置时这些测试跳过。

```bash
export BACKUP_TEST_DATABASE_URL='postgres://localhost:5432/postgres?sslmode=disable'
```

```bash
make test    # go test -race ./...
make vet
make build   # 输出到 bin/，默认 linux/amd64
```

## 开发流程

PRD → GitHub Issue → PR：每个 Issue 一个分支（`feat/<issue>-<name>`），PR 描述写 `Closes #<issue>`，CI 通过后 squash 合并。
