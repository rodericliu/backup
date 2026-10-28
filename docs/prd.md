# MySQL 从库备份平台 PRD

| 项 | 内容 |
|---|---|
| 产品名称 | MySQL 从库备份平台（backup-server / backup-agent） |
| 文档版本 | v1.0 |
| 日期 | 2026-10-28 |
| 状态 | 待评审 |

---

## 1. 背景

公司生产 MySQL 的备份依赖部署在专用备份服务器上的从库。从库以 podman 容器运行，每台备份服务器上有多个从库实例（Percona Server 5.6 / 5.7）。现状问题：

1. **没有定时备份**：备份服务器上没有 crontab / systemd timer，备份靠手工执行脚本。
2. **备份不压缩、无校验**：单次全量为未压缩目录（如 fetisch 717G），没有完整性校验和恢复验证。
3. **没有统一视图**：无法查看当前在备份什么、历史成功率、失败原因。
4. **IO 冲突风险**：多个大库同时备份会严重影响从库复制。
5. **多机扩展**：当前有 backup-esh-01，backup-inx-01 将重装后加入，未来还会新增备份服务器，需要统一管理。

### 1.1 现有环境（backup-esh-01）

| 项目（容器） | 版本 | 数据量 | 数据盘 | 端口 |
|---|---|---|---|---|
| fetisch_maindb_slave | Percona Server 5.6.51 | 718G | /nvme_data (NVMe) | 10.20.0.202:3307 |
| poppen_maindb_slave | Percona Server 5.6.51 | 690G | /nvme_data (NVMe) | 10.20.0.202:3306 |
| kmv2_message_slave | Percona Server 5.7.42 | 351G | /data (HDD) | 10.21.0.202:3307 |
| kmv2_maindb_slave | Percona Server 5.7.42 | 217G | /data (HDD) | 10.21.0.202:3306 |
| gays_maindb_slave | Percona Server 5.6.51 | 80G | /data (HDD) | 10.23.1.202:3306 |

- 主机：Ubuntu 24.04，32 核，251G 内存，podman 4.9.3。
- 容器内已有 xtrabackup 2.4.28、mysqldump、mysqlbinlog。
- 所有容器均挂载宿主机 `/data/backup` 到容器内同一路径。
- `/data` 91T（剩余 88T），`/nvme_data` 1.5T（**已用 95%**）。

---

## 2. 目标与非目标

### 2.1 目标

1. 基于 xtrabackup 的自动化物理备份：支持全量 + 增量，频率按项目可配置。
2. **同一台备份服务器同一时刻只运行一个备份任务**，并通过排班把各项目错开，均衡 IO。
3. 多台备份服务器由一个中心 Web 平台统一管理：增删项目、配置频率、查看状态与历史。
4. 完整记录每次备份的状态、耗时、大小、位点和失败原因。
5. 中心服务不可用时，各备份服务器仍按最后一次配置继续备份。
6. 每日向企业微信发送备份日报（第二阶段）。

### 2.2 非目标（本期不做）

- 异地复制 / 对象存储上传。
- 一键自动恢复（本期仅提供恢复命令指引和人工恢复验证流程）。
- 逻辑备份（mysqldump 全量导出）。
- 管理 MySQL 从库本身（建库、复制搭建、参数变更）。
- 多租户、细粒度 RBAC（本期只有管理员角色）。

---

## 3. 术语

| 术语 | 说明 |
|---|---|
| 主机（Host） | 一台运行 backup-agent 的备份服务器 |
| 项目（Project） | 一个需要备份的 MySQL 从库容器，归属于一台主机 |
| 任务（Job） | 一次具体的备份执行（全量或增量） |
| 链（Chain） | 一个全量及其后续增量组成的一组备份，恢复时以链为单位 |
| 全量（Full） | xtrabackup 完整物理备份 |
| 增量（Incremental） | 基于上一次备份 LSN 的 xtrabackup 增量备份 |
| 偏移量（Offset） | 项目在排班周期内的起始时间偏移，用于错开各项目 |
| Changed Page Bitmap | Percona Server 的 `innodb_track_changed_pages` 功能，使增量备份无需全盘扫描 |

---

## 4. 用户与场景

| 角色 | 场景 |
|---|---|
| DBA / 运维管理员 | 新增或删除需要备份的项目，调整频率与保留策略 |
| DBA / 运维管理员 | 查看当前备份进度、各主机负载与排班 |
| DBA / 运维管理员 | 排查失败任务：查看失败原因与日志 |
| DBA / 运维管理员 | 恢复时查找某时间点可用的备份链、主库位点和恢复命令 |
| DBA / 运维管理员 | 每天在企业微信查看备份日报 |

