# 使用 Podman pod 部署 backup-server

本文档说明如何用一个 Podman pod 运行 backup-server、PostgreSQL 和 nginx。

## 架构

```
                      浏览器
                        │  https://bup.ideawisegroup.com/
                        ▼
┌──────────────────── pod: backup ─────────────────────┐
│                                                      │
│  backup-web     :80  → 跳转 HTTPS                     │
│                 :443 → 前端静态文件                   │
│                        /api/、/healthz 转发 ──┐       │
│                                               ▼      │
│  backup-server  127.0.0.1:8443  Web 管理 API          │
│                 0.0.0.0:9443    Agent API ◄────────── backup-agent（https://<内部IP>:9443）
│                        │                             │
│                        ▼                             │
│  backup-db      127.0.0.1:5432  PostgreSQL 17        │
│                                                      │
└──────────────────────────────────────────────────────┘
```

| 容器 | 镜像 | 说明 |
|---|---|---|
| `backup-web` | `reg.ideawisegroup.com:1443/library/backup-web`（`Dockerfile` 的 `web` 目标） | 提供前端页面，转发 `/api/` 和 `/healthz` |
| `backup-server` | `reg.ideawisegroup.com:1443/library/backup-server`（`Dockerfile` 的 `server` 目标） | 管理 API 和 Agent API，启动时自动迁移数据库 |
| `backup-db` | `docker.io/library/postgres:17` | 在 `backup` schema 中保存平台数据 |

- 同一个 pod 中的容器共享网络，容器之间通过 `127.0.0.1` 通信。
- 宿主机只开放 `80`、`443` 和 `9443`；`8443` 和 `5432` 只在 pod 内使用。
- Agent 不经过 nginx，而是直接访问 server 的内部 IP 和 `9443` 端口，并用私有 CA 校验 server 证书。

以下示例使用这些值，请替换为实际值：

| 项目 | 示例值 |
|---|---|
| Web 域名 | `bup.ideawisegroup.com` |
| server 宿主机内部 IP | `10.20.0.10` |
| 宿主机数据目录 | `/data/volumes/backup-server` |
| 时区 | `Asia/Shanghai`（必须与各 agent 主机一致） |

## 宿主机目录

```
/data/volumes/backup-server/
├── ca/                    # 私有 CA：ca.crt、ca.key；不挂载到运行中的容器，必须备份
├── etc/                   # 挂载到 backup-server 的 /etc/backup-server（只读）
│   ├── server.yaml
│   ├── master.key         # 用于加密 MySQL 密码；丢失后无法解密，必须备份
│   └── tls/
│       ├── server.crt
│       └── server.key
├── pgdata/                # 挂载到 backup-db 的 /var/lib/postgresql/data
├── initdb/
│   └── postgres-init.sh   # 仅在 PostgreSQL 首次初始化时执行
├── env/                   # 数据库密码，权限 600
│   ├── backup-db.env      # POSTGRES_PASSWORD、BACKUP_DB_PASSWORD
│   └── backup-server.env  # BACKUP_DB_PASSWORD
└── nginx/
    ├── certs/             # bup.ideawisegroup.com 的公网证书
    │   ├── ideawisegroup.com.crt
    │   └── ideawisegroup.com.key
    └── backup-ca.crt      # 私有 CA 公钥，nginx 用它校验 backup-server
```

## 前提条件

- Podman 3.0 或更高版本。本文不使用 `podman secret`，数据库密码通过 `--env-file` 传入。
- `bup.ideawisegroup.com` 已解析到宿主机，浏览器用户可以访问 80 和 443 端口。
- 各备份服务器可以访问 `10.20.0.10:9443`。
- 构建机器上有本仓库源码，并且可以推送镜像到 `reg.ideawisegroup.com:1443`。
- 部署宿主机可以从 `reg.ideawisegroup.com:1443` 拉取镜像。

下面以 **rootless podman** 为例。使用 root 运行时：

- 把 `podman unshare chown` 改成 `chown`。
- 跳过“允许使用 80 和 443 端口”这一步。

## 1. 构建、推送和拉取镜像

| 镜像 | 地址 |
|---|---|
| backup-server | `reg.ideawisegroup.com:1443/library/backup-server` |
| backup-web | `reg.ideawisegroup.com:1443/library/backup-web` |

每个镜像都会打两个标签：

- 版本号标签，例如 `v1.2.0` 或 `048d5a3`，用于回滚。
- `latest`，部署命令默认使用这个标签。

**在构建机器的仓库根目录执行：**

