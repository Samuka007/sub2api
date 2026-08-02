#!/usr/bin/env python3
"""Validate repository governance artifacts and local-only document boundaries."""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path


REQUIRED_FILES = (
    ".upstream-version",
    "README.md",
    "README_CN.md",
    "README_JA.md",
    "AGENTS.md",
    "GIT_WORKFLOW.md",
    "DEV_GUIDE.md",
    "docs/README.md",
    ".github/ISSUE_TEMPLATE/change.md",
    ".github/pull_request_template.md",
    ".github/workflows/code-quality.yml",
    "Makefile",
    ".agent/skills/reviewing-code-changes/SKILL.md",
    ".agent/skills/reviewing-code-changes/test-prompts.json",
    ".agent/skills/reviewing-code-changes/agents/openai.yaml",
    ".agent/skills/reviewing-code-changes/references/reviewer-contracts.md",
    ".agent/skills/reviewing-code-changes/references/output-format.md",
    ".agent/skills/reviewing-code-changes/references/test-review-methods.md",
    ".agent/skills/reviewing-code-changes/scripts/prepare_review_context.py",
    ".agent/skills/reviewing-code-changes/scripts/shuffle_diff.py",
    ".agent/skills/reviewing-code-changes/scripts/validate_findings.py",
    ".agent/skills/reviewing-code-changes/scripts/fuse_findings.py",
    "tools/pr_gate.sh",
    "tools/test_review_skill.sh",
    "tools/check_pr_issue_ownership.sh",
    "tools/check_pr_issue_ownership_test.sh",
    "tools/test_check_repository_governance.py",
    "tools/check_pr_title.sh",
    "tools/test_check_pr_title.sh",
    "tools/generate_release_notes.sh",
    "tools/test_generate_release_notes.sh",
)

REQUIRED_SYMLINKS = {
    ".claude/skills": "../.agent/skills",
    ".codex/skills": "../.agent/skills",
}

FORBIDDEN_TRACKED_PREFIXES = (
    "docs/superpowers/",
    "openspec/changes/",
    "code-reviews/",
    "skills/",
)

IGNORED_WORK_SAMPLES = (
    "docs/superpowers/example.md",
    "openspec/changes/example/proposal.md",
    "code-reviews/review-context.json",
    "skills/example/SKILL.md",
)

UPSTREAM_READMES = ("README.md", "README_CN.md", "README_JA.md")


def git(root: Path, *args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["git", *args],
        cwd=root,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )


def repository_root() -> Path:
    result = git(Path.cwd(), "rev-parse", "--show-toplevel")
    if result.returncode != 0:
        raise RuntimeError(f"not a git repository: {result.stderr.strip()}")
    return Path(result.stdout.strip())


def tracked_files(root: Path) -> set[str]:
    result = git(root, "ls-files", "-z")
    if result.returncode != 0:
        raise RuntimeError(result.stderr.strip())
    return {item for item in result.stdout.split("\0") if item}


def upstream_version(root: Path) -> tuple[str, str]:
    values: dict[str, str] = {}
    for line in (root / ".upstream-version").read_text(encoding="utf-8").splitlines():
        key, separator, value = line.partition("=")
        if separator:
            values[key.strip()] = value.strip()
    return values.get("UPSTREAM_TAG", ""), values.get("UPSTREAM_COMMIT", "")


def main() -> int:
    try:
        root = repository_root()
        tracked = tracked_files(root)
    except RuntimeError as error:
        print(f"governance check failed: {error}", file=sys.stderr)
        return 1

    errors: list[str] = []

    for relative_path in REQUIRED_FILES:
        path = root / relative_path
        if not path.is_file() or path.stat().st_size == 0:
            errors.append(f"required non-empty file is missing: {relative_path}")

    try:
        upstream_tag, upstream_commit = upstream_version(root)
    except OSError as error:
        errors.append(f"cannot read .upstream-version: {error}")
    else:
        if not upstream_tag or not upstream_commit:
            errors.append(".upstream-version must define UPSTREAM_TAG and UPSTREAM_COMMIT")
        else:
            for relative_path in UPSTREAM_READMES:
                path = root / relative_path
                if not path.is_file():
                    continue
                text = path.read_text(encoding="utf-8")
                if upstream_tag not in text or (
                    relative_path == "README.md" and upstream_commit not in text
                ):
                    errors.append(
                        f"README upstream baseline does not match .upstream-version: {relative_path}"
                    )

    for relative_path, expected_target in REQUIRED_SYMLINKS.items():
        path = root / relative_path
        if not path.is_symlink():
            errors.append(f"required Skill discovery symlink is missing: {relative_path}")
        elif str(path.readlink()) != expected_target:
            errors.append(
                f"Skill discovery symlink has wrong target: {relative_path} -> {path.readlink()}"
            )

    for relative_path in sorted(tracked):
        if relative_path.endswith(".pyc") or "/__pycache__/" in relative_path:
            errors.append(f"generated Python cache is tracked: {relative_path}")
        for prefix in FORBIDDEN_TRACKED_PREFIXES:
            if relative_path.startswith(prefix):
                errors.append(f"local working artifact is tracked: {relative_path}")
                break

    for sample in IGNORED_WORK_SAMPLES:
        result = git(root, "check-ignore", "-q", sample)
        if result.returncode != 0:
            errors.append(f"local working artifact is not ignored: {sample}")

    if errors:
        print("Repository governance check failed:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1

    print("Repository governance check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