---

## 5. 总体架构

```
                ┌──────────────────────────────────┐
浏览器 ──HTTPS──>│ backup-server（独立主机）          │──> PostgreSQL（现有实例）
                │ Web UI / 管理 API / Agent API     │
                │ 排班预测 / 日报                    │
                └───────────────▲──────────────────┘
                     HTTPS + Agent Token（agent 主动连接）
         ┌──────────────────────┼──────────────────────┐
  backup-agent            backup-agent            backup-agent
  backup-esh-01           backup-inx-01           （未来新增）
  本地调度 + 串行执行      本地调度 + 串行执行
  podman exec xtrabackup  podman exec xtrabackup
  本地 SQLite 缓存         本地 SQLite 缓存
```

### 5.1 组件职责

| 组件 | 职责 |
|---|---|
| backup-server | Web UI；用户认证；主机、项目、任务数据管理；配置下发；接收 agent 上报；排班预测；命令下发；日报；审计 |
| backup-agent | 部署在每台备份服务器；拉取本机配置；本地排班；单 worker 串行执行备份；预检查；保留清理；bitmap 清理；上报心跳、进度、结果；离线缓存 |
| PostgreSQL | 中心数据存储，只有 backup-server 访问 |
| 本地 SQLite | agent 的配置缓存、待上报结果队列、链和 LSN 状态 |

### 5.2 关键设计原则

1. **只有 agent 主动连接 server**，备份服务器不开放入站端口。双方仍然双向通讯：agent 上报状态，server 通过长轮询下发命令（见 5.3）。
2. **调度在 agent 本地执行**，server 宕机不影响备份。
3. **互斥范围是主机**：同一主机串行，不同主机互不影响。
4. **单仓库、两个二进制**：`backup-server` 与 `backup-agent` 共享 API 类型定义，独立发布、独立升级，API 带版本号。

### 5.3 server 与 agent 的通讯

#### 5.3.1 通讯方式

所有请求都由 agent 发起：

| 方向 | 渠道 | 频率 | 内容 |
|---|---|---|---|
| agent → server | `POST /heartbeat` | 每 15s | 在线状态、磁盘、bitmap、容器列表、当前任务、队列、已生效的配置版本 |
| agent → server | `POST /jobs/{id}/events` | 状态变化时；运行中每 5s | 任务进入队列、开始、进度（已写入字节数、速度）、跳过 |
| agent → server | `PUT /jobs/{id}` | 任务结束时 | 最终结果、LSN、大小、失败原因 |
| server → agent | 心跳响应 | 每 15s | 最新配置版本号；版本号变化时 agent 调用 `GET /config` 拉取配置 |
| server → agent | `GET /commands?wait=30s` | 长轮询，持续挂起 | 立即备份、取消、上传日志、重新扫描容器、连接测试 |

#### 5.3.2 server 如何知道 agent 正在做什么

- server 上的主机状态、当前任务和队列，都是 agent **最近一次上报**的结果，不是实时查询。当前任务和队列最多延迟 15s，运行进度最多延迟 5s。
- 任务进入 agent 队列（`pending`）时就上报一次，所以排队中的任务也能在 Web 上看到。
- 心跳超时（默认 60s）后，server 不再知道 agent 的真实状态。Web 显示“离线”和最后已知状态，例如“离线（最后已知：正在全量备份 fetisch，已写入 320G）”，不能继续显示为“运行中”。
- agent 恢复连接后，按顺序补传离线期间的任务结果和事件（幂等），server 据此更新历史。

#### 5.3.3 是否可以执行由 agent 判断

- “同一主机同一时刻只运行一个备份”完全由 agent 保证（单 worker + 文件锁，见 S-1），**不依赖 server 的状态**。
- server 不判断 agent 当前能不能执行备份。Web 上点击“立即执行”时，server 只下发一条命令，agent 收到后把任务放入本机队列排队（见 P-8）。即使 server 看到的状态有几秒延迟，也不会产生并发备份。
- 取消命令到达时任务已结束，agent 在命令结果中返回“任务已结束”，不做任何操作。

#### 5.3.4 配置来源

| 阶段 | 项目配置来源 | `agent.yaml` 的作用 |
|---|---|---|
| P1（独立运行） | `agent.yaml` 中的 `projects` | 连接信息 + 项目配置 |
| P2 起（接入 server） | server 下发，缓存在本地 SQLite | 只保留连接、目录、压缩、日志等本机参数 |

- 配置了 `server_url` 时，agent 以 server 下发的配置为准，**忽略** `agent.yaml` 中的 `projects`，并在启动日志中提示。
- server 修改配置后 `config_version` 加 1。agent 通过心跳响应发现版本变化，拉取新配置并写入 SQLite，然后在下一次心跳中报告已生效的版本。Web 据此显示配置“已生效”或“等待生效”。
- server 不可用时，agent 使用 SQLite 中缓存的最后一份配置继续调度。首次启动且从未拉取过配置时，agent 不执行任何备份，持续重试连接。
- 配置变更不影响正在运行的任务，从下一个计划点开始生效。

---

## 6. 功能需求

### 6.1 主机管理（Host）

| 编号 | 需求 | 优先级 |
|---|---|---|
| H-1 | 管理员在 Web 上生成一次性注册 token（有效期可配置，默认 24h） | P0 |
| H-2 | agent 首次启动使用注册 token 换取长期 agent token，主机自动登记 | P0 |
| H-3 | 主机列表展示：名称、IP、agent 版本、在线状态、最后心跳、当前任务、磁盘使用率、bitmap 占用 | P0 |
| H-4 | 心跳超时（默认 60s）判定离线，Web 标红，并显示离线前最后已知的任务状态（见 5.3.2） | P0 |
| H-5 | 吊销主机 token；吊销后 agent 无法再上报或获取配置 | P0 |
| H-6 | 展示 agent 上报的本机容器列表（名称、镜像、状态、端口、挂载） | P0 |
| H-7 | 主机级配置：备份根目录、最大并发（固定为 1，不开放修改）、默认 throttle | P1 |

### 6.2 项目管理（Project）

| 编号 | 需求 | 优先级 |
|---|---|---|
| P-1 | 新增项目：选择主机，再从该主机上报的容器列表中选择容器 | P0 |
| P-2 | 选择等级模板：重要（4h 全量 + 1h 增量）/ 普通（24h 全量，无增量），模板参数可修改 | P0 |
| P-3 | 可配置参数：全量周期、增量周期（0 表示不做增量）、偏移量、增量模式（链式 / 差异）、保留天数、复制延迟阈值、xtrabackup `--parallel`、`--throttle`、超时、失败重试次数与间隔 | P0 |
| P-4 | 配置 MySQL 备份账号（用户名、密码），密码加密存储，Web 不回显 | P0 |
| P-5 | 新增项目时，server 根据历史耗时或数据量估算，推荐负载最低的偏移量 | P0 |
| P-6 | 启用 / 停用项目（停用后不再调度，已有备份保留） | P0 |
| P-7 | 删除项目：停止调度；可选“保留备份至保留期结束”或“立即删除备份文件”（需二次确认） | P0 |
| P-8 | 立即执行全量 / 增量（进入该主机的串行队列，不插队正在运行的任务） | P0 |
| P-9 | 保存前做连接测试：由 agent 用账号连接 MySQL、检查权限和复制状态 | P1 |
| P-10 | 项目配置修改后，配置版本号加 1，agent 在 30s 内生效；Web 显示配置“已生效 / 等待生效”（见 5.3.4） | P0 |

### 6.3 排班与调度

#### 6.3.1 规则

| 编号 | 需求 | 优先级 |
|---|---|---|
| S-1 | 每台主机**同一时刻只运行一个备份任务**：agent 单 worker 串行执行，并持有全局文件锁 `/run/backup-agent.lock` | P0 |
| S-2 | 全量计划时间 = 对齐到全量周期的时间点 + 偏移量；增量计划时间 = 全量周期内每个增量周期时间点 + 偏移量 | P0 |
| S-3 | 到期任务进入主机队列，按“计划时间 → 类型（全量优先）→ 项目优先级”排序 | P0 |
| S-4 | **合并**：同一项目的同类任务如果在队列中尚未开始、而下一次计划已到，则旧任务标记为“跳过：被新任务覆盖” | P0 |
| S-5 | 全量失败：本链后续增量标记为“跳过：无基准”；按配置自动重试全量（默认 1 次，间隔 10 分钟） | P0 |
| S-6 | 增量失败：下一次增量以最近一次成功的备份为基准 | P0 |
| S-7 | 增量开始时若该项目没有可用的成功全量，自动转为全量 | P0 |
| S-8 | agent 重启：运行中的任务标记“中断”，清理不完整文件；错过的计划不补跑，只执行下一个计划点 | P0 |
| S-9 | 支持全局或主机级“暂停调度”开关（运维维护时使用） | P1 |

#### 6.3.2 backup-esh-01 默认排班

