# Langfuse 从服务器部署

此目录在从服务器部署 Langfuse v3、PostgreSQL、ClickHouse、Redis、MinIO，以及到主服务器的持久 SSH 反向隧道。

当前链路：

1. Langfuse Web 在从服务器监听 0.0.0.0:3000。
2. ssh-tunnel 容器连接主服务器 SSH 端口 31589，并将从服务器 127.0.0.1:3000 映射到主服务器 127.0.0.1:3000。
3. 主服务器 langfuse-tunnel-relay 容器只监听 Docker 网桥 172.18.0.1:3100。
4. 主服务器 Caddy 仅将 /api/public/otel* 反代到 172.18.0.1:3100；其他请求仍交给 Sub2API。

数据库、Redis、ClickHouse、MinIO 均不映射宿主机端口。

## 启动与检查

~~~sh
cd deploy/langfuse
./generate-env.sh
./manage.sh start
./manage.sh status
./manage.sh check
./reverse-tunnel.sh status
~~~

默认通过 DaoCloud 拉取 Docker Hub 镜像，Alpine 构建使用阿里云软件源。Chainguard MinIO 使用已验证可直连的官方 registry。

.env 保存数据库密码、Langfuse headless 初始化账号与项目 key，权限必须为 600，且不得提交。生产部署中，LANGFUSE_INIT_PROJECT_PUBLIC_KEY 和 LANGFUSE_INIT_PROJECT_SECRET_KEY 必须与主服务器 Sub2API 的 MODEL_TRACING_PUBLIC_KEY 和 MODEL_TRACING_SECRET_KEY 一致。

## 隧道运维

~~~sh
./reverse-tunnel.sh status
./reverse-tunnel.sh logs
./reverse-tunnel.sh stop
./reverse-tunnel.sh start
~~~

隧道属于 Compose 项目并设置 restart: unless-stopped，Docker 或 NAS 重启后会自动恢复。SSH key 目录由 SSH_DIR 配置，只读挂载到隧道容器。

主服务器中继容器同样设置 restart: unless-stopped。它的配置保存在：

~~~text
/root/sub2api/.local-deploy/langfuse-relay/Caddyfile
~~~

## 回滚

主服务器旧 Langfuse 容器已停止，但数据卷保留。需要回滚时：

1. 将主服务器 Caddyfile 恢复为 Caddyfile.pre-langfuse-slave。
2. 在主服务器 /root/sub2api/.local-deploy/langfuse 执行 docker-compose up -d。
3. 停止从服务器 ssh-tunnel 和主服务器 langfuse-tunnel-relay。

不要执行 docker compose down -v 或 docker-compose down -v，这会删除持久化数据。

## 限制

MinIO 当前仅供容器内部使用，文本追踪不受影响。需要浏览器多模态直传时，应为 MinIO 单独配置 TLS 反代并修改 MINIO_EXTERNAL_ENDPOINT。

## SSH 专用用户安全边界

反向隧道使用主服务器系统用户 langfuse-tunnel，不使用 root。该账户密码已锁定，只接受 deploy/langfuse/.ssh-tunnel 中的专用 Ed25519 key。

主服务器通过 SSHD Match 和 authorized_keys 双层限制：

- 只允许 publickey 认证；
- 只允许 remote TCP forwarding；
- 只允许监听主服务器 127.0.0.1:3000；
- 禁止普通命令、PTY、X11、agent forwarding 和用户 rc；
- GatewayPorts 为 no，转发端口不能暴露到公网。

私钥目录 deploy/langfuse/.ssh-tunnel 已被 Git 忽略，容器内只读挂载。常规 root SSH 管理入口仍保留，但隧道容器不再使用它。
