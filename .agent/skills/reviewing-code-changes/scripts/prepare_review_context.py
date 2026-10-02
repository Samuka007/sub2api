#!/usr/bin/env python3
"""Prepare factual context for multi-agent code review."""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
from pathlib import Path
from typing import Any


DOC_NAMES = {
    "README",
    "README.md",
    "CHANGELOG.md",
    "MAINLINE.md",
    "EVIDENCE_INDEX.md",
}
DOC_PARTS = ("doc", "docs", "design", "plan", "plans", "adr", "architecture", "spec")
INTEGRATION_PATTERNS = [
    re.compile(r"https?://[A-Za-z0-9._~:/?#\[\]@!$&'()*+,;=%-]+"),
    re.compile(r"\b[A-Za-z0-9_./-]*(?:Service|Client|API|Api|SDK|Sdk|RPC|Rpc)[A-Za-z0-9_./-]*\b"),
    re.compile(r"\b(?:http|https|grpc|rpc|dubbo|mq|topic|queue|database|datasource|redis|mysql|odps)\b", re.I),
    re.compile(r"\b[A-Za-z][A-Za-z0-9_.-]{2,}\.(?:com|cn|net|org)\b"),
]


def run(cmd: list[str], cwd: Path, check: bool = False) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        cmd,
        cwd=str(cwd),
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=check,
    )


def git_root(cwd: Path) -> Path:
    proc = run(["git", "rev-parse", "--show-toplevel"], cwd)
    if proc.returncode != 0:
        raise SystemExit(f"not a git repository: {cwd}\n{proc.stderr.strip()}")
    return Path(proc.stdout.strip())


def git_output(args: list[str], cwd: Path) -> str:
    proc = run(["git", *args], cwd)
    if proc.returncode != 0:
        raise SystemExit(proc.stderr.strip())
    return proc.stdout


def untracked_files(cwd: Path) -> list[str]:
    output = git_output(["ls-files", "--others", "--exclude-standard", "-z"], cwd)
    return [path for path in output.split("\0") if path]


def render_untracked_diff(paths: list[str], cwd: Path) -> str:
    chunks: list[str] = []
    for path in paths:
        proc = run(
            ["git", "diff", "--no-index", "--binary", "--no-ext-diff", "--", "/dev/null", path],
            cwd,
        )
        if proc.returncode not in {0, 1}:
            raise SystemExit(proc.stderr.strip())
        chunks.append(proc.stdout)
    return "".join(chunks)


def collect_diff(mode: str, cwd: Path, base: str | None, head: str | None, diff_file: str | None) -> dict[str, Any]:
    if diff_file:
        return {"source": f"file:{diff_file}", "text": Path(diff_file).read_text(encoding="utf-8")}
    if mode == "staged":
        return {"source": "git diff --cached", "text": git_output(["diff", "--cached"], cwd)}
    if mode == "all":
        tracked_paths = [
            path
            for path in git_output(["diff", "HEAD", "--name-only", "-z"], cwd).split("\0")
            if path
        ]
        untracked_paths = untracked_files(cwd)
        return {
            "source": "git diff HEAD plus untracked files",
            "text": git_output(["diff", "HEAD"], cwd) + render_untracked_diff(untracked_paths, cwd),
            "files": sorted(dict.fromkeys([*tracked_paths, *untracked_paths])),
        }
    if mode == "range":
        if not base:
            raise SystemExit("--base is required for --mode range")
        rev = f"{base}..{head}" if head else base
        return {"source": f"git diff {rev}", "text": git_output(["diff", rev], cwd)}
    if mode == "pr":
        if base and head:
            return {"source": f"git diff {base}...{head}", "text": git_output(["diff", f"{base}...{head}"], cwd)}
        raise SystemExit("PR mode needs an externally fetched diff, or --base and --head. For AntCode PRs, use antcode-skill first.")
    raise SystemExit(f"unsupported mode: {mode}")


def changed_files(diff_text: str) -> list[str]:
    files: list[str] = []
    for line in diff_text.splitlines():
        if line.startswith("diff --git "):
            parts = line.split()
            if len(parts) >= 4:
                path = parts[3]
                if path.startswith("b/"):
                    path = path[2:]
                files.append(path)
    return sorted(dict.fromkeys(files))


