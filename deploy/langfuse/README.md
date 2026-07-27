# Langfuse 从服务器部署

此目录在从服务器部署 Langfuse v3、PostgreSQL、ClickHouse、Redis、MinIO，以及到主服务器的 Xray VLESS Reverse + REALITY 反向链路。

当前链路：

1. `xray-bridge` 从从服务器主动连接主服务器 `31590/tcp`，从服务器无需开放入站端口。
2. 主服务器 Caddy 将受信任的 `/api/public/otel*` 请求交给 host-network OTEL Collector 的 `172.28.0.1:4318`。
3. Collector 将批处理后的追踪导出到 `xray-portal` 的 Docker 网桥地址 `172.18.0.1:3100`。
4. Portal 把流量送入反向链路；从服务器 Bridge 只允许链路流量落到 `langfuse-web:3000`，默认出站为 `blackhole`。

数据库、Redis、ClickHouse、MinIO 均不映射宿主机端口。

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

已有 `.env` 的 SSH 隧道部署直接运行以下命令即可补充 Xray 参数，不会重置 Langfuse 密钥：

~~~sh
./generate-xray-config.sh
~~~

## 主服务器 Portal

将整个 `main-server-xray` 目录安全传到主服务器，然后在主服务器执行：

~~~sh
cd main-server-xray
docker compose config --quiet
docker compose pull
docker compose up -d
~~~

主服务器需要允许 `31590/tcp` 入站。`3100` 只绑定到 `172.18.0.1`，不暴露在公网。官方 Xray 镜像固定为 `ghcr.io/xtls/xray-core:26.5.9`，两端必须使用同一版本。

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

## 从 SSH 隧道切换

默认使用新的 `31590/tcp`，避免与旧 SSHD 的 `31589` 冲突。建议按以下顺序切换：

1. 在从服务器执行 `./generate-xray-config.sh`，把 `main-server-xray` 安全部署到主服务器。
2. 主服务器防火墙放行 `31590/tcp`。
3. 在从服务器启动 `xray-bridge`，确认它持续运行且没有 REALITY/VLESS 错误。
4. 启动 `xray-portal`，确认主服务器能够通过 `172.18.0.1:3100` 访问从服务器 Langfuse 健康检查。
5. 将主服务器 OTEL Collector 的 `LANGFUSE_ENDPOINT` 从 `http://127.0.0.1:3000/api/public/otel` 改为 `http://172.18.0.1:3100/api/public/otel`，重建 Collector 后验证磁盘队列持续出队。
6. 从主服务器验证 `/api/public/otel` 链路后，停止并删除从服务器旧 `ssh-tunnel` 容器。
7. 删除旧 SSH 专用账号、授权 key 和 SSHD Match 配置。`31589` 仍是主服务器 root 管理 SSH 端口，不应关闭。
8. 确认不再需要回滚后，删除从服务器 `deploy/langfuse/.ssh-tunnel`。

不要执行 `docker compose down -v` 或 `docker-compose down -v`，这会删除持久化数据。

## 回滚

Xray 切流失败时，将 Collector 的 `LANGFUSE_ENDPOINT` 恢复为 `http://127.0.0.1:3000/api/public/otel` 并重建 Collector，再停止 `xray-portal`。切换稳定前不要删除旧 SSH 密钥。

## MinIO 限制

MinIO 当前仅供容器内部使用，文本追踪不受影响。需要浏览器多模态直传时，应为 MinIO 单独配置 TLS 反代并修改 `MINIO_EXTERNAL_ENDPOINT`。

## 安全边界

- 公网反向链路使用 VLESS Reverse、XTLS Vision 和 REALITY，不使用明文 VLESS。
- Xray UUID 只用于反向链路，不能复用于普通代理客户端。
- Bridge 的默认出站是 `blackhole`，专用 Freedom 出站仅允许 TCP 3000 并强制重定向到 `langfuse-web:3000`。
- Portal 服务端口只绑定主服务器 Docker 网桥，公网仅暴露经过 UUID 和 REALITY 双重校验的反向连接端口。