4 小时一个循环，每小时只安排一个全量：

| 小时（h mod 4） | 全量（:00） | 增量（随后排队） |
|---|---|---|
| 0, 4, 8, 12, 16, 20 | fetisch | poppen → kmv2_maindb → gays |
| 1, 5, 9, 13, 17, 21 | poppen | fetisch → kmv2_maindb → gays |
| 2, 6, 10, 14, 18, 22 | kmv2_maindb | fetisch → poppen → gays |
| 3, 7, 11, 15, 19, 23 | gays | fetisch → poppen → kmv2_maindb |
| 每日 03:00（排在 gays 之后） | kmv2_message（普通） | — |

最终排班以 P0 阶段的实测耗时为准进行调整。

#### 6.3.3 排班预测视图

| 编号 | 需求 | 优先级 |
|---|---|---|
| S-10 | server 用与 agent 相同的排班算法 + 历史平均耗时，生成每台主机未来 24h 的预测甘特图 | P0 |
| S-11 | 某小时预计总耗时超过 60 分钟时标红提示“超负荷” | P0 |
| S-12 | 甘特图叠加显示实际执行情况（实际开始、结束、状态） | P1 |
| S-13 | 支持在甘特图中拖动调整项目偏移量 | P2 |

### 6.4 备份执行（agent）

#### 6.4.1 流程

```
预检查 → 准备临时凭据 → xtrabackup 流式备份 | zstd 压缩（同时计算 sha256）
→ 结果校验 → 提取元数据 → 收尾（恢复 SQL 线程 / 清理凭据 / bitmap 清理）→ 上报
```

#### 6.4.2 需求

| 编号 | 需求 | 优先级 |
|---|---|---|
| E-1 | 预检查：容器运行中 | P0 |
| E-2 | 预检查：`Slave_IO_Running` 与 `Slave_SQL_Running` 均为 Yes，`Seconds_Behind_Master` 小于阈值；不满足则任务失败并记录原因 | P0 |
| E-3 | 预检查：备份目标磁盘剩余空间 > 该项目上次同类备份大小 × 1.5（无历史时按数据目录大小估算） | P0 |
| E-4 | MySQL 凭据只在内存中持有；执行时写入容器可访问目录下的 600 权限临时 cnf，通过 `--defaults-extra-file` 传入，任务结束后删除；凭据不得出现在命令行参数中 | P0 |
| E-5 | 备份命令：`podman exec <容器> xtrabackup --backup --stream=xbstream --slave-info --safe-slave-backup --parallel=N [--throttle=N] --extra-lsndir=<元数据目录> [--incremental-basedir=<基准元数据目录>]`，stdout 经 `zstd -T<n> -<level>` 写入文件 | P0 |
| E-6 | 写入过程中计算 sha256；每 5 秒上报一次已写入字节数和速度 | P0 |
| E-7 | 成功判定：xtrabackup 退出码为 0，且日志包含 `completed OK!`，且产物文件非空 | P0 |
| E-8 | 提取并保存：from_lsn、to_lsn、主库 binlog 文件和位置（slave_info）、备份大小（压缩前 / 后）、耗时、是否使用了 changed page bitmap | P0 |
| E-9 | 收尾：确认 `Slave_SQL_Running = Yes`，若因备份中断而停止则执行 `START SLAVE SQL_THREAD` 并记录 | P0 |
| E-10 | 超时（按项目配置）后终止 xtrabackup，任务记为“超时”，清理不完整文件 | P0 |
| E-11 | 支持取消正在运行的任务（来自 Web 命令），行为同超时，状态记为“已取消” | P0 |
| E-12 | 增量备份未使用 bitmap 时，任务标记为“警告” | P1 |

#### 6.4.3 存储布局

```
<backup_root>/<项目>/<YYYYMMDD-HHMM>-chain/
    00-full/
        backup.xbstream.zst
        xtrabackup_checkpoints
        xtrabackup_info
        xtrabackup_slave_info
        backup.log
        meta.json            # 任务 ID、类型、LSN、sha256、大小、时间等
    01-incr/ ...
    02-incr/ ...
```

#### 6.4.4 保留与清理

| 编号 | 需求 | 优先级 |
|---|---|---|
| R-1 | 按**整条链**清理：链中最新一份备份超过保留天数（默认 7 天）时删除整条链 | P0 |
| R-2 | 永不删除当前链和最近一条完整成功的链 | P0 |
| R-3 | 清理动作记录到任务事件和审计日志 | P0 |
| R-4 | 备份成功后执行 `PURGE CHANGED_PAGE_BITMAPS BEFORE <LSN>`，LSN 取当前链全量的 to_lsn（保留至少一个全量周期的 bitmap） | P0 |
| R-5 | bitmap 占用超过阈值（默认 10GB）时告警 | P1 |