```bash
REG=reg.ideawisegroup.com:1443/library
V=$(git describe --tags --always --dirty)

podman build --target server -t $REG/backup-server:$V -t $REG/backup-server:latest --build-arg VERSION=$V .
podman build --target web    -t $REG/backup-web:$V    -t $REG/backup-web:latest .

podman login reg.ideawisegroup.com:1443
for img in backup-server backup-web; do
  podman push $REG/$img:$V
  podman push $REG/$img:latest
done
```

两个镜像都包含同一次构建出的前端代码。构建、推送和部署时，两个镜像都要使用同一个版本。

**在部署宿主机上执行：**

```bash
podman login reg.ideawisegroup.com:1443
podman pull reg.ideawisegroup.com:1443/library/backup-server:latest
podman pull reg.ideawisegroup.com:1443/library/backup-web:latest
```

如果镜像仓库使用私有 CA 签发的证书，需要先把 CA 证书放到下面的位置，再执行 `podman login`：

| 运行方式 | CA 证书位置 |
|---|---|
| rootless podman | `~/.config/containers/certs.d/reg.ideawisegroup.com:1443/ca.crt` |
| root 运行 podman | `/etc/containers/certs.d/reg.ideawisegroup.com:1443/ca.crt` |

## 2. 创建目录

```bash
B=/data/volumes/backup-server
sudo mkdir -p $B
sudo chown $(id -u):$(id -g) $B
mkdir -p $B/{ca,etc/tls,pgdata,initdb,env,nginx/certs}
```

把仓库中的以下两个文件复制到部署宿主机：

| 仓库中的文件 | 宿主机位置 |
|---|---|
| `deploy/podman/postgres-init.sh` | `$B/initdb/postgres-init.sh` |
| `deploy/server.yaml.example` | `$B/etc/server.yaml` |

后续命令都会使用变量 `B`。

## 3. 创建数据库密码文件

当前 Podman 版本不能使用 `podman secret`，所以把数据库密码写入两个环境变量文件，启动容器时用 `--env-file` 传入。

```bash
PG_SUPER=$(openssl rand -hex 24)
DB_PW=$(openssl rand -hex 24)

cat > $B/env/backup-db.env <<EOF
POSTGRES_PASSWORD=$PG_SUPER
BACKUP_DB_PASSWORD=$DB_PW
EOF

cat > $B/env/backup-server.env <<EOF
BACKUP_DB_PASSWORD=$DB_PW
EOF

chmod 700 $B/env
chmod 600 $B/env/*.env
unset PG_SUPER DB_PW
```

| 文件 | 使用者 | 内容 |
|---|---|---|
| `backup-db.env` | `backup-db` | `POSTGRES_PASSWORD`：超级用户 `postgres` 的密码<br>`BACKUP_DB_PASSWORD`：初始化脚本用它创建 `backup` 账号 |
| `backup-server.env` | `backup-server` | `BACKUP_DB_PASSWORD`：连接 PostgreSQL 时使用的 `backup` 账号密码 |

- 两个文件里的 `BACKUP_DB_PASSWORD` 必须相同。
- backup-server 只拿到应用账号密码，拿不到 PostgreSQL 超级用户密码。
- 密码使用 `openssl rand -hex` 生成，只包含 `0-9` 和 `a-f`，不需要在 env 文件中加引号或转义。

> 有宿主机权限的人可以通过 `podman inspect` 看到这些环境变量。需要严格控制能登录部署宿主机的人员。
>
> `POSTGRES_PASSWORD` 和 `BACKUP_DB_PASSWORD` 只在 `pgdata` 首次初始化时生效。之后修改 env 文件不会修改数据库中的密码。修改密码的方法见“日常操作”。

## 4. 生成私有 CA、server 证书和主密钥

```bash
IMG=reg.ideawisegroup.com:1443/library/backup-server:latest

# 私有 CA
podman run --rm --user 0 -v $B/ca:/etc/backup-server/ca:Z $IMG cert init

# server 证书：内部 IP 用于 Agent 访问，域名用于 nginx 校验
podman run --rm --user 0 \
  -v $B/ca:/etc/backup-server/ca:ro,Z \
  -v $B/etc/tls:/etc/backup-server/tls:Z \
  $IMG cert issue -host 10.20.0.10 -host bup.ideawisegroup.com

# 主密钥
podman run --rm --user 0 -v $B/etc:/etc/backup-server:Z $IMG keygen

# 复制 CA 公钥给 nginx；不要复制 ca.key
cp $B/ca/ca.crt $B/nginx/backup-ca.crt
```

> 必须备份 `ca/` 和 `etc/master.key`。
>
> - 丢失 `master.key` 后，已保存的 MySQL 密码无法解密。
> - 丢失 `ca.key` 后，无法再签发同一个 CA 的证书，需要给所有 agent 重新分发 CA。

