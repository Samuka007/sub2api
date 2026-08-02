# Langfuse 从服务器部署

此目录在从服务器部署 Langfuse v3、PostgreSQL、ClickHouse、Redis、MinIO，以及到主服务器的 Xray VLESS Reverse + REALITY 反向链路。

当前链路：

1. `xray-bridge` 从从服务器主动连接主服务器 `443/tcp`，从服务器无需开放入站端口。
2. 主服务器 Xray Portal 独占公网 `443`；普通 HTTPS 连接经 REALITY 失败回落转发到主机内部 Caddy `8443`，因此主站域名和请求仍由 Caddy 处理。
3. 主服务器 Caddy 将受信任的 `/api/public/otel*` 请求交给 host-network OTEL Collector 的 `172.28.0.1:4318`。
4. Collector 将批处理后的追踪导出到 `xray-portal` 的 Docker 网桥地址 `172.18.0.1:3100`。
5. Portal 把流量送入反向链路；从服务器 Bridge 只允许链路流量落到 `langfuse-ingest:3000`，默认出站为 `blackhole`。

数据库、Redis、ClickHouse、MinIO 均不映射宿主机端口。

Compose 不设置 CPU、容器内存、Node.js heap 或 Redis `maxmemory` 上限，与 Langfuse 官方 Compose 的资源策略一致。容量规划应保证至少满足官方最低要求，并根据 Worker CPU 使用率横向扩容；不要通过压低队列并发来适配资源充足的生产主机。

## Langfuse 读写隔离

Compose 将管理 UI/Public API 与 OTLP ingestion 拆成两个 Web 进程：`langfuse-web` 继续绑定宿主回环端口并负责数据库迁移，`langfuse-ingest` 不发布宿主端口，等待前者健康后接管 Xray反向链路中的 OTLP流量。Worker和 ingestion使用主 ClickHouse连接；UI支持的读查询通过内部 `clickhouse-read-proxy` 使用专用 `langfuse_read` 用户。

`langfuse_read` 使用 `readonly=2`，允许 Langfuse发送必要的 query settings；资源类 settings 同时设置正数下限和最大 constraints，超过执行时间、内存、扫描量、结果集、线程、临时磁盘或用户并发上限的请求直接失败，零值不能表示“不限”。默认单查询执行上限为 10 秒、内存 4 GiB、线程 4，全体读查询用户内存上限为 32 GiB、并发上限为 8。代理另有每秒 20 请求、burst 40 和 12 秒 upstream timeout。

read proxy不发布宿主端口，以非 root、只读根文件系统和移除 capabilities的方式运行。它清除来访 ClickHouse认证并注入独立的 `CLICKHOUSE_READ_PASSWORD`；access log只记录状态和耗时，error log提升为 `crit`，避免普通 upstream故障记录 URI、查询参数或 SQL。查询归属通过 `system.query_log.user = 'langfuse_read'` 识别，不依赖可能被 Langfuse覆盖的 `log_comment`。

## 初次生成

~~~sh
cd deploy/langfuse
LANGFUSE_PUBLIC_URL=https://langfuse.example.com ./generate-env.sh
~~~

`LANGFUSE_PUBLIC_URL` 必须是最终由 TLS 反向代理提供的 Langfuse 地址；生成脚本拒绝明文 HTTP 或缺失的地址。默认只把从服务器的 `3000` 绑定到 `127.0.0.1`，不会在局域网或公网直接暴露管理入口。

`generate-env.sh` 会同时生成：

- `.env`：Langfuse 密钥、Xray UUID 和 REALITY 密钥；
- `.xray/bridge.json`：从服务器配置；
- `main-server-xray/config.json`：主服务器配置；
- `main-server-xray/.env`：主服务器 Compose 参数。