#### 6.4.5 失败原因分类

| 代码 | 说明 |
|---|---|
| `CONTAINER_NOT_RUNNING` | 容器未运行 |
| `REPLICATION_BROKEN` | IO 或 SQL 线程未运行 |
| `REPLICATION_LAG` | 复制延迟超过阈值 |
| `DISK_INSUFFICIENT` | 磁盘空间不足 |
| `MYSQL_AUTH_FAILED` | 备份账号认证或权限失败 |
| `XTRABACKUP_ERROR` | xtrabackup 执行失败（附日志最后 50 行） |
| `COMPRESS_ERROR` | zstd 或写文件失败 |
| `TIMEOUT` | 超时 |
| `NO_BASE` | 增量缺少可用基准 |
| `INTERRUPTED` | agent 重启或异常退出导致中断 |
| `CANCELED` | 被手动取消 |
| `SUPERSEDED` | 被新任务覆盖而跳过 |

#### 6.4.6 增量模式

以“00:00 全量，01:00、02:00、03:00 增量”为例：

| | 链式（chained） | 差异（differential） |
|---|---|---|
| 01:00 增量的基准 | 00:00 全量 | 00:00 全量 |
| 02:00 增量的基准 | 01:00 增量 | 00:00 全量 |
| 03:00 增量的基准 | 02:00 增量 | 00:00 全量 |
| 每份增量包含 | 最近 1 小时的变更页 | 从全量到当前的全部变更页 |
| 体积与每次 IO | 小且稳定 | 越往后越大 |
| 恢复到 03:00 | 全量 + 01 + 02 + 03 依次应用 | 全量 + 03 |
| 中间一份损坏 | 之后的时间点都无法恢复 | 只影响该时间点 |

- 实现方式：`--incremental-basedir` 指向上一份备份（链式）或本链全量（差异）的 `--extra-lsndir` 元数据目录。
- 默认链式：每小时 IO 平稳，符合“控制 IO”的首要目标；本方案每条链最多 3 份增量，恢复步骤有限。
- 项目可切换为差异模式；切换在下一条链开始时生效。

### 6.5 任务与历史

| 编号 | 需求 | 优先级 |
|---|---|---|
| J-1 | 任务状态：`pending`、`running`、`success`、`warning`、`failed`、`skipped`、`canceled` | P0 |
| J-2 | 历史列表：按主机、项目、类型、状态、时间范围筛选，分页 | P0 |
| J-3 | 任务详情：计划 / 开始 / 结束时间、耗时、压缩前后大小、速度、LSN、主库位点、链和基准任务、是否使用 bitmap、失败原因代码与摘要、日志尾部（200 行）、文件路径、sha256 | P0 |
| J-4 | 查看完整日志：server 下发“上传日志”命令，agent 上传后展示 | P1 |
| J-5 | 链视图：按项目展示所有链及每条链的全量和增量，标注可恢复的时间点 | P0 |
| J-6 | 恢复指引：根据所选备份点，生成对应的解压、`xbstream -x`、`--prepare`（含增量合并顺序）命令 | P1 |
| J-7 | 任务事件时间线：状态变化、进度采样、清理动作 | P1 |

### 6.6 仪表盘

| 编号 | 需求 | 优先级 |
|---|---|---|
| D-1 | 主机卡片：在线状态、当前运行任务（项目、类型、已写入、速度、已耗时）、队列中的下几个任务 | P0 |
| D-2 | 今日统计：成功、失败、跳过数；总备份量 | P0 |
| D-3 | 项目健康：每个项目最近一次成功备份时间；超过 2 个全量周期无成功备份的标红 | P0 |
| D-4 | 磁盘：各主机数据盘、备份盘使用率；超过 90% 标红 | P0 |
| D-5 | 运行中任务的进度通过 SSE 实时刷新 | P0 |
| D-6 | 最近失败列表（快速进入详情） | P0 |

### 6.7 用户与安全