## 5. 编写 `server.yaml`

把 `$B/etc/server.yaml` 改为以下内容：

```yaml
# /etc/backup-server/server.yaml（容器内路径）

listen: "127.0.0.1:8443"          # Web 管理 API，只供 pod 内的 nginx 访问
agent_listen: "0.0.0.0:9443"      # Agent API，通过宿主机内部 IP 对外开放

tls:
  cert: /etc/backup-server/tls/server.crt
  key:  /etc/backup-server/tls/server.key

database:
  host: 127.0.0.1                 # pod 内的 backup-db
  port: 5432
  name: backup
  user: backup
  password: ""                    # 留空，由 backup-server.env 中的 BACKUP_DB_PASSWORD 提供
  sslmode: disable                # 只在 pod 内部通信
  max_conns: 10

secrets:
  master_key_file: /etc/backup-server/master.key

auth:
  session_ttl: 12h
  bootstrap_admin: admin          # 首次启动时创建，初始密码打印在日志中

agent:
  heartbeat_timeout: 60s

timezone: Asia/Shanghai           # 容器默认使用 UTC，必须明确填写，并与 agent 时区一致

assistant:
  enabled: false

log:
  level: info
  dir: ""                         # 输出到 stderr，用 podman logs 查看
```

`listen` 和 `agent_listen` 必须使用不同端口：

- `listen` 使用 `127.0.0.1:8443`，只允许 pod 内的 nginx 访问。
- `agent_listen` 使用 `0.0.0.0:9443`，供 Agent 访问。

如果两者相同，Agent API 会和 Web API 共用 `8443`，Agent 就无法通过 `9443` 访问。

## 6. 设置文件权限

backup-server 容器使用 UID/GID `10001` 运行。`master.key` 的权限必须是 `600`，否则程序会拒绝启动。

PostgreSQL 容器内的 `postgres` 用户 UID 是 `999`。

```bash
chmod 600 $B/etc/server.yaml $B/etc/master.key $B/etc/tls/server.key
chmod 755 $B/initdb/postgres-init.sh
podman unshare chown -R 10001:10001 $B/etc
podman unshare chown -R 999:999 $B/pgdata
```

## 7. 准备公网 HTTPS 证书

把公司的 `ideawisegroup.com` 证书复制到以下位置：

| 文件 | 内容 |
|---|---|
| `$B/nginx/certs/ideawisegroup.com.crt` | 证书文件，必须覆盖 `bup.ideawisegroup.com`，例如 `*.ideawisegroup.com` |
| `$B/nginx/certs/ideawisegroup.com.key` | 证书私钥 |

`ideawisegroup.com.crt` 中要按顺序放入：

1. 站点证书
2. 中间证书

否则部分客户端会提示证书链不完整。

```bash
install -m 644 ideawisegroup.com.crt $B/nginx/certs/
install -m 600 ideawisegroup.com.key $B/nginx/certs/

# 检查证书有效期、覆盖的域名，以及私钥是否匹配
openssl x509 -in $B/nginx/certs/ideawisegroup.com.crt -noout -subject -enddate -ext subjectAltName
diff <(openssl x509 -in $B/nginx/certs/ideawisegroup.com.crt -noout -pubkey) \
     <(openssl pkey -in $B/nginx/certs/ideawisegroup.com.key -pubout) && echo "key matches"
```

更换证书时，覆盖这两个文件，然后执行：

```bash
podman exec backup-web nginx -s reload
```

## 8. 允许 rootless podman 使用 80 和 443 端口

```bash
echo 'net.ipv4.ip_unprivileged_port_start=80' | sudo tee /etc/sysctl.d/90-unprivileged-ports.conf
sudo sysctl --system
```

## 9. 创建 pod

```bash
podman pod create --name backup -p 80:80 -p 443:443 -p 9443:9443
```

## 10. 启动 PostgreSQL

```bash
podman run -d --pod backup --name backup-db --restart=always \
  --env-file $B/env/backup-db.env \
  -v $B/pgdata:/var/lib/postgresql/data:Z \
  -v $B/initdb/postgres-init.sh:/docker-entrypoint-initdb.d/10-backup.sh:ro,Z \
  --health-cmd 'pg_isready -U postgres -d backup' --health-interval 10s \
  docker.io/library/postgres:17
```

`pgdata` 为空时，PostgreSQL 会初始化数据目录，并执行 `postgres-init.sh`。脚本会：

