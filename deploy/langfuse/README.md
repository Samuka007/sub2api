# Langfuse 从服务器部署

此目录在从服务器部署 Langfuse v3、PostgreSQL、ClickHouse、Redis、MinIO，以及到主服务器的 Xray VLESS Reverse + REALITY 反向链路。

当前链路：

1. `xray-bridge` 从从服务器主动连接主服务器 `443/tcp`，从服务器无需开放入站端口。
2. 主服务器 Xray Portal 独占公网 `443`；普通 HTTPS 连接经 REALITY 失败回落转发到主机内部 Caddy `8443`，因此主站域名和请求仍由 Caddy 处理。
3. 主服务器 Caddy 将受信任的 `/api/public/otel*` 请求交给 host-network OTEL Collector 的 `172.28.0.1:4318`。
4. Collector 将批处理后的追踪导出到 `xray-portal` 的 Docker 网桥地址 `172.18.0.1:3100`。
5. Portal 把流量送入反向链路；从服务器 Bridge 只允许链路流量落到 `langfuse-web:3000`，默认出站为 `blackhole`。

数据库、Redis、ClickHouse、MinIO 均不映射宿主机端口。

Compose 不设置 CPU、容器内存、Node.js heap 或 Redis `maxmemory` 上限，与 Langfuse 官方 Compose 的资源策略一致。容量规划应保证至少满足官方最低要求，并根据 Worker CPU 使用率横向扩容；不要通过压低队列并发来适配资源充足的生产主机。

## 初次生成

~~~sh
cd deploy/langfuse
./generate-env.sh
~~~

`generate-env.sh` 会同时生成：

- `.env`：Langfuse 密钥、Xray UUID 和 REALITY 密钥；
- `.xray/bridge.json`：从服务器配置；
- `main-server-xray/config.json`：主服务器配置；
- `main-server-xray/.env`：主服务器 Compose 参数。

这些文件包含密钥并被 Git 忽略。`.env` 权限为 600；Xray 配置文件位于权限为 700 的目录中，文件自身为 644，以便官方非 root 镜像只读挂载。生产部署中，`LANGFUSE_INIT_PROJECT_PUBLIC_KEY` 和 `LANGFUSE_INIT_PROJECT_SECRET_KEY` 必须与主服务器 Sub2API 的 `MODEL_TRACING_PUBLIC_KEY` 和 `MODEL_TRACING_SECRET_KEY` 一致。

已有 `.env` 的旧隧道部署直接运行以下命令即可补充 Xray 参数并迁移旧的默认端口，不会重置 Langfuse 密钥：

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
docker compose up -d
~~~

主服务器需要允许 `443/tcp` 入站。Compose 将宿主机 `443` 映射到容器内非特权的 `31590`，避免给 rootless Xray 增加绑定特权端口的能力。`3100` 只绑定到 `172.18.0.1`，不暴露在公网；Caddy 回落端口为内部 `8443`。官方 Xray 镜像固定为 `ghcr.io/xtls/xray-core:26.5.9`，两端必须使用同一版本。

## 从服务器启动与检查

~~~sh
./manage.sh start
./manage.sh status
./manage.sh check
./xray-tunnel.sh status
./xray-tunnel.sh logs
~~~

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
- Bridge 的默认出站是 `blackhole`，专用 Freedom 出站仅允许 TCP 3000 并强制重定向到 `langfuse-web:3000`。
- Portal 的 Langfuse 服务端口只绑定主服务器 Docker 网桥；公网只暴露经过 UUID 和 REALITY 双重校验的 `443` 反向连接端口，Caddy 的 `8443` 回落端口不对公网开放。
