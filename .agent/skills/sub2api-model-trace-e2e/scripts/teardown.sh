#!/usr/bin/env bash
# scripts/teardown.sh — 只清理当前 checkout 标记的 E2E 临时资源，保留仓库代码与 skills。
# 旧版无 owner label 的固定名称资源仅在 E2E_CLEAN_LEGACY=1 时迁移清理。
set -euo pipefail
SKILL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$(cd "$SKILL_DIR/../../.." && pwd)"

E2E_RUNTIME="${E2E_RUNTIME:-native}"
COLIMA_PROFILE="${COLIMA_PROFILE:-swebench}"
source "$SKILL_DIR/scripts/runtime_adapter.sh"
resolve_runtime
validate_native_runtime
resolve_e2e_owner

log() { printf '[teardown] %s\n' "$*" >&2; }

remove_legacy_e2e_resources
log "stopping and removing E2E resources owned by $E2E_OWNER_ID"
remove_owned_e2e_resources

log "teardown complete (binary still at $REPO_ROOT/.e2e-bin/sub2api)"
echo "OK"