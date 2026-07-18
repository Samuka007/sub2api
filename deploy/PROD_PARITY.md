# Local production parity

Two Docker modes are provided and both reuse the existing `infra-postgres` and
`infra-redis` containers.

## Production parity

This mode runs the exact images exported from the production host:

- `sub2api:pricing-model-iq-v0.1.160-v2`
  (`sha256:78d81d5318638d859bf90bab39a305e356e8799fedb05ae6949e74ee59f2d0fd`)
- `sub2api-moderation-adapter:responses-v3-chunked`
  (`sha256:f801a83ea99394bf28612c672c9de997c9347bdb3fcaa9c16ea60bc2521edcdb`)

```powershell
cd C:\code\4sub\deploy
docker compose -f docker-compose.prod-parity.yml up -d
```

## Development parity

This mode builds the current source while retaining the production service
topology and runtime configuration.

```powershell
cd C:\code\4sub\deploy
$env:SUB2API_VERSION = "0.1.161+pricing-radar.model-iq.3"
$env:SUB2API_DEV_IMAGE = "sub2api:0.1.161-pricing-model-iq.3"
docker compose -f docker-compose.host-infra.yml up -d --build
```

Only one mode should run at a time because both intentionally use the same
container names and local port. Stop the active mode before switching:

```powershell
docker compose -f docker-compose.prod-parity.yml down
docker compose -f docker-compose.host-infra.yml down
```

The local database is isolated from production. The exact historical source
commit used for the production binary is unavailable, so production parity is
provided by the exported image; source-level debugging uses the reconstructed
development tree documented in `..\RECOVERY.md`.

## Local-only adaptations

- Production customer, account, group, and database records are not copied.
- Production content moderation is scoped to group IDs `4`, `5`, and `7`.
  Because those business records are intentionally absent locally, the local
  policy applies to all local groups while retaining the production thresholds,
  model, mode, keywords, worker count, and retention settings.
- The moderation adapter uses the authorized production API key and the public
  production relay (`https://sub.scitrace.cc`). Local moderation tests therefore
  consume production capacity and create production-side request records.
- Production has no stored `prompt_audit_config`, so prompt auditing remains at
  its production default (disabled). Content moderation and Model IQ are active.

## Build locally and run on the server

Build the server architecture locally and export the resulting image:

```powershell
$version = "0.1.161-pricing-model-iq.3"
docker build --platform linux/amd64 `
  --build-arg VERSION="0.1.161+pricing-radar.model-iq.3" `
  --tag "sub2api:$version" ..
docker image save --output "sub2api-$version.tar" "sub2api:$version"
```

After uploading and loading the image, set `SUB2API_IMAGE` in the server `.env`
and use the committed overlay to guarantee that Docker does not build or pull:

```bash
docker image load --input /tmp/sub2api-0.1.161-pricing-model-iq.3.tar
export SUB2API_IMAGE=sub2api:0.1.161-pricing-model-iq.3
docker compose -f docker-compose.yml -f docker-compose.server-image.yml \
  up -d --no-build sub2api
```