| 编号 | 需求 | 优先级 |
|---|---|---|
| U-1 | 账号密码登录，密码 bcrypt 存储，session cookie（HttpOnly、Secure、SameSite=Strict） | P0 |
| U-2 | 首次启动无用户时自动创建管理员，初始密码打印到日志，首次登录强制修改 | P0 |
| U-3 | 登录失败次数限制（如 5 次 / 15 分钟） | P1 |
| U-4 | 用户管理：新增、禁用、重置密码（所有用户均为管理员） | P1 |
| U-5 | 审计日志：记录登录、主机注册和吊销、项目增删改、手动触发、取消、删除备份等操作 | P0 |
| U-6 | MySQL 凭据使用 AES-256-GCM 加密存储，密钥来自 server 本地密钥文件 | P0 |
| U-7 | server ↔ agent 全程 HTTPS，使用自签 CA；agent 通过 `server_ca` 校验 server 证书 | P0 |
| U-9 | backup-server 提供证书生成命令：`backup-server cert init` 生成自签 CA，`backup-server cert issue --host <域名/IP>...` 签发 server 证书（含 SAN），CA 证书分发给各 agent | P0 |
| U-8 | agent token 只在数据库中保存哈希 | P0 |

### 6.8 企业微信日报（第二阶段）

| 编号 | 需求 | 优先级 |
|---|---|---|
| W-1 | 配置企业微信机器人 webhook 地址与发送时间（默认每日 09:00） | P2 |
| W-2 | 日报内容：统计周期；按主机汇总在线状态和磁盘使用；按项目汇总成功、失败、跳过数；最近一次成功时间；总备份量；失败明细（时间、项目、原因） | P2 |
| W-3 | 使用 markdown 消息格式；超长时截断并附 Web 链接 | P2 |
| W-4 | 支持“发送测试消息” | P2 |
| W-5 | 实时告警（任务失败、主机离线、项目长时间无成功备份）推送企业微信 | P2 |

---

## 7. 数据模型（PostgreSQL）

| 表 | 主要字段 |
|---|---|
| `users` | id、username、password_hash、must_change_password、disabled、last_login_at、created_at |
| `sessions` | id、user_id、expires_at、ip、user_agent |
| `register_tokens` | id、token_hash、expires_at、used_at、created_by |
| `hosts` | id、name、hostname、ip、token_hash、agent_version、status、last_heartbeat_at、backup_root、metrics(jsonb：磁盘、bitmap)、containers(jsonb)、revoked_at、created_at |
| `projects` | id、host_id、name、container、tier、full_interval、incr_interval、offset、incr_mode、retention_days、lag_threshold、parallel、throttle、timeout、retry_count、retry_interval、enabled、mysql_user、mysql_password_enc、config_version、deleted_at、created_at、updated_at |
| `jobs` | id、host_id、project_id、type(full/incr)、chain_id、base_job_id、trigger(schedule/manual/retry)、status、scheduled_at、started_at、finished_at、raw_size、compressed_size、from_lsn、to_lsn、slave_info(jsonb)、used_bitmap、error_code、error_message、log_tail、path、sha256 |
| `chains` | id、project_id、full_job_id、started_at、last_job_at、status、deleted_at |
| `job_events` | id、job_id、ts、type、payload(jsonb) |
| `agent_commands` | id、host_id、type、payload(jsonb)、status、created_by、created_at、acked_at、finished_at、result(jsonb) |
| `audit_logs` | id、user_id、action、target_type、target_id、detail(jsonb)、ip、created_at |
| `settings` | key、value(jsonb) |

- 表结构迁移使用 golang-migrate，仅由 backup-server 在启动时执行。
- `jobs.id` 由 agent 生成（UUIDv7），保证离线补传时幂等。

---

## 8. 接口概要

### 8.1 Agent API（`/api/agent/v1`，Bearer agent token）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/register` | 用注册 token 换取长期 token |
| POST | `/heartbeat` | 每 15s：agent 版本、磁盘、bitmap 占用、容器列表、当前任务、队列（接下来的任务）、已生效的配置版本；返回最新配置版本号 |
| GET | `/config` | 获取本机项目配置（含解密后的 MySQL 凭据）；心跳返回的配置版本号变化时调用 |
| GET | `/commands?wait=30s` | 长轮询获取命令：立即备份、取消、上传日志、重新扫描容器、连接测试 |
| POST | `/commands/{id}/result` | 回报命令执行结果 |
| POST | `/jobs/{id}/events` | 上报状态变化和进度（批量），任务进入 `pending` 时即上报 |
| PUT | `/jobs/{id}` | 上报任务最终结果（幂等） |
| POST | `/jobs/{id}/log` | 上传完整日志 |

### 8.2 Web API（`/api/v1`，session）

