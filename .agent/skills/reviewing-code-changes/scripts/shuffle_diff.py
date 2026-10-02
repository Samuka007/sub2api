#!/usr/bin/env python3
"""Shuffle diff file blocks and hunks for review input randomization."""

from __future__ import annotations

import argparse
import json
import random
from pathlib import Path


def split_file_blocks(diff_text: str) -> list[str]:
    blocks: list[str] = []
    current: list[str] = []
    for line in diff_text.splitlines(keepends=True):
        if line.startswith("diff --git ") and current:
            blocks.append("".join(current))
            current = [line]
        else:
            current.append(line)
    if current:
        blocks.append("".join(current))
    return blocks


def split_hunks(block: str) -> tuple[str, list[str]]:
    lines = block.splitlines(keepends=True)
    header: list[str] = []
    hunks: list[list[str]] = []
    current: list[str] | None = None
    for line in lines:
        if line.startswith("@@ "):
            if current is not None:
                hunks.append(current)
            current = [line]
        elif current is None:
            header.append(line)
        else:
            current.append(line)
    if current is not None:
        hunks.append(current)
    return "".join(header), ["".join(hunk) for hunk in hunks]


def shuffle_diff(diff_text: str, passes: int, seed: int) -> dict[str, object]:
    blocks = split_file_blocks(diff_text)
    output_passes: list[dict[str, object]] = []
    for index in range(1, passes + 1):
        pass_seed = seed + index
        rng = random.Random(pass_seed)
        shuffled_blocks = blocks[:]
        rng.shuffle(shuffled_blocks)
        rendered: list[str] = []
        file_order: list[str] = []
        for block in shuffled_blocks:
            first = block.splitlines()[0] if block.splitlines() else ""
            file_order.append(first)
            header, hunks = split_hunks(block)
            rng.shuffle(hunks)
            rendered.append(header + "".join(hunks))
        output_passes.append(
            {
                "pass_id": index,
                "seed": pass_seed,
                "file_order": file_order,
                "block_count": len(shuffled_blocks),
                "diff": "".join(rendered),
            }
        )
    return {
        "schema_version": "1.0",
        "strategy": "deterministic_file_and_hunk_shuffle",
        "original_block_count": len(blocks),
        "passes": output_passes,
    }


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--context", help="review context JSON from prepare_review_context.py")
    parser.add_argument("--diff-file", help="raw diff file")
    parser.add_argument("--out", required=True)
    parser.add_argument("--passes", type=int, default=7)
    parser.add_argument("--seed", type=int, default=20260622)
    args = parser.parse_args()
    if args.context:
        context = json.loads(Path(args.context).read_text(encoding="utf-8"))
        diff_text = context.get("diff", "")
    elif args.diff_file:
        diff_text = Path(args.diff_file).read_text(encoding="utf-8")
    else:
        raise SystemExit("provide --context or --diff-file")
    result = shuffle_diff(diff_text, args.passes, args.seed)
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"wrote {out}")
    print(f"passes={len(result['passes'])} blocks={result['original_block_count']}")


if __name__ == "__main__":
    main()
