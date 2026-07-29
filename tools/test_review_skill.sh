#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/sub2api-review-skill-test.XXXXXX")"
trap 'rm -rf "${tmp_dir}"' EXIT

review_repo="${tmp_dir}/review-repo"
mkdir -p "${review_repo}"
git -C "${review_repo}" init -q
git -C "${review_repo}" config user.name "Review Skill Test"
git -C "${review_repo}" config user.email "review-skill-test@example.invalid"
printf 'base staged\n' >"${review_repo}/staged.txt"
printf 'base unstaged\n' >"${review_repo}/unstaged.txt"
git -C "${review_repo}" add staged.txt unstaged.txt
git -C "${review_repo}" commit -qm 'test: create review fixture'
printf 'changed staged\n' >"${review_repo}/staged.txt"
git -C "${review_repo}" add staged.txt
printf 'changed unstaged\n' >"${review_repo}/unstaged.txt"
printf 'new untracked\n' >"${review_repo}/untracked.txt"

python3 -m json.tool .agent/skills/reviewing-code-changes/test-prompts.json >/dev/null
PYTHONPYCACHEPREFIX="${tmp_dir}/pycache" python3 -m py_compile .agent/skills/reviewing-code-changes/scripts/*.py

python3 .agent/skills/reviewing-code-changes/scripts/prepare_review_context.py \
  --mode all \
  --cwd "${review_repo}" \
  --out "${tmp_dir}/context.json"
python3 .agent/skills/reviewing-code-changes/scripts/shuffle_diff.py \
  --context "${tmp_dir}/context.json" \
  --passes 2 \
  --seed 20260729 \
  --out "${tmp_dir}/shuffled.json"
printf '[]\n' >"${tmp_dir}/raw-findings.json"
printf '{}\n' >"${tmp_dir}/invalid-object.json"
printf '{"error":"reviewer timeout"}\n' >"${tmp_dir}/invalid-error.json"
printf '{"findings":null}\n' >"${tmp_dir}/invalid-null.json"
printf '[{"file":"incomplete.py"}]\n' >"${tmp_dir}/invalid-finding.json"
for invalid_file in "${tmp_dir}"/invalid-*.json; do
  if python3 .agent/skills/reviewing-code-changes/scripts/validate_findings.py "${invalid_file}" >/dev/null 2>&1; then
    printf 'Invalid reviewer output unexpectedly passed: %s\n' "${invalid_file}" >&2
    exit 1
  fi
done
python3 .agent/skills/reviewing-code-changes/scripts/validate_findings.py \
  "${tmp_dir}/raw-findings.json" \
  --out "${tmp_dir}/validated-findings.json"
python3 .agent/skills/reviewing-code-changes/scripts/fuse_findings.py \
  "${tmp_dir}/validated-findings.json" \
  --out "${tmp_dir}/fused-findings.json"

python3 - "${tmp_dir}" <<'PY'
import json
import sys
from pathlib import Path

root = Path(sys.argv[1])
context = json.loads((root / "context.json").read_text(encoding="utf-8"))
shuffled = json.loads((root / "shuffled.json").read_text(encoding="utf-8"))
validated = json.loads((root / "validated-findings.json").read_text(encoding="utf-8"))
fused = json.loads((root / "fused-findings.json").read_text(encoding="utf-8"))

assert context["schema_version"] == "1.0"
assert context["changed_files"] == ["staged.txt", "unstaged.txt", "untracked.txt"]
assert "changed staged" in context["diff"]
assert "changed unstaged" in context["diff"]
assert "new untracked" in context["diff"]
assert shuffled["original_block_count"] == 3
assert len(shuffled["passes"]) == 2
assert all(item["block_count"] == 3 for item in shuffled["passes"])
assert validated == {"schema_version": "1.0", "findings": []}
assert fused == {"schema_version": "1.0", "findings": []}
PY

printf 'Review Skill smoke tests passed\n'