| 资源 | 操作 |
|---|---|
| `/auth` | 登录、登出、修改密码 |
| `/hosts` | 列表、详情、生成注册 token、吊销、容器列表 |
| `/projects` | 增删改查、启停、立即执行、连接测试、推荐偏移量 |
| `/jobs` | 列表筛选、详情、取消、查看日志 |
| `/chains` | 按项目列出链、恢复指引 |
| `/schedule` | 获取主机 24h 预测排班 |
| `/dashboard` | 仪表盘汇总 |
| `/events/stream` | SSE 实时进度 |
| `/settings` | 全局设置（含企业微信） |
| `/audit-logs` | 审计查询 |
| `/users` | 用户管理 |

---

## 9. 非功能需求

| 类别 | 要求 |
|---|---|
| 可靠性 | server 或 PostgreSQL 不可用时 agent 继续按缓存配置执行；结果暂存本地 SQLite，恢复后按顺序补传；补传接口幂等 |
| 可靠性 | agent 异常退出后由 systemd 自动拉起；启动时处理残留的 running 任务和不完整文件 |
| 性能 | 备份吞吐受 xtrabackup 与磁盘限制，agent 自身开销（压缩除外）CPU < 5%、内存 < 200MB |
| 性能 | Web 页面列表查询在 10 万条任务记录下 < 1s |
| IO 控制 | 每台主机严格串行；支持 `--throttle` 限速；xtrabackup 与 zstd 以低 IO 优先级运行 |
| 安全 | 见 6.7；备份目录权限 700；日志中不得出现密码 |
| 可观测 | server 与 agent 输出结构化日志（JSON），按天轮转；提供 `/healthz` |
| 兼容性 | Percona Server 5.6 / 5.7 + xtrabackup 2.4；podman 4.x；Ubuntu 24.04 |
| 时间 | 所有主机须启用 NTP；数据库存储 UTC，前端按浏览器时区显示 |
| 升级 | server 与 agent 可独立升级；API 版本不兼容时 agent 拒绝连接并在心跳中报告 |

---

## 10. 部署与配置

### 10.1 backup-server

- 由用户自行在独立主机部署，项目提供二进制、systemd unit 示例 `backup-server.service` 和配置示例。
- PostgreSQL 由用户负责创建库和账号，项目只提供配置项；表结构由 backup-server 启动时自动迁移。
- Web 端口与 Agent API 端口可分别定义；`agent_listen` 留空时 Agent API 与 Web 共用 `listen`。
- 配置文件 `/etc/backup-server/server.yaml`：

```yaml
listen: "0.0.0.0:8443"          # Web UI + 管理 API
agent_listen: "0.0.0.0:9443"    # Agent API，可单独开放给备份服务器；留空则复用 listen
tls:
  cert: /etc/backup-server/tls/server.crt
  key:  /etc/backup-server/tls/server.key
database:
  host: pg-host
  port: 5432
  name: backup
  user: backup
  password: ""                  # 也可用环境变量 BACKUP_DB_PASSWORD 覆盖
  sslmode: require              # disable / require / verify-full
  max_conns: 10
secrets:
  master_key_file: /etc/backup-server/master.key
auth:
  session_ttl: 12h
  bootstrap_admin: admin
agent:
  heartbeat_timeout: 60s
log:
  level: info
  dir: /var/log/backup-server
```

### 10.2 backup-agent

- 每台备份服务器部署，以 root 运行（需要调用 podman），systemd 服务 `backup-agent.service`。
- 配置文件 `/etc/backup-agent/agent.yaml`：

```yaml
server_url: "https://backup-server.internal:9443"   # 指向 server 的 agent_listen 端口
server_ca: /etc/backup-agent/tls/ca.crt
register_token_file: /etc/backup-agent/register.token
state_dir: /var/lib/backup-agent
backup_root: /data/backup
lock_file: /run/backup-agent.lock
zstd:
  threads: 8
  level: 3
log:
  level: info
  dir: /var/log/backup-agent
```

- 接入 server 后，项目配置由 server 下发，`agent.yaml` 中无需也不应再写 `projects`；即使写了也会被忽略（见 5.3.4）。
- P1 独立运行时没有 `server_url`，在 `agent.yaml` 的 `projects` 中直接配置项目，示例见 `deploy/agent.yaml.example`。

### 10.3 新增备份服务器流程

1. 在 Web 上生成注册 token。
2. 拷贝 `backup-agent` 二进制、`agent.yaml`、CA 证书和 token 文件到新主机。
3. `systemctl enable --now backup-agent`。
4. 在 Web 上确认主机在线后，添加项目。

### 10.4 MySQL 备份账号（每个从库）

```sql
SET sql_log_bin = 0;
CREATE USER 'backup'@'localhost' IDENTIFIED BY '...';
GRANT RELOAD, LOCK TABLES, PROCESS, REPLICATION CLIENT, SUPER ON *.* TO 'backup'@'localhost';
SET sql_log_bin = 1;
```

### 10.5 从库前置条件

- 需要增量备份的从库在 `my.cnf` 中开启 `innodb_track_changed_pages = ON`（需重启容器）。
- 开启后须先完成一次全量，后续增量才能使用 bitmap。

---

## 11. 代码仓库结构

```
backup/
├── cmd/
│   ├── backup-server/
│   └── backup-agent/
├── internal/
│   ├── api/            # server 与 agent 共享的请求/响应类型
│   ├── schedule/       # 排班算法（agent 执行与 server 预测共用）
│   ├── server/         # HTTP、认证、SSE、命令通道、日报
│   ├── store/          # PostgreSQL 访问 + migrations
│   ├── agent/
│   │   ├── worker/     # 串行 worker + 文件锁
│   │   ├── executor/   # 预检查、xtrabackup、zstd、sha256、收尾
│   │   ├── mysql/      # 复制状态、bitmap 清理
│   │   ├── podman/
│   │   ├── retention/
│   │   └── cache/      # 本地 SQLite
│   └── crypto/
├── web/                # Vue3 + Element Plus + ECharts
├── deploy/             # systemd unit、示例配置、建库 SQL
└── Makefile
```

技术选型：Go 1.22+、pgx、golang-migrate、modernc.org/sqlite（agent 本地缓存）、Vue3 + Element Plus + ECharts；前端构建产物通过 `embed` 打包进 backup-server。

---

## 12. 里程碑

| 阶段 | 内容 | 验收标准 |
|---|---|---|
| P0 验证 | 开启 bitmap；创建备份账号；在 gays 上手动完成流式全量 → 增量 → prepare 恢复；实测各库全量、增量耗时 | 恢复后的实例可启动并通过抽样校验；输出各库耗时数据 |
| P1 agent 核心 | 执行器、串行 worker、本地排班、保留、bitmap 清理、本地缓存；可用本地 yaml 配置独立运行 | 在 esh-01 上对 gays 连续运行 48h，任务按计划执行且无并发 |
| P2 server | PostgreSQL 迁移、注册与认证、配置下发、上报、命令通道、凭据加密 | agent 接入 server；停掉 server 后 agent 继续备份，恢复后数据完整补传 |
| P3 前端 | 登录、仪表盘、主机、项目、排班、历史与链视图、设置、审计 | 所有 P0 功能需求可在 Web 完成 |
| P4 上线 | esh-01 逐个接入 5 个项目；inx-01 重装后接入；完成一次完整恢复演练 | 连续 7 天无漏备份；恢复演练成功 |
| P5 日报 | 企业微信日报与实时告警 | 每日准时收到日报，内容与 Web 一致 |

---

## 13. 风险与应对

| 风险 | 影响 | 应对 |
|---|---|---|
| 未开启 bitmap 时增量需全盘扫描 | 每小时读 IO 接近全量，排班可能放不下 | 开启 `innodb_track_changed_pages`；任务记录是否使用 bitmap 并告警 |
| `/nvme_data` 使用率 95% | fetisch / poppen 数据增长或 bitmap 文件可能写满磁盘 | 磁盘告警；bitmap 及时清理；尽快扩容或迁移 |
| 备份与 kmv2、gays 数据目录在同一 HDD | 写备份与复制争抢 IO；单盘故障同时丢失数据和备份 | `--throttle` 限速；后续规划异地副本 |
| `--safe-slave-backup` 短暂停止 SQL 线程 | 复制延迟短时上升 | 可接受；收尾时确保 SQL 线程恢复 |
| 从库复制中断未被发现 | 备份内容过旧 | 预检查复制状态并作为失败原因记录、告警 |
| 实际耗时超出排班预期 | 任务积压 | 串行队列 + 合并策略；甘特图超负荷提示；根据实测调整偏移量 |

---

## 14. 待确认事项

| # | 事项 | 结论 |
|---|---|---|
| 1 | backup-server 部署 | 已确认：用户自行部署；Web 端口与 Agent 端口通过配置定义 |
| 2 | PostgreSQL | 已确认：用户负责，项目只提供配置项 |
| 3 | TLS 证书 | 已确认：自签 CA，由 `backup-server cert` 命令生成 |
| 4 | 默认排班 | 待 P0 实测后确认 |
| 5 | 增量模式默认值 | 已确认：链式，项目可切换为差异（见 6.4.6） |
