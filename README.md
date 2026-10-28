# backup

MySQL 从库备份平台：`backup-server`（中心 Web 平台）与 `backup-agent`（部署在每台备份服务器）。

需求见 [docs/prd.md](docs/prd.md)。

## backup-agent

在每台备份服务器上，对 podman 容器中的 Percona Server 从库执行 xtrabackup 流式全量 / 增量备份，经 zstd 压缩并计算 sha256。同一台主机同一时刻只运行一个备份任务。

当前（P1）可以只靠本地配置独立运行；接入 backup-server 在 P2 实现。

### 前置条件

- Ubuntu 24.04，podman 4.x，宿主机安装 `zstd`（`apt install zstd`）
- 每个从库容器内有 xtrabackup 2.4、mysql 客户端，并把宿主机的备份根目录（默认 `/data/backup`）挂载到容器内的**相同路径**
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

### 备份目录

```
<backup_root>/<项目>/<YYYYMMDD-HHMM>-chain/
    00-full/  backup.xbstream.zst  xtrabackup_checkpoints  xtrabackup_info  xtrabackup_slave_info  backup.log  meta.json
    01-incr/  ...
```

## 开发

需要 Go 1.27+ 和 zstd。

```bash
make test    # go test -race ./...
make vet
make build   # 输出到 bin/，默认 linux/amd64
```

## 开发流程

PRD → GitHub Issue → PR：每个 Issue 一个分支（`feat/<issue>-<name>`），PR 描述写 `Closes #<issue>`，CI 通过后 squash 合并。
