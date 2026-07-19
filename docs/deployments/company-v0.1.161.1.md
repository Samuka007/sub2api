# company-v0.1.161.1 生产部署记录

## 发布标识

| 项目 | 值 |
| --- | --- |
| 部署时间 | `2026-07-19`（Asia/Shanghai） |
| 内部标签 | `company-v0.1.161.1` |
| Git 提交 | `987aa006e92f756ba4159036c06605d0afa021bf` |
| 官方基线 | `v0.1.161` / `19149ca196eeae4a4482e5299dc6fa4ba0b06c8c` |
| 程序版本 | `0.1.161+company.1` |
| Docker 镜像 | `sub2api:company-v0.1.161.1` |
| 镜像 ID | `sha256:112c5dbc31c83416eadf1851ddd016152b90e27a8ffd2227a060426f6af6d6e9` |
| 平台 | `linux/amd64` |

## 构建校验

| 构建输入 | SHA-256 |
| --- | --- |
| Git 源码归档 | `7c05ac177fa2a9851d3647c879dc82b769bdc1ab2bfb68438414e454ce322087` |
| 前端生产产物 | `e707e2b1490d4c0a8a094ee5ce0beee529dd1de4da273d831672497dc4c0ea7a` |
| Linux/amd64 二进制 | `49340bd41e1f08735fa774edef5803ab84be0995628282bf201b8d446ea94079` |
| 运行镜像配方 | `f90b902dbfb5f1343a4681584356a86ba50e7066eddc6afe7bc7c3f72ffb799a` |

服务器内存不足以稳定完成前端和 Go 全量容器构建。前端使用锁定的 `pnpm 9.15.9` 在本地构建；后端使用经官方 SHA-256 校验的 Go `1.26.5` 工具链交叉编译。二进制构建参数为 `CGO_ENABLED=0`、`GOOS=linux`、`GOARCH=amd64`、`-tags embed` 和 `-trimpath`。服务器在逐项校验上传文件后只组装运行镜像。

## 部署范围

只使用 Compose 的 `--no-deps` 模式重建 `sub2api` 应用容器。以下服务没有重建或修改：

- `sub2api-postgres`
- `sub2api-redis`
- `sub2api-moderation-adapter`

## 验证结果

- Docker 容器状态为 `running` 和 `healthy`，重启次数为 `0`。
- `/health` 返回 `{"status":"ok"}`。
- `/api/v1/settings/public` 返回版本 `0.1.161+company.1`。
- 前端首页返回 HTTP `200`。
- 未携带 API Key 请求 `/v1/models` 返回 HTTP `401`。
- 部署后真实 `/responses` 请求持续返回 HTTP `200`，内容审核链路正常工作。
- PostgreSQL、Redis 和审核适配器保持原容器与健康状态。

## 回滚

部署前应用镜像：

```text
sub2api:pricing-model-iq-v0.1.160-v2
sha256:78d81d5318638d859bf90bab39a305e356e8799fedb05ae6949e74ee59f2d0fd
```

如果新版本发生回归，只切换应用容器回上述固定镜像，不回滚或重建 PostgreSQL、Redis 数据卷和审核适配器。
