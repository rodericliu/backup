This manual explains how this platform uses Percona XtraBackup 2.4 to back up MySQL replicas: the steps a backup goes through, how files are compressed and stored, how full backups relate to chained incrementals, and how to restore from a backup. For how xtrabackup itself works and its command options, see the [Percona XtraBackup 2.4 documentation](https://docs.percona.com/percona-xtrabackup/2.4/index.html).

> Percona XtraBackup 2.4 supports only MySQL 5.7 and earlier. It has reached end of life (EOL), and its documentation is read-only.

## Overview

- **backup-server** stores project configuration, collects job results and serves this web interface. It never connects to the databases being backed up.
- **backup-agent** runs on every backup server. It schedules locally, runs backups **serially** with a single worker (one job at a time), applies retention, and reports heartbeats, progress and results to the server.
- **Replica container**: xtrabackup, xbstream and zstd all run inside the replica's podman container. The backup root (default `/data/backup`) is mounted into the container at the same path, so files written inside the container land directly on the host's backup disk.

The complete flow of one backup:

```text
Prechecks → write temporary credentials
  → xtrabackup streams inside the container | zstd compresses into a file
  → validate result → extract metadata
  → finish (restart SQL thread / purge bitmaps / delete credentials) → report
```

## Backup process

### 1. Prechecks

If any of the following fails, the job fails immediately with the corresponding error code:

| Check | Condition | Error code |
|---|---|---|
| Container | The project's container exists and is running | `CONTAINER_NOT_RUNNING` |
| Replication threads | `Slave_IO_Running` and `Slave_SQL_Running` are both `Yes` | `REPLICATION_BROKEN` |
| Replication lag | `Seconds_Behind_Master` is below the project's lag threshold (default 10 minutes) | `REPLICATION_LAG` |
| Disk space | Free space in the backup directory ≥ estimated size × 1.5 | `DISK_INSUFFICIENT` |

The estimated size is the compressed size of the project's last successful backup of the same type (full or incremental). Without history, it is estimated from the size of the MySQL data directory.

### 2. Temporary credentials

The MySQL account and password never appear in command-line arguments. Before each run, the agent writes a temporary `my-*.cnf` with mode `0600` under `<backup root>/.agent/` and passes it to xtrabackup with `--defaults-extra-file`. The file is deleted as soon as the job ends, whether it succeeded or not.

For the privileges the backup account needs, see [Connection and Privileges Needed](https://docs.percona.com/percona-xtrabackup/2.4/using_xtrabackup/privileges.html).

### 3. Running the backup

The agent runs this pipeline inside the container with `podman exec` (simplified for readability):

```bash
xtrabackup --defaults-extra-file=<temporary cnf> \
  --backup --stream=xbstream \
  --slave-info --safe-slave-backup \
  --parallel=<N> [--throttle=<N>] \
  --target-dir=<job directory> --extra-lsndir=<job directory> \
  [--incremental-basedir=<job directory of the base backup>] \
| zstd -q -c -T<threads> -<level> > <job directory>/backup.xbstream.zst
```

| Option | What this platform uses it for |
|---|---|
| `--stream=xbstream` | Instead of writing an uncompressed backup to disk first, stream the data to stdout for zstd to compress |
| `--slave-info` | Record the master binlog file and position that the replica corresponds to, used to rebuild replication after a restore |
| `--safe-slave-backup` | Stop the SQL thread briefly at the end of the backup to get a consistent replication position |
| `--parallel` | Number of threads copying data files, set in the project configuration (default 4) |
| `--throttle` | Limit IO operations per second; 0 means unlimited. Set in the project configuration |
| `--extra-lsndir` | Also write `xtrabackup_checkpoints` and `xtrabackup_info` as regular files; the next incremental reads its base LSN from here |
| `--incremental-basedir` | Incrementals only: points to the job directory of the base backup (see "Full, incremental and chains" below) |

Other details:

- With `low_io_priority: true` in the agent configuration (the default), xtrabackup and zstd run under `ionice -c3` and `nice -n 19` to minimize the impact on the replica's workload.
- Progress is reported every 5 seconds: the **compressed bytes written so far** and the write rate. The progress figure is therefore much smaller than the actual database size.
- When the project's timeout (default 6 hours) is exceeded or the job is canceled in the web interface, the agent terminates xtrabackup inside the container, and the job is recorded as timed out or canceled.

For all options, see [The xtrabackup Option Reference](https://docs.percona.com/percona-xtrabackup/2.4/xtrabackup_bin/xbk_option_reference.html) and [Throttling Backups](https://docs.percona.com/percona-xtrabackup/2.4/advanced/throttling_backups.html).

### 4. Success criteria

A job succeeds only if **all** of the following hold:

1. zstd exits with code 0;
2. xtrabackup exits with code 0;
3. the xtrabackup log contains `completed OK!`;
4. `backup.xbstream.zst` is not empty and has been flushed to disk.

### 5. Extracting metadata

After a success, the agent reads `from_lsn` and `to_lsn` from `xtrabackup_checkpoints`, parses the master binlog position (and GTID) from the log into `xtrabackup_slave_info`, and records whether an incremental used the changed page bitmap. All of this is shown in the job details.

### 6. Finishing

Always, whether the job succeeded or not:

- **Make sure the SQL thread is running**: `--safe-slave-backup` stops the SQL thread. If an interrupted backup left it stopped, the agent runs `START SLAVE SQL_THREAD` and logs it.
- **Delete the temporary credentials file.**

Only after a success:

- **Purge bitmaps**: run `PURGE CHANGED_PAGE_BITMAPS BEFORE <LSN>`. After a full backup, the LSN is the full's own `to_lsn`; after an incremental, it is the `to_lsn` of the chain's full backup, so that later incrementals in the chain can still use the bitmap.

Only after a failure:

- **Delete incomplete artifacts** (data file, checkpoints, info, slave_info), keeping `backup.log` and `meta.json` for troubleshooting.

### 7. Storage layout

```text
<backup root>/<project>/<YYYYMMDD-HHMM>-chain/     # one chain, named after the start time of its full backup
    00-full/                                      # full backup
        backup.xbstream.zst                       # compressed backup stream
        xtrabackup_checkpoints                    # backup type, from_lsn, to_lsn
        xtrabackup_info                           # backup details
        xtrabackup_slave_info                     # master binlog position (for rebuilding replication)
        backup.log                                # complete xtrabackup log
        meta.json                                 # job ID, type, LSNs, size, times, status, ...
    01-incr/                                      # 1st incremental, same files as above
    02-incr/
    ...
```

For what each file means, see [Index of files created by Percona XtraBackup](https://docs.percona.com/percona-xtrabackup/2.4/xtrabackup-files.html).

## Compression

This platform does **not** use xtrabackup's built-in `--compress` (qpress). Instead, it pipes the xbstream stream into zstd:

- **The disk is written only once**: data flows from xtrabackup straight into the compressed target file, with no intermediate "write first, compress later" file, so the backup disk sees less writing and less space use.
- **Better ratio and speed**: at similar CPU cost, zstd compresses noticeably better than qpress.
- **Simple restore**: `zstd -dc backup.xbstream.zst | xbstream -x -C <directory>` produces a directory ready for prepare in one step, without a separate `--decompress`.

Compression is configured in the agent configuration file and applies to all projects on that host:

| Setting | Default | Description |
|---|---|---|
| `zstd.threads` | `8` | zstd compression threads (`-T`) |
| `zstd.level` | `3` | Compression level (1–19); higher levels compress more but cost more CPU |
| `low_io_priority` | `true` | Run xtrabackup and zstd with low IO and CPU priority |

zstd runs inside the container, so the replica image must include `zstd`; otherwise the job fails with `COMPRESS_ERROR`.

## Full, incremental and chains

### LSN

InnoDB marks every change to a data page with an ever-increasing log sequence number (LSN). Every backup records two LSNs:

- `from_lsn`: the LSN copying starts from. 0 for a full backup; the base backup's `to_lsn` for an incremental.
- `to_lsn`: the checkpoint LSN at the end of the backup.

An incremental backup copies only pages whose LSN is greater than `from_lsn`, that is, pages that changed after the base.

### Full backups

A full backup copies the whole data directory and can be restored on its own. Every full backup starts a **new chain** and is stored as `00-full`.

### Chains

A chain = one full backup + the incrementals after it. Chains are the unit in which this platform organizes, restores and cleans up backups:

- The full-backup interval determines the length of a chain. For example, "important" projects default to a full every 4 hours and an incremental every hour, so a chain is "1 full + up to 3 incrementals".
- A failed incremental leaves no gap: the next incremental is based on the chain's **last successful backup**.
- When a full backup fails, its chain is marked failed, and scheduled incrementals are skipped (`NO_BASE`) until a full backup succeeds again.
- When a project has no usable full backup yet, a scheduled incremental runs as a full backup instead.

### Chained vs. differential incrementals

Projects can use one of two incremental modes; **chained is the default**. Take "full at 00:00, incrementals at 01:00, 02:00, 03:00" as an example:

| | Chained (default) | Differential |
|---|---|---|
| Base of the 01:00 incremental | 00:00 full | 00:00 full |
| Base of the 02:00 incremental | 01:00 incremental | 00:00 full |
| Base of the 03:00 incremental | 02:00 incremental | 00:00 full |
| Each incremental contains | Pages changed since the previous backup | All pages changed since the full |
| Size and IO per run | Small and steady | Grows over time |
| Restore to 03:00 | Full + 01 + 02 + 03 applied in order | Full + 03 |
| One backup in the middle is corrupt | All later points are unrecoverable | Only that point is affected |

This platform defaults to chained because every incremental's IO is small and steady, which fits the primary goal of "keeping replica IO under control". The number of incrementals per chain is limited, so restores stay short.

After changing the incremental mode in the project configuration, the change **takes effect from the next chain**; the current chain keeps its mode.

For how incrementals work, see [Incremental Backup](https://docs.percona.com/percona-xtrabackup/2.4/backup_scenarios/incremental_backup.html).

### Changed page bitmap

With Percona Server's `innodb_track_changed_pages` enabled on the replica, xtrabackup can read the bitmap of changed pages for incrementals instead of scanning the whole data directory, which greatly reduces IO.

- After enabling it, a full backup must complete first; only incrementals after that can use the bitmap.
- When an incremental does not use the bitmap (the log lacks `using the changed page bitmap`, or it fell back to a full scan), the job status is **Warning**. The backup itself is usable, but that incremental read the whole data directory.
- After every successful backup, the agent purges bitmap files that are no longer needed (see "Finishing" above). When bitmaps exceed a threshold (default 10 GiB), the agent logs an alert.

### Retention and cleanup

Cleanup works on **whole chains**; individual backups within a chain are never deleted:

- When the newest successful backup of a chain finished longer ago than the project's retention days (default 7), the whole chain directory is deleted.
- These chains are never deleted: the newest chain, the newest chain whose full backup succeeded, and chains still in progress.
- The agent checks for cleanup after every successful backup.

## Restore process

> The "Backup chain" view of the Job history page lists every restorable point in time. Click **Restore guide** on a backup to get ready-to-copy commands generated for that backup's chain, host, container and data directory. This section explains what each step does.

### 1. Determine which backups to use

Starting from the target backup, follow each backup's "base job" back to the full backup, then order them from oldest to newest:

- **Chained**: restoring to 03:00 needs `00-full`, `01-incr`, `02-incr` and `03-incr`, all of them;
- **Differential**: restoring to 03:00 needs only `00-full` and `03-incr`.

Every backup in this sequence must have the status Success or Warning; otherwise the point in time is not restorable. The restored data matches the state when the target backup **finished**.

### 2. Preparation

- On the **Hosts** page, pause scheduling for this host. The replica container is stopped during the restore, so scheduled backups would fail; resume scheduling afterwards.
- Make sure the backup disk has enough space: the extraction directory needs about as much space as the raw database.
- Placeholders such as `<backup root>`, `<container>`, `<image>` and `<host data directory>` in the commands below are filled in by the "Restore guide".

### 3. Extract

Inside the replica container (which has `zstd` and the 2.4 `xbstream`), extract each backup into the restore directory:

```bash
R=<backup root>/restore/<project>-<time>
podman exec <container> mkdir -p $R/full $R/incr1 $R/incr2
podman exec <container> sh -c 'zstd -dc <chain directory>/00-full/backup.xbstream.zst | xbstream -x -C '$R'/full'
podman exec <container> sh -c 'zstd -dc <chain directory>/01-incr/backup.xbstream.zst | xbstream -x -C '$R'/incr1'
podman exec <container> sh -c 'zstd -dc <chain directory>/02-incr/backup.xbstream.zst | xbstream -x -C '$R'/incr2'
```

### 4. Prepare

Prepare applies the redo log copied during the backup to the data files, bringing the data to a consistent state.

**With only a full backup:**

```bash
podman exec <container> xtrabackup --prepare --target-dir=$R/full
```

**With incrementals**, prepare the full backup first, then apply each incremental in order:

```bash
podman exec <container> xtrabackup --prepare --apply-log-only --target-dir=$R/full
podman exec <container> xtrabackup --prepare --apply-log-only --target-dir=$R/full --incremental-dir=$R/incr1
podman exec <container> xtrabackup --prepare --target-dir=$R/full --incremental-dir=$R/incr2
```

Note:

- Every step except the **last** incremental needs `--apply-log-only`. It rolls committed transactions forward without rolling back uncommitted ones yet; rolling back early would make it impossible to apply further incrementals.
- The last incremental is applied without `--apply-log-only`; this step performs the rollback and yields data that can start.
- Apply strictly in order; never skip or reorder.
- All incrementals are merged into `$R/full`; only that directory is used afterwards.

### 5. Replace the data directory

```bash
# Stop the replica container, keep the original data directory under a new name and create an empty one
podman stop <container>
mv <host data directory> <host data directory>.before-restore-<time>
mkdir <host data directory>

# Copy the data back with a temporary container of the same image and fix ownership
podman run --rm --user 0 --entrypoint sh \
  -v <backup root>:<backup root> -v <host data directory>:/var/lib/mysql \
  <image> \
  -c 'xtrabackup --copy-back --target-dir='$R'/full --datadir=/var/lib/mysql && chown -R mysql:mysql /var/lib/mysql'

# Start the container and check its log
podman start <container>
podman logs --tail 50 <container>
```

- **Rename and keep** the original data directory first, and delete it only after confirming the restore, so you can roll back if something goes wrong.
- If the host data directory is itself a mount point, `mv` fails. In that case, move its contents elsewhere or empty it instead.
- If the container's mounts carry an SELinux label (`:Z`), add the same option to the temporary container's `-v` arguments.
- Running `--copy-back` with the replica's own image keeps the xtrabackup version and file ownership consistent.

### 6. Rebuild replication

Use the master position recorded by the **target backup** (the last one in the sequence), i.e. the "Master position" in the job details or the content of `xtrabackup_slave_info`:

```sql
-- podman exec -it <container> mysql -uroot -p
CHANGE MASTER TO MASTER_HOST='<master>', MASTER_USER='<user>', MASTER_PASSWORD='<password>',
  MASTER_LOG_FILE='<binlog file>', MASTER_LOG_POS=<position>;
START SLAVE;
SHOW SLAVE STATUS\G
```

If the master has GTID enabled, you can instead run `SET GLOBAL gtid_purged='<GTID recorded by the backup>'` and set up replication with `MASTER_AUTO_POSITION=1`.

### 7. Wrap up

1. Watch `SHOW SLAVE STATUS` until both IO and SQL threads are `Yes` and the lag catches up;
2. Spot-check the data;
3. Delete the restore directory `$R` and the kept `<host data directory>.before-restore-<time>`;
4. Resume scheduling on the **Hosts** page.

For the full restore procedure, see [The Backup Cycle - Full Backups](https://docs.percona.com/percona-xtrabackup/2.4/backup_scenarios/full_backup.html) and [The xbstream binary](https://docs.percona.com/percona-xtrabackup/2.4/xbstream/xbstream.html).

## Common failures

| Code | Meaning | What to check |
|---|---|---|
| `CONTAINER_NOT_RUNNING` | The container does not exist or is not running | Check the container with `podman ps -a` on the host; confirm the container name in the project |
| `REPLICATION_BROKEN` | The IO or SQL thread is not running, or the lag is NULL | Check `Last_IO_Error` and `Last_SQL_Error` in `SHOW SLAVE STATUS\G` and fix replication first |
| `REPLICATION_LAG` | Replication lag exceeds the threshold | Wait for the replica to catch up; if lag is often high, raise the project's lag threshold or adjust the schedule |
| `DISK_INSUFFICIENT` | Not enough free space on the backup disk | Free up or extend the backup disk; check whether the retention period is too long |
| `MYSQL_AUTH_FAILED` | The backup account failed to authenticate or lacks privileges | Verify the account, password and privileges in the project |
| `XTRABACKUP_ERROR` | xtrabackup failed | Check the log tail in the job details, or `backup.log` in the job directory |
| `COMPRESS_ERROR` | zstd or writing the file failed | Make sure the container has `zstd` and the backup root is mounted into the container and writable |
| `TIMEOUT` | The project's timeout was exceeded | Check for throttling or IO contention; raise the timeout if needed |
| `NO_BASE` | No usable base for the incremental | The chain's full backup failed or was cleaned up; wait for, or manually run, a full backup |
| `INTERRUPTED` | Interrupted by an agent restart or crash | Check the agent log; the next scheduled run proceeds normally |
| `CANCELED` | Canceled manually | — |
| `SUPERSEDED` | A new job of the same project and type was queued while this one was still waiting, so this one was skipped | Usually the previous job took so long that the next scheduled time arrived; check whether the schedule is too full |

## Official documentation

- [Percona XtraBackup 2.4 documentation home](https://docs.percona.com/percona-xtrabackup/2.4/index.html)
- [How Percona XtraBackup Works](https://docs.percona.com/percona-xtrabackup/2.4/how_xtrabackup_works.html)
- [Percona XtraBackup User Manual](https://docs.percona.com/percona-xtrabackup/2.4/manual.html)
- [Frequently Asked Questions](https://docs.percona.com/percona-xtrabackup/2.4/faq.html)