这些文件包含密钥并被 Git 忽略。`.env` 权限为 600；Xray 配置文件位于权限为 700 的目录中，文件自身为 644，以便官方非 root 镜像只读挂载。生成器也会把不含密钥的 ClickHouse XML 配置显式设为 644，确保 UID 101 在严格 umask 的部署主机上仍可读取 bind mount。生产部署中，`LANGFUSE_INIT_PROJECT_PUBLIC_KEY` 和 `LANGFUSE_INIT_PROJECT_SECRET_KEY` 必须与主服务器 Sub2API 的 `MODEL_TRACING_PUBLIC_KEY` 和 `MODEL_TRACING_SECRET_KEY` 一致。生成器使用跨进程锁，并通过原子替换发布两端配置；如果另一个生成过程正在运行，本次调用会直接失败。

默认镜像均通过国内镜像站拉取并固定到不可变 digest。普通 `./manage.sh start` 不会拉取或升级镜像；只有显式执行 `./manage.sh pull` 才会下载配置中固定的镜像。变更镜像 digest 前应先完成备份、预发布验证和回滚演练。

已有 `.env` 的旧隧道部署直接运行以下命令即可补充 Xray 参数、读代理镜像和独立读密码，并迁移旧的默认端口；现有 Langfuse/数据库密钥和命名卷不会被重置：

~~~sh
./generate-xray-config.sh
~~~

## 443 端口复用

不要让 Caddy 和 Xray 通过 `SO_REUSEPORT` 随机抢占同一个 `443` 监听；这会让普通主站请求偶发落入 Xray。这里由 Xray 统一监听 `443`，REALITY 校验失败的普通 HTTPS 连接原样回落到 Caddy，属于协议级分流。

主服务器 Caddy 使用仓库中的 `deploy/Caddyfile` 时，先通过环境变量把站点改为内部 `8443`：

~~~sh
sudo systemctl edit caddy
# 在 [Service] 段加入：
# Environment="SUB2API_HTTPS_PORT=8443"
sudo systemctl daemon-reload
sudo env SUB2API_HTTPS_PORT=8443 caddy validate --config /etc/caddy/Caddyfile
sudo env SUB2API_HTTPS_PORT=8443 caddy reload --config /etc/caddy/Caddyfile
~~~

站点地址仍是 `api.sub2api.com`；`https_port` 只改变 Caddy 的内部监听端口，自动 HTTPS 跳转仍指向公网 `443`。请在主服务器防火墙中屏蔽公网 `8443`。如果 Caddy 不是通过 systemd 运行，请将 `SUB2API_HTTPS_PORT` 加入实际的 Caddy 服务环境，并先用 `caddy validate` 检查配置。确认主机网关上的 `8443` 可访问后，再启动 Portal。`8443` 只作为回落端口，不应对公网开放；公网只需放行 `443/tcp`。

## 主服务器 Portal

将整个 `main-server-xray` 目录安全传到主服务器，然后在主服务器执行：

~~~sh
cd main-server-xray
docker compose config --quiet
docker compose pull
docker compose up -d --pull never --force-recreate
~~~

主服务器需要允许 `443/tcp` 入站。Compose 将宿主机 `443` 映射到容器内非特权的 `31590`，避免给 rootless Xray 增加绑定特权端口的能力。`3100` 只绑定到 `172.18.0.1`，不暴露在公网；Caddy 回落端口为内部 `8443`。两端使用经南京大学 GHCR 镜像站代理并固定 digest 的 Xray `26.5.9`，不得单独修改其中一端。该版本提供用于仅放行 `langfuse-ingest:3000` 的 Freedom `finalRules`，并已通过真实 VLESS Reverse + REALITY 双端链路 smoke。更新生成配置后必须用上面的 `--force-recreate` 重建 Portal；从服务器的 `./xray-tunnel.sh start` 也会强制重建 Bridge，确保 bind mount 切换到原子替换后的新文件。

为 `LANGFUSE_PUBLIC_URL` 配置独立域名，并在主服务器 Caddy 中通过网桥端口提供 TLS 入口，例如：

~~~caddyfile
langfuse.example.com {
    reverse_proxy 172.18.0.1:3100
}
~~~