def nearby_tests(files: list[str], root: Path) -> list[str]:
    candidates: set[str] = set()
    test_markers = ("test", "tests", "spec", "__tests__")
    all_files = [p for p in root.rglob("*") if p.is_file() and ".git" not in p.parts]
    for path in files:
        rel = Path(path)
        stem = rel.stem.lower()
        parent_parts = {part.lower() for part in rel.parts}
        if any(marker in parent_parts or marker in stem for marker in test_markers):
            candidates.add(path)
            continue
        for item in all_files:
            name = item.name.lower()
            parts = {part.lower() for part in item.relative_to(root).parts}
            if stem and stem in name and any(marker in parts or marker in name for marker in test_markers):
                candidates.add(str(item.relative_to(root)))
    return sorted(candidates)


def doc_files(root: Path, limit: int) -> list[str]:
    docs: list[str] = []
    for path in root.rglob("*"):
        if len(docs) >= limit:
            break
        if not path.is_file() or ".git" in path.parts:
            continue
        rel = path.relative_to(root)
        if path.name in DOC_NAMES or path.suffix.lower() in {".md", ".mdx", ".rst"}:
            lowered = [part.lower() for part in rel.parts]
            if path.name in DOC_NAMES or any(part in DOC_PARTS for part in lowered):
                docs.append(str(rel))
    return docs


def integration_terms(diff_text: str) -> list[str]:
    terms: set[str] = set()
    for pattern in INTEGRATION_PATTERNS:
        for match in pattern.findall(diff_text):
            if isinstance(match, tuple):
                match = match[0]
            term = str(match).strip("\"'`,);(")
            if 3 <= len(term) <= 120:
                terms.add(term)
    return sorted(terms)[:80]


def search_work_repos(terms: list[str], work_root: Path, limit: int) -> list[dict[str, str]]:
    if not work_root.exists() or not terms:
        return []
    results: list[dict[str, str]] = []
    seen: set[str] = set()
    terms_for_search = [term for term in terms if "/" not in term and "." not in term][:12]
    for repo in work_root.iterdir():
        if len(results) >= limit:
            break
        if not repo.is_dir() or not (repo / ".git").exists():
            continue
        for term in terms_for_search:
            proc = run(["rg", "-n", "--fixed-strings", "--glob", "!.git", "--max-count", "3", term, "."], repo)
            if proc.returncode == 0 and proc.stdout.strip():
                key = f"{repo}:{term}"
                if key not in seen:
                    results.append({"repo": str(repo), "term": term, "matches": "\n".join(proc.stdout.splitlines()[:5])})
                    seen.add(key)
                break
    return results


def yuque_hints(diff_text: str, terms: list[str]) -> dict[str, Any]:
    urls = sorted(set(re.findall(r"https?://[^\s)'\"<>]+yuque[^\s)'\"<>]*", diff_text)))
    commands: list[str] = []
    for url in urls[:5]:
        commands.append(f"yuque resolve {url} --json")
    for term in terms[:5]:
        commands.append(f"yuque search {json.dumps(term, ensure_ascii=False)} --page-size 10 --json")
    return {"urls": urls, "suggested_commands": commands}


def build_context(args: argparse.Namespace) -> dict[str, Any]:
    root = git_root(Path(args.cwd).resolve())
    diff = collect_diff(args.mode, root, args.base, args.head, args.diff_file)
    files = diff["files"] if "files" in diff else changed_files(diff["text"])
    terms = integration_terms(diff["text"])
    context = {
        "schema_version": "1.0",
        "repo": str(root),
        "mode": args.mode,
        "base": args.base,
        "head": args.head,
        "diff_source": diff["source"],
        "diff": diff["text"],
        "changed_files": files,
        "test_files": nearby_tests(files, root),
        "doc_files": doc_files(root, args.doc_limit),
        "integration_terms": terms,
        "work_repo_matches": search_work_repos(terms, Path(args.work_root).expanduser(), args.work_repo_limit)
        if args.integration_search
        else [],
        "yuque": yuque_hints(diff["text"], terms),
        "missing_evidence_policy": "If real integration repositories or documents are not found, report the missing evidence; do not mock or guess.",
    }
    return context


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mode", choices=["staged", "all", "range", "pr"], default="all")
    parser.add_argument("--cwd", default=os.getcwd())
    parser.add_argument("--base")
    parser.add_argument("--head")
    parser.add_argument("--diff-file")
    parser.add_argument("--out", required=True)
    parser.add_argument("--integration-search", action="store_true")
    parser.add_argument("--work-root", default="~/work")
    parser.add_argument("--work-repo-limit", type=int, default=20)
    parser.add_argument("--doc-limit", type=int, default=80)
    args = parser.parse_args()
    context = build_context(args)
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(context, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"wrote {out}")
    print(f"changed_files={len(context['changed_files'])} test_files={len(context['test_files'])} docs={len(context['doc_files'])}")


if __name__ == "__main__":
    main()