- 创建 `backup` 账号，连接数上限为 30。
- 创建 `backup` 数据库，所有者为 `backup`。
- 只允许 `backup` 连接 `backup` 数据库。
- 创建 `backup` schema，所有者为 `backup`。
- 收回所有用户在 `public` schema 中的权限。
- 设置 `backup` 账号的默认 `search_path` 为 `backup`，默认时区为 `UTC`。

查看初始化结果：

```bash
podman logs backup-db 2>&1 | tail -20
podman exec backup-db psql -U postgres -d backup -c '\dn+'
```

> 初始化脚本只在 `pgdata` 为空时执行。如果脚本执行失败，需要删除容器并清空 `$B/pgdata` 后重新启动：
>
> ```bash
> podman rm -f backup-db
> podman unshare rm -rf $B/pgdata/*
> ```

## 11. 启动 backup-server

```bash
podman run -d --pod backup --name backup-server --restart=always \
  --env-file $B/env/backup-server.env \
  -v $B/etc:/etc/backup-server:ro,Z \
  reg.ideawisegroup.com:1443/library/backup-server:latest
```

启动时，server 会先检查数据库连接。如果 PostgreSQL 尚未就绪，server 会退出，并由 `--restart=always` 自动重启。

数据库可以连接后，server 会：

1. 在 `backup` schema 中自动创建或更新数据表。
2. 首次启动时创建管理员账号 `admin`，并在日志中打印初始密码。

查看初始管理员密码：

```bash
podman logs backup-server 2>&1 | grep -i -E "admin|password"
```

首次登录后需要修改密码。

## 12. 启动 nginx

```bash
podman run -d --pod backup --name backup-web --restart=always \
  -v $B/nginx/certs:/etc/nginx/certs:ro,Z \
  -v $B/nginx/backup-ca.crt:/etc/nginx/backup-ca/ca.crt:ro,Z \
  reg.ideawisegroup.com:1443/library/backup-web:latest
```

nginx 的配置文件已经打包进镜像，源码位于 `deploy/nginx/`：

| 文件 | 内容 |
|---|---|
| `backup.conf` | 站点配置：只接受 `bup.ideawisegroup.com`；HTTP 跳转到 HTTPS；处理前端路由；转发 `/api/` 和 `/healthz` |
| `backup-proxy.conf` | 转发配置：保留 `Host`，确保 server 的同源检查通过；校验 server 证书；关闭缓冲，支持实时事件和 AI 助手 |
| `security-headers.conf` | 与 backup-server 一致的安全响应头 |

## 13. 检查部署

```bash
podman ps --pod --filter pod=backup                    # 三个容器都应显示 Up 和 healthy
podman exec backup-web nginx -t                        # 检查 nginx 配置
curl -s  https://bup.ideawisegroup.com/healthz         # {"status":"ok","version":"..."}
curl -sI https://bup.ideawisegroup.com/ | head -1      # HTTP/2 200
curl -sI http://bup.ideawisegroup.com/ | grep -i location        # 跳转到 https://bup.ideawisegroup.com/
curl -s --cacert $B/ca/ca.crt https://10.20.0.10:9443/healthz    # Agent API 正常
podman exec backup-db psql -U backup -d backup -c 'SHOW search_path' -c '\dt'   # 数据表在 backup schema 中
```

最后，用浏览器打开 `https://bup.ideawisegroup.com/`，用 `admin` 和日志中的初始密码登录。

## 14. 接入 Agent

在每台备份服务器上执行：

1. 把 `$B/ca/ca.crt` 复制到 `/etc/backup-agent/tls/ca.crt`。
2. 修改 `/etc/backup-agent/agent.yaml`：

   ```yaml
   server_url: https://10.20.0.10:9443
   server_ca: /etc/backup-agent/tls/ca.crt
   ```

3. 在 Web 页面上生成注册 token，写入 agent 的 `register.token`，然后执行：

   ```bash
   systemctl restart backup-agent
   ```

如果 `server_url` 使用的地址没有包含在 server 证书中，Agent 会因证书校验失败而无法连接。

更换内部 IP 后，需要用 `cert issue` 重新签发 server 证书：

1. 删除 `$B/etc/tls/server.*`。
2. 按第 4 步重新签发证书。
3. 按第 6 步重新设置权限。
4. 执行 `podman restart backup-server`。

## 15. 防火墙

| 端口 | 来源 | 说明 |
|---|---|---|
| 80/tcp | 浏览器用户 | 跳转到 HTTPS |
| 443/tcp | 浏览器用户 | Web 页面和管理 API |
| 9443/tcp | 只开放给备份服务器 | Agent API |

以 firewalld 为例：