DNS、证书和域名必须与 `LANGFUSE_PUBLIC_URL` 一致。不要把从服务器的 `3000` 改回 `0.0.0.0`；本地应急访问可使用 SSH 端口转发到 `127.0.0.1:3000`。

## 从服务器启动与检查

~~~sh
./manage.sh pull
./manage.sh start
./manage.sh status
./manage.sh check
./xray-tunnel.sh status
./xray-tunnel.sh logs
~~~

提交或升级 Xray 配置前，在可访问 `api.sub2api.com:443` 的 Docker 主机运行真实隔离链路 smoke：

~~~sh
./deploy/tests/langfuse-stack-smoke.sh
./deploy/tests/langfuse-xray-e2e-smoke.sh
~~~

第一个脚本通过国内镜像拉起包含全部依赖的临时 Langfuse Compose 栈并验证 UI、ingestion和 Worker健康状态；第二个脚本创建带进程后缀的临时 Docker 网络和三个临时容器，验证 `Portal:3100 -> VLESS Reverse + REALITY -> Bridge -> langfuse-ingest:3000`。两者均自动清理且不占用固定宿主机端口。

Xray Bridge 属于 Compose 项目并设置 `restart: unless-stopped`，Docker 或 NAS 重启后会自动恢复。常用链路命令：

~~~sh
./xray-tunnel.sh render
./xray-tunnel.sh start
./xray-tunnel.sh stop
./xray-tunnel.sh status
./xray-tunnel.sh logs
~~~

## 从旧隧道切换

默认使用主服务器 `443/tcp`，不占用旧 SSHD 的 `31589`。建议按以下顺序切换：

1. 在主服务器先把 Caddy 切到内部 `8443`，验证站点和证书正常。
2. 在从服务器执行 `./generate-xray-config.sh`，把 `main-server-xray` 安全部署到主服务器。
3. 主服务器防火墙放行 `443/tcp`，并确认 `8443/tcp` 未对公网放行。
4. 启动 `xray-portal`，确认主站 HTTPS 仍可访问。
5. 在从服务器启动 `xray-bridge`，确认它持续运行且没有 REALITY/VLESS 错误。
6. 确认主服务器能够通过 `172.18.0.1:3100` 访问从服务器 Langfuse 健康检查。
7. 将主服务器 OTEL Collector 的 `LANGFUSE_ENDPOINT` 从 `http://127.0.0.1:3000/api/public/otel` 改为 `http://172.18.0.1:3100/api/public/otel`，重建 Collector 后验证磁盘队列持续出队。
8. 从主服务器验证 `/api/public/otel` 链路后，停止并删除从服务器旧隧道容器。
9. 确认不再需要回滚后，再删除旧 SSH 专用账号、授权 key 和 `deploy/langfuse/.ssh-tunnel`。`31589` 仍是主服务器 root 管理 SSH 端口，不应关闭。

不要执行 `docker compose down -v` 或 `docker-compose down -v`，这会删除持久化数据。

## 回滚

Xray 切流失败时，将 Collector 的 `LANGFUSE_ENDPOINT` 恢复为 `http://127.0.0.1:3000/api/public/otel` 并重建 Collector，再停止 `xray-portal`。切换稳定前不要删除旧 SSH 密钥。

## MinIO 限制

MinIO 当前仅供容器内部使用，文本追踪不受影响。需要浏览器多模态直传时，应为 MinIO 单独配置 TLS 反代并修改 `MINIO_EXTERNAL_ENDPOINT`。

## 安全边界

- 公网反向链路使用 VLESS Reverse、XTLS Vision 和 REALITY，不使用明文 VLESS。
- Xray UUID 只用于反向链路，不能复用于普通代理客户端。
- Bridge 的默认出站是 `blackhole`，专用 Freedom 出站仅允许 TCP 3000 并强制重定向到 `langfuse-ingest:3000`。
- Portal 的 Langfuse 服务端口只绑定主服务器 Docker 网桥；公网只暴露经过 UUID 和 REALITY 双重校验的 `443` 反向连接端口，Caddy 的 `8443` 回落端口不对公网开放。
