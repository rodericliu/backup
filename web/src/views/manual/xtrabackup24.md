本手册说明本平台如何使用 Percona XtraBackup 2.4 备份 MySQL 从库：一次备份经历了哪些步骤、文件如何压缩和存放、全量与链式增量的关系，以及如何从备份恢复。xtrabackup 本身的原理和命令参数请参考 [Percona XtraBackup 2.4 官方文档](https://docs.percona.com/percona-xtrabackup/2.4/index.html)。

> Percona XtraBackup 2.4 只支持 MySQL 5.7 及更早版本，官方已停止维护（EOL），文档为只读状态。

## 概览

- **backup-server**：保存项目配置、汇总任务结果，提供本 Web 界面。它不直接接触备份数据库。
- **backup-agent**：部署在每台备份服务器上。它在本地排班，用单个 worker **串行**执行备份（同一时间只跑一个任务），负责保留清理，并把心跳、进度和结果上报给 server。
- **从库容器**：xtrabackup、xbstream 和 zstd 都在从库的 podman 容器内执行。备份根目录（默认 `/data/backup`）以相同路径挂载进容器，所以容器内写出的文件可以直接落到宿主机的备份盘上。

一次备份的完整流程：

```text
预检查 → 写临时凭据 → 容器内 xtrabackup 流式备份 | zstd 压缩写入文件
      → 结果校验 → 提取元数据 → 收尾（恢复 SQL 线程 / 清理 bitmap / 删除凭据）→ 上报
```

## 备份流程

### 1. 预检查

以下任何一项不满足，任务都会直接失败，并记录对应的错误码：

| 检查项 | 条件 | 失败代码 |
|---|---|---|
| 容器状态 | 项目配置的容器存在且正在运行 | `CONTAINER_NOT_RUNNING` |
| 复制线程 | `Slave_IO_Running` 和 `Slave_SQL_Running` 都是 `Yes` | `REPLICATION_BROKEN` |
| 复制延迟 | `Seconds_Behind_Master` 小于项目的延迟阈值（默认 10 分钟） | `REPLICATION_LAG` |
| 磁盘空间 | 备份目录的可用空间 ≥ 预估大小 × 1.5 | `DISK_INSUFFICIENT` |

预估大小取该项目上一次同类备份（全量或增量）成功后的压缩大小；没有历史记录时，按 MySQL 数据目录的大小估算。

### 2. 临时凭据

MySQL 账号和密码不会出现在命令行参数里。每次执行前，agent 在 `<备份根目录>/.agent/` 下写一个权限为 `0600` 的临时 `my-*.cnf`，通过 `--defaults-extra-file` 传给 xtrabackup；任务结束（无论成功与否）后立即删除这个文件。

备份账号需要的权限见官方文档 [Connection and Privileges Needed](https://docs.percona.com/percona-xtrabackup/2.4/using_xtrabackup/privileges.html)。

### 3. 执行备份

agent 通过 `podman exec` 在容器内执行下面这条管道（为便于阅读做了简化）：

```bash
xtrabackup --defaults-extra-file=<临时 cnf> \
  --backup --stream=xbstream \
  --slave-info --safe-slave-backup \
  --parallel=<N> [--throttle=<N>] \
  --target-dir=<任务目录> --extra-lsndir=<任务目录> \
  [--incremental-basedir=<基准备份的任务目录>] \
| zstd -q -c -T<线程数> -<级别> > <任务目录>/backup.xbstream.zst
```

| 参数 | 本平台的用途 |
|---|---|
| `--stream=xbstream` | 不在磁盘上先落一份未压缩的备份，而是把数据以流的形式输出到 stdout，交给 zstd 压缩 |
| `--slave-info` | 记录从库对应的主库 binlog 文件和位置，恢复后用来重建复制 |
| `--safe-slave-backup` | 备份收尾时临时停止 SQL 线程，保证拿到一致的复制位点 |
| `--parallel` | 并行拷贝数据文件的线程数，在项目配置中设置（默认 4） |
| `--throttle` | 限制每秒的 IO 操作数，0 表示不限，在项目配置中设置 |
| `--extra-lsndir` | 额外把 `xtrabackup_checkpoints`、`xtrabackup_info` 写成普通文件，下一次增量会从这里读取基准 LSN |
| `--incremental-basedir` | 只在增量备份时使用，指向基准备份的任务目录（见下文“全量、增量与链”） |

其他细节：

- agent 配置 `low_io_priority: true`（默认开启）时，xtrabackup 和 zstd 都以 `ionice -c3` 和 `nice -n 19` 运行，尽量不影响从库上的业务。
- 每 5 秒上报一次进度，内容是**已写入的压缩后字节数**和写入速度，所以进度数字会明显小于数据库的实际大小。
- 超过项目的超时时间（默认 6 小时），或在页面上取消任务时，agent 会在容器内终止 xtrabackup，任务分别记为“超时”或“已取消”。

参数的完整说明见官方文档 [The xtrabackup Option Reference](https://docs.percona.com/percona-xtrabackup/2.4/xtrabackup_bin/xbk_option_reference.html)、[Throttling Backups](https://docs.percona.com/percona-xtrabackup/2.4/advanced/throttling_backups.html)。

### 4. 成功判定

以下条件**全部满足**，任务才算成功：

1. zstd 退出码为 0；
2. xtrabackup 退出码为 0；
3. xtrabackup 日志中包含 `completed OK!`；
4. `backup.xbstream.zst` 文件非空，并已刷新到磁盘。

### 5. 提取元数据

成功后，agent 从 `xtrabackup_checkpoints` 读取 `from_lsn` 和 `to_lsn`，从日志中解析主库 binlog 位置（以及 GTID），写入 `xtrabackup_slave_info`，并记录本次增量是否用上了 changed page bitmap。这些信息都会显示在任务详情中。

### 6. 收尾

无论成功与否，都会执行：

- **确认 SQL 线程在运行**：`--safe-slave-backup` 会停止 SQL 线程。如果备份中断导致它没有恢复，agent 会执行 `START SLAVE SQL_THREAD` 并记录日志。
- **删除临时凭据文件**。

只在成功时执行：

- **清理 bitmap**：执行 `PURGE CHANGED_PAGE_BITMAPS BEFORE <LSN>`。全量成功后用它自己的 `to_lsn`；增量成功后用本链全量的 `to_lsn`，保证本链后续的增量仍然能用 bitmap。

只在失败时执行：

- **删除不完整的产物**（数据文件、checkpoints、info、slave_info），保留 `backup.log` 和 `meta.json` 供排查。

### 7. 存储布局

```text
<备份根目录>/<项目>/<YYYYMMDD-HHMM>-chain/     # 一条链，以全量开始的时间命名
    00-full/                                   # 全量
        backup.xbstream.zst                    # 压缩后的备份数据流
        xtrabackup_checkpoints                 # 备份类型、from_lsn、to_lsn
        xtrabackup_info                        # 备份的详细信息
        xtrabackup_slave_info                  # 主库 binlog 位置（恢复后重建复制用）
        backup.log                             # xtrabackup 的完整日志
        meta.json                              # 任务 ID、类型、LSN、大小、时间、状态等
    01-incr/                                   # 第 1 份增量，文件同上
    02-incr/
    ...
```

各文件的含义见官方文档 [Index of files created by Percona XtraBackup](https://docs.percona.com/percona-xtrabackup/2.4/xtrabackup-files.html)。

## 压缩

本平台**不使用** xtrabackup 自带的 `--compress`（qpress），而是把 xbstream 流通过管道交给 zstd：

- **只写一次磁盘**：数据从 xtrabackup 流出后直接压缩写入目标文件，没有“先落盘、再压缩”的中间文件，备份盘的写入量和占用空间都更小。
- **压缩比和速度更好**：zstd 在相近的 CPU 开销下，压缩比明显高于 qpress。
- **恢复方式简单**：`zstd -dc backup.xbstream.zst | xbstream -x -C <目录>` 一步就能解压出可以 prepare 的备份目录，不需要额外的 `--decompress`。

压缩参数在 agent 配置文件中设置，对这台主机上的所有项目生效：

| 配置项 | 默认值 | 说明 |
|---|---|---|
| `zstd.threads` | `8` | zstd 压缩线程数（`-T`） |
| `zstd.level` | `3` | 压缩级别（1–19），越高压缩比越大，CPU 开销也越大 |
| `low_io_priority` | `true` | 以低 IO 和低 CPU 优先级运行 xtrabackup 和 zstd |

zstd 在容器内执行，所以从库镜像中必须装有 `zstd`；找不到时任务会以 `COMPRESS_ERROR` 失败。

## 全量、增量与链

### LSN

InnoDB 用一个不断增长的日志序列号（LSN）标记数据页的每次修改。每份备份都会记录两个 LSN：

- `from_lsn`：从哪个 LSN 开始拷贝。全量为 0；增量等于基准备份的 `to_lsn`。
- `to_lsn`：备份结束时的检查点 LSN。

增量备份只拷贝 LSN 大于 `from_lsn` 的数据页，也就是基准之后发生过变化的页。

### 全量备份

全量备份拷贝整个数据目录，可以单独恢复。每次全量都会开启一条**新的链**，作为 `00-full` 存放。

### 链

一条链 = 一份全量 + 它后面的若干份增量。链是本平台组织、恢复和清理备份的基本单位：

- 全量的周期决定链的长度。例如“重要”项目默认每 4 小时全量、每 1 小时增量，一条链就是“1 份全量 + 最多 3 份增量”。
- 链中某份增量失败时，不会留下空洞：下一份增量会以链中**最后一份成功的备份**为基准。
- 全量失败时，这条链被标记为失败；在下一次全量成功之前，计划内的增量会被跳过（`NO_BASE`）。
- 项目还没有任何可用的全量时，计划内的增量会自动改为执行全量。

### 链式增量与差异增量

项目可以选择两种增量模式，**默认是链式**。以“00:00 全量，01:00、02:00、03:00 增量”为例：

| | 链式（chained，默认） | 差异（differential） |
|---|---|---|
| 01:00 增量的基准 | 00:00 全量 | 00:00 全量 |
| 02:00 增量的基准 | 01:00 增量 | 00:00 全量 |
| 03:00 增量的基准 | 02:00 增量 | 00:00 全量 |
| 每份增量包含 | 距上一份备份的变更页 | 从全量到当前的全部变更页 |
| 体积与每次 IO | 小且稳定 | 越往后越大 |
| 恢复到 03:00 | 全量 + 01 + 02 + 03 依次应用 | 全量 + 03 |
| 中间一份损坏 | 之后的时间点都无法恢复 | 只影响该时间点 |

本平台默认用链式，是因为每份增量的 IO 都小而稳定，符合“控制从库 IO”的首要目标；同时每条链的增量份数有限，恢复步骤不会太长。

在项目配置中切换增量模式后，**从下一条链开始生效**，当前链保持原来的模式。

增量备份的原理和命令见官方文档 [Incremental Backup](https://docs.percona.com/percona-xtrabackup/2.4/backup_scenarios/incremental_backup.html)。

### changed page bitmap

从库开启 Percona Server 的 `innodb_track_changed_pages` 后，xtrabackup 做增量时可以直接读取变更页的位图，不用扫描整个数据目录，IO 会小很多。

- 开启后要先完成一次全量，之后的增量才能用上 bitmap。
- 增量没用上 bitmap（日志中没有 `using the changed page bitmap`，或退回了全量扫描）时，任务状态为 **警告**。备份本身可用，但这次增量读了整个数据目录。
- 每次成功备份后，agent 会自动清理已经用不上的 bitmap 文件（见上文“收尾”）。bitmap 占用超过阈值（默认 10 GiB）时，agent 会记录告警。

### 保留与清理

清理按**整条链**进行，不会只删除链中的某一份：

- 链中最新一份成功备份的完成时间超过项目的保留天数（默认 7 天）时，删除整条链的目录。
- 以下链永远不会被删除：最新的一条链、最近一条全量成功的链、正在进行中的链。
- 每次备份成功后，agent 会检查一次是否需要清理。

## 恢复流程

> 任务历史页的“备份链”视图会列出所有可恢复的时间点。点击某份备份的 **恢复指引**，会根据这份备份所在的链、主机、容器和数据目录，生成可以直接复制执行的命令。本节解释这些命令每一步在做什么。

### 1. 确定要用哪几份备份

从目标备份开始，沿着每份备份记录的“基准任务”一直往回找，直到全量为止，然后按从旧到新的顺序排列：

- **链式**：恢复到 03:00 需要 `00-full`、`01-incr`、`02-incr`、`03-incr`，缺一不可；
- **差异**：恢复到 03:00 只需要 `00-full`、`03-incr`。

这个序列中的每一份备份都必须是“成功”或“警告”状态，否则这个时间点不可恢复。恢复后的数据与目标备份**完成时**的状态一致。

### 2. 准备工作

- 到 **主机** 页面暂停这台主机的调度。恢复期间从库容器会停止，计划备份会失败；恢复完成后再恢复调度。
- 确认备份盘有足够的空间：解压目录需要约等于数据库原始大小的空间。
- 下文命令中的 `<备份根目录>`、`<容器>`、`<镜像>`、`<宿主机数据目录>` 等，“恢复指引”会自动填好实际值。

### 3. 解压

在从库容器内（容器里有 `zstd` 和 2.4 版本的 `xbstream`）把每份备份解压到恢复目录：

```bash
R=<备份根目录>/restore/<项目>-<时间>
podman exec <容器> mkdir -p $R/full $R/incr1 $R/incr2
podman exec <容器> sh -c 'zstd -dc <链目录>/00-full/backup.xbstream.zst | xbstream -x -C '$R'/full'
podman exec <容器> sh -c 'zstd -dc <链目录>/01-incr/backup.xbstream.zst | xbstream -x -C '$R'/incr1'
podman exec <容器> sh -c 'zstd -dc <链目录>/02-incr/backup.xbstream.zst | xbstream -x -C '$R'/incr2'
```

### 4. prepare

prepare 会把备份期间拷贝的 redo log 应用到数据文件上，使数据达到一致状态。

**只有一份全量时：**

```bash
podman exec <容器> xtrabackup --prepare --target-dir=$R/full
```

**有增量时**，先对全量执行 prepare，再按顺序逐份应用增量：

```bash
podman exec <容器> xtrabackup --prepare --apply-log-only --target-dir=$R/full
podman exec <容器> xtrabackup --prepare --apply-log-only --target-dir=$R/full --incremental-dir=$R/incr1
podman exec <容器> xtrabackup --prepare --target-dir=$R/full --incremental-dir=$R/incr2
```

注意：

- 除**最后一份**增量外，每一步都要加 `--apply-log-only`。它表示只前滚已提交的事务、暂不回滚未提交的事务；如果提前回滚，后面的增量就无法再应用上去。
- 最后一份增量不加 `--apply-log-only`，这一步会完成回滚，得到可以启动的数据。
- 必须严格按顺序应用，不能跳过或调换。
- 所有增量都会合并进 `$R/full`，之后只需要用这个目录。

### 5. 替换数据目录

```bash
# 停止从库容器，把原数据目录改名保留，再建一个空目录
podman stop <容器>
mv <宿主机数据目录> <宿主机数据目录>.before-restore-<时间>
mkdir <宿主机数据目录>

# 用同一镜像的临时容器拷回数据并修正属主
podman run --rm --user 0 --entrypoint sh \
  -v <备份根目录>:<备份根目录> -v <宿主机数据目录>:/var/lib/mysql \
  <镜像> \
  -c 'xtrabackup --copy-back --target-dir='$R'/full --datadir=/var/lib/mysql && chown -R mysql:mysql /var/lib/mysql'

# 启动容器并检查日志
podman start <容器>
podman logs --tail 50 <容器>
```

- 原数据目录先**改名保留**，确认恢复无误后再删除，以便出问题时回退。
- 如果宿主机数据目录本身是一个挂载点，`mv` 会失败。这时改为把其中的内容移到别处或清空。
- 如果容器的挂载带 SELinux 标签（`:Z`），临时容器的 `-v` 参数也要加上相同的选项。
- 使用与从库相同的镜像来执行 `--copy-back`，可以保证 xtrabackup 版本和文件属主一致。

### 6. 重建复制

使用**目标备份**（序列中最后一份）记录的主库位点，也就是任务详情中的“主库位点”或 `xtrabackup_slave_info` 的内容：

```sql
-- podman exec -it <容器> mysql -uroot -p
CHANGE MASTER TO MASTER_HOST='<master>', MASTER_USER='<user>', MASTER_PASSWORD='<password>',
  MASTER_LOG_FILE='<binlog 文件>', MASTER_LOG_POS=<位置>;
START SLAVE;
SHOW SLAVE STATUS\G
```

主库开启了 GTID 时，也可以改用 `SET GLOBAL gtid_purged='<备份记录的 GTID>'`，再用 `MASTER_AUTO_POSITION=1` 建立复制。

### 7. 收尾

1. 观察 `SHOW SLAVE STATUS`，确认 IO 和 SQL 线程都是 `Yes`，并且延迟逐渐追平；
2. 抽样核对数据；
3. 删除恢复目录 `$R` 和改名保留的 `<宿主机数据目录>.before-restore-<时间>`；
4. 到 **主机** 页面恢复调度。

完整的恢复说明见官方文档 [The Backup Cycle - Full Backups](https://docs.percona.com/percona-xtrabackup/2.4/backup_scenarios/full_backup.html) 和 [The xbstream binary](https://docs.percona.com/percona-xtrabackup/2.4/xbstream/xbstream.html)。

## 常见失败原因

| 代码 | 含义 | 排查建议 |
|---|---|---|
| `CONTAINER_NOT_RUNNING` | 容器不存在或未运行 | 在主机上 `podman ps -a` 检查容器状态；确认项目配置的容器名正确 |
| `REPLICATION_BROKEN` | IO 或 SQL 线程未运行，或延迟为 NULL | `SHOW SLAVE STATUS\G` 查看 `Last_IO_Error` 和 `Last_SQL_Error`，先修复复制 |
| `REPLICATION_LAG` | 复制延迟超过阈值 | 等待从库追上；延迟经常偏高时，可以调大项目的延迟阈值或调整排班 |
| `DISK_INSUFFICIENT` | 备份盘可用空间不足 | 清理或扩容备份盘；检查保留天数是否过长 |
| `MYSQL_AUTH_FAILED` | 备份账号认证失败，或权限不足 | 核对项目中的账号密码和权限 |
| `XTRABACKUP_ERROR` | xtrabackup 执行失败 | 查看任务详情中的日志尾部，或任务目录下的 `backup.log` |
| `COMPRESS_ERROR` | zstd 或写文件失败 | 确认容器内有 `zstd`、备份根目录挂载进了容器且可写 |
| `TIMEOUT` | 超过项目的超时时间 | 检查是否有限速或 IO 争抢；必要时调大超时时间 |
| `NO_BASE` | 增量缺少可用的基准 | 本链全量失败或已被清理；等待或手动执行一次全量 |
| `INTERRUPTED` | agent 重启或异常退出导致中断 | 检查 agent 日志；下一个计划点会正常执行 |
| `CANCELED` | 被手动取消 | — |
| `SUPERSEDED` | 同一项目同类型的任务还在排队时又加入了新任务，旧任务被跳过 | 通常是前面的任务耗时太长，下一个计划点已经到了；检查排班是否过满 |

## 官方文档

- [Percona XtraBackup 2.4 文档首页](https://docs.percona.com/percona-xtrabackup/2.4/index.html)
- [How Percona XtraBackup Works](https://docs.percona.com/percona-xtrabackup/2.4/how_xtrabackup_works.html)
- [Percona XtraBackup User Manual](https://docs.percona.com/percona-xtrabackup/2.4/manual.html)
- [Frequently Asked Questions](https://docs.percona.com/percona-xtrabackup/2.4/faq.html)
