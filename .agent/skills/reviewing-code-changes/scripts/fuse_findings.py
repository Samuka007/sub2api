#!/usr/bin/env python3
"""Fuse validated findings and compute initial confidence scores."""

from __future__ import annotations

import argparse
import json
from collections import defaultdict
from pathlib import Path
from typing import Any


SEVERITY_RANK = {"critical": 3, "important": 2, "minor": 1}


def load_findings(path: Path) -> list[dict[str, Any]]:
    data = json.loads(path.read_text(encoding="utf-8"))
    if isinstance(data, dict):
        return list(data.get("findings", []))
    if isinstance(data, list):
        return data
    raise ValueError("expected array or {'findings': [...]}")


def key_for(item: dict[str, Any]) -> tuple[str, int, str]:
    return (str(item["file"]), int(item["line"]), str(item["category"]))


def score_bucket(items: list[dict[str, Any]]) -> float:
    score = max(float(item.get("confidence", 0)) for item in items)
    roles = {item.get("role") for item in items}
    categories = {item.get("category") for item in items}
    if len(roles) >= 2:
        score += 0.15
    if any(item.get("test_gap") for item in items):
        score += 0.20
    if any(item.get("missing_evidence") for item in items):
        score -= 0.10
    if categories & {"test_gap", "false_positive", "integration_gap"}:
        score += 0.05
    return max(0.0, min(1.0, round(score, 2)))


def fuse(findings: list[dict[str, Any]]) -> dict[str, Any]:
    buckets: dict[tuple[str, int, str], list[dict[str, Any]]] = defaultdict(list)
    for item in findings:
        buckets[key_for(item)].append(item)
    fused: list[dict[str, Any]] = []
    for idx, (key, items) in enumerate(sorted(buckets.items()), start=1):
        severity = max((item["severity"] for item in items), key=lambda value: SEVERITY_RANK[value])
        fused.append(
            {
                "finding_id": f"F-{idx:03d}",
                "file": key[0],
                "line": key[1],
                "category": key[2],
                "severity": severity,
                "confidence": score_bucket(items),
                "roles": sorted({str(item["role"]) for item in items}),
                "evidence_count": len(items),
                "evidence": [item["evidence"] for item in items],
                "why_it_matters": [item["why_it_matters"] for item in items],
                "suggested_fix": [item["suggested_fix"] for item in items if item.get("suggested_fix")],
                "test_gap": [item["test_gap"] for item in items if item.get("test_gap")],
                "missing_evidence": [item["missing_evidence"] for item in items if item.get("missing_evidence")],
                "raw_findings": items,
            }
        )
    fused.sort(key=lambda item: (SEVERITY_RANK[item["severity"]], item["confidence"], item["evidence_count"]), reverse=True)
    return {"schema_version": "1.0", "findings": fused}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("path")
    parser.add_argument("--out", required=True)
    args = parser.parse_args()
    findings = load_findings(Path(args.path))
    result = fuse(findings)
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"wrote {out}")
    print(f"fused_findings={len(result['findings'])}")


if __name__ == "__main__":
    main()
