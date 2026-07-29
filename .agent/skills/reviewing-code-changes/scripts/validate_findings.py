#!/usr/bin/env python3
"""Validate subagent finding JSON for reviewing-code-changes."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
from typing import Any


REQUIRED = {
    "file": str,
    "line": int,
    "role": str,
    "category": str,
    "severity": str,
    "confidence": (int, float),
    "evidence": str,
    "why_it_matters": str,
    "suggested_fix": str,
    "test_gap": str,
    "missing_evidence": str,
}
SEVERITIES = {"critical", "important", "minor"}
CATEGORIES = {
    "test_gap",
    "false_positive",
    "requirement_mismatch",
    "bug",
    "security",
    "concurrency",
    "performance",
    "maintainability",
    "integration_gap",
}


def load_findings(path: Path) -> list[dict[str, Any]]:
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, list):
        raise ValueError("top-level JSON must be an array")
    return data


def validate_item(item: Any, index: int) -> list[str]:
    errors: list[str] = []
    if not isinstance(item, dict):
        return [f"[{index}] finding must be an object"]
    for key, expected in REQUIRED.items():
        if key not in item:
            errors.append(f"[{index}] missing {key}")
        elif isinstance(item[key], bool) or not isinstance(item[key], expected):
            errors.append(f"[{index}] {key} has wrong type")
    if isinstance(item.get("line"), int) and item["line"] < 1:
        errors.append(f"[{index}] line must be >= 1")
    if item.get("severity") not in SEVERITIES:
        errors.append(f"[{index}] severity must be one of {sorted(SEVERITIES)}")
    if item.get("category") not in CATEGORIES:
        errors.append(f"[{index}] category must be one of {sorted(CATEGORIES)}")
    confidence = item.get("confidence")
    if isinstance(confidence, (int, float)) and not 0 <= float(confidence) <= 1:
        errors.append(f"[{index}] confidence must be between 0 and 1")
    if not str(item.get("evidence", "")).strip():
        errors.append(f"[{index}] evidence must not be empty")
    return errors


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("path")
    parser.add_argument("--out")
    args = parser.parse_args()
    path = Path(args.path)
    findings = load_findings(path)
    errors: list[str] = []
    for index, item in enumerate(findings):
        errors.extend(validate_item(item, index))
    if errors:
        print("\n".join(errors))
        raise SystemExit(1)
    normalized = {"schema_version": "1.0", "findings": findings}
    if args.out:
        Path(args.out).write_text(json.dumps(normalized, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"valid findings: {len(findings)}")


if __name__ == "__main__":
    main()