```bash
sudo firewall-cmd --permanent --add-service=http --add-service=https
sudo firewall-cmd --permanent --add-rich-rule='rule family=ipv4 source address=10.20.0.0/24 port port=9443 protocol=tcp accept'
sudo firewall-cmd --reload
```

## 16. 开机自动启动

先允许当前用户在未登录时运行 systemd 用户服务：

```bash
sudo loginctl enable-linger $(id -un)
```

先检查系统是否有 `podman-restart.service`：

```bash
systemctl --user cat podman-restart.service
```

**如果有这个服务**，直接启用。开机后它会启动所有带 `--restart=always` 的容器：

```bash
systemctl --user enable podman-restart.service
```

**如果没有这个服务**，说明 Podman 版本较旧。可以让 podman 为现有的 pod 和容器生成 systemd 服务：

```bash
mkdir -p ~/.config/systemd/user
cd ~/.config/systemd/user
podman generate systemd --files --name backup
systemctl --user daemon-reload
systemctl --user enable pod-backup.service
```

这个命令会生成 `pod-backup.service`，以及三个容器各自的 `container-*.service`。

- 启用 `pod-backup.service` 后，开机会启动整个 pod。
- 每次删除并重建容器后，比如升级后，都要重新执行上面的生成命令。

如果使用 root 运行 podman：

- 服务文件放到 `/etc/systemd/system`。
- 去掉 `systemctl` 命令中的 `--user`。

## 17. 升级

1. 在构建机器上按第 1 步构建并推送新版本。
2. 在部署宿主机上执行：

   ```bash
   podman pull reg.ideawisegroup.com:1443/library/backup-server:latest
   podman pull reg.ideawisegroup.com:1443/library/backup-web:latest

   podman rm -f backup-server backup-web
   # 再执行第 11 步和第 12 步
   ```

- 数据库迁移会在 server 启动时自动执行。
- 升级前建议先按下一节备份数据库。
- 需要回滚时，在第 11 步和第 12 步中把 `latest` 换成旧版本标签，然后重新创建容器。
- 如果新版本已经执行了数据库迁移，回滚前需要恢复升级前的数据库备份。

## 18. 备份

| 内容 | 方式 |
|---|---|
| PostgreSQL | `podman exec backup-db pg_dump -U postgres -Fc backup > backup-db-$(date +%F).dump` |
| 私有 CA、主密钥、配置、数据库密码 | 备份 `$B/ca/`、`$B/etc/` 和 `$B/env/` |

建议用 cron 或 systemd timer 定期导出数据库，并把文件保存到其他主机。

恢复数据库：

```bash
podman stop backup-server
podman exec -i backup-db pg_restore -U postgres -d backup --clean --if-exists < backup-db-2026-11-02.dump
podman start backup-server
```

## 19. 日常操作

```bash
podman logs -f backup-server                     # server 日志
podman logs -f backup-web                        # nginx 访问日志和错误日志
podman exec backup-web nginx -s reload           # 更换证书后重新加载 nginx
podman exec -it backup-db psql -U backup -d backup   # 进入数据库
podman pod stop backup && podman pod start backup    # 停止或启动整个 pod
```

修改 `backup` 账号的数据库密码：

```bash
NEW_PW=$(openssl rand -hex 24)
podman exec backup-db psql -U postgres -c "ALTER ROLE backup PASSWORD '$NEW_PW'"
sed -i "s/^BACKUP_DB_PASSWORD=.*/BACKUP_DB_PASSWORD=$NEW_PW/" $B/env/backup-db.env $B/env/backup-server.env
unset NEW_PW
podman rm -f backup-server
# 再执行第 11 步
```

修改 env 文件后，必须删除并重新创建 backup-server 容器，新的环境变量才会生效。只执行 `podman restart` 时，容器仍使用旧密码。

## 注意事项

- **Web 用户 IP：** server 收到的 Web 请求都来自 pod 内的 nginx。所以审计日志和会话中的 Web 用户 IP 会显示为 `127.0.0.1`。按 IP 限制登录失败的功能也会把所有 Web 用户视为同一个来源；按用户名的限制不受影响。需要查看用户真实 IP 时，请查看 nginx 访问日志。Agent 直接连接 server，记录的仍然是真实 IP。
- **时区：** `timezone` 必须与 agent 主机一致，否则 Web 页面上的计划时间预测会出现偏差。
- **PostgreSQL 版本：** 本文使用与 CI 相同的 `postgres:17`。如果改用 18，需要把数据目录挂载点改为 `/var/lib/postgresql`。
- **请求大小：** nginx 的 `client_max_body_size` 为 `1m`，与 server 的 JSON 请求大小上限一致。
