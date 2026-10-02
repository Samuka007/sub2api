# company-v0.1.163.2 生产部署记录

## 发布标识

| 项目 | 值 |
| --- | --- |
| 部署时间 | `2026-07-23 04:04`（Asia/Shanghai） |
| 内部标签 | `company-v0.1.163.2` |
| Git 提交 | `fca6930ca8118ad829780135c7e0d44b9aa59c52` |
| 功能 PR | [`Jonesxq/4Sub2#7`](https://github.com/Jonesxq/4Sub2/pull/7)（Hermes quota recovery） |
| 官方基线 | `v0.1.163` / `d0bdd7e771636a8d315f542cafd39484f39bd60c` |
| 程序版本 | `0.1.163+company.2` |
| Docker 镜像 | `sub2api:company-v0.1.163.2` |
| 镜像摘要 | `sha256:feb4b330cbca27bb952ebc4333d158f491eee7bb1f22757a5a0e59bd8d42a886` |
| 镜像平台 | `linux/amd64` |
| 镜像归档 SHA-256 | `7ef28c4093d2a7217e4b8643572c2e7e3f2845d7e493d0b7361c76b8d41382c6` |

## 配置

生产 Compose 继续使用单个 `standard` 模式应用实例。仅增加 Hermes 环境变量映射并设置：

```text
QUOTA_RECOVERY_ENABLED=true
QUOTA_RECOVERY_INTERVAL_SECONDS=28800
```

其他 Hermes 参数沿用代码默认值：批大小 `50`、并发 `3`、单账号超时 `25` 秒、抖动上限 `10` 秒。

部署前配置备份：

```text
/home/ubuntu/sub2api-deploy/docker-compose.yml.bak.hermes-20260722T200311Z
/home/ubuntu/sub2api-deploy/.env.bak.hermes-20260722T200311Z
```

## 部署范围

只使用 `--no-build --no-deps` 重建 `sub2api` 应用容器。PostgreSQL 和 Redis 容器 ID 保持不变，未重建或修改数据卷。

## 验证结果

- 应用容器状态为 `running` 和 `healthy`，重启次数为 `0`。
- 容器镜像、镜像摘要、OCI revision 和版本标签与本记录一致。
- 容器运行配置为 `RUN_MODE=standard`、Hermes 已启用、周期为 `28800` 秒。
- `/health` 返回 `{"status":"ok"}`。
- 首页返回 HTTP `200`。
- 未携带 API Key 请求 `/v1/models` 返回 HTTP `401`。
- 公共设置接口返回版本 `0.1.163+company.2`。
- Hermes 首轮列出并检查 `23` 个候选账号：`recovered=0`、`exhausted=23`、`unknown=0`、`cas_misses=0`、`errors=0`。
- 部署后日志中 Hermes 运行失败、单实例连接丢失、panic 和 fatal 计数为 `0`。

## 回滚

部署前应用镜像保留为：

```text
sub2api:company-v0.1.163.1
sha256:4b737719d2a5ec8ccbee8061ecfcbbbfea01ed944ca178e121865e77b2cd521f
```

如需回滚，恢复上述 Compose 与 `.env` 备份，然后只重建应用容器：

```bash
cd /home/ubuntu/sub2api-deploy
cp -p docker-compose.yml.bak.hermes-20260722T200311Z docker-compose.yml
cp -p .env.bak.hermes-20260722T200311Z .env
docker compose --env-file .env -f docker-compose.yml up -d --no-build --no-deps sub2api
```

不得回滚或重建 PostgreSQL、Redis 数据卷。
