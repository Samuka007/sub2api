#!/usr/bin/env python3
"""Drive nested-fork + multi-compaction scenarios against local sub2api and judge results.

Requires local stack on :8080 / :3000 and env from /opt/sub2api-stack/.env
(SUB2API_ADMIN_*, LANGFUSE_*, CLICKHOUSE_PASSWORD). Does not print secrets.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

STACK = Path("/opt/sub2api-stack")
EXPORT = Path("/opt/sub2api-model-trace/scripts/langfuse_session_export.py")
OUT_DIR = Path("/opt/sub2api-model-trace/tmp")


def load_env() -> None:
    env_path = STACK / ".env"
    for line in env_path.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, v = line.split("=", 1)
        os.environ.setdefault(k.strip(), v.strip().strip('"').strip("'"))


def http_json(method: str, url: str, body: Optional[dict] = None, headers: Optional[dict] = None) -> Tuple[int, Any]:
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            raw = resp.read().decode()
            return resp.status, json.loads(raw) if raw else None
    except urllib.error.HTTPError as exc:
        raw = exc.read().decode(errors="replace")
        try:
            parsed = json.loads(raw) if raw else None
        except json.JSONDecodeError:
            parsed = raw
        return exc.code, parsed


def ch_query(sql: str) -> str:
    env = os.environ
    cmd = [
        "docker",
        "compose",
        "--env-file",
        str(STACK / ".env"),
        "-f",
        str(STACK / "compose.yml"),
        "exec",
        "-T",
        "clickhouse",
        "clickhouse-client",
        "--user",
        "clickhouse",
        "--password",
        env["CLICKHOUSE_PASSWORD"],
        "--query",
        sql,
    ]
    out = subprocess.check_output(cmd, env={**os.environ, "DOCKER_HOST": "unix:///run/docker.sock"})
    return out.decode()


def msg(role: str, mid: str, text: str) -> dict:
    return {
        "type": "message",
        "role": role,
        "id": mid,
        "content": [{"type": "input_text" if role == "user" else "output_text", "text": text}],
    }


def post_responses(
    api_key: str,
    session_id: str,
    turn_id: str,
    input_items: List[dict],
    *,
    fork_from: Optional[Tuple[str, str, str]] = None,
    request_kind: str = "turn",
) -> int:
    cm: Dict[str, Any] = {
        "session_id": session_id,
        "thread_id": session_id,
        "turn_id": turn_id,
    }
    meta = {
        "session_id": session_id,
        "thread_id": session_id,
        "turn_id": turn_id,
        "request_kind": request_kind,
    }
    body: Dict[str, Any] = {
        "model": "gpt-5.6-luna",
        "stream": False,
        "store": False,
        "input": input_items,
        "client_metadata": cm,
    }
    if fork_from:
        fs, fm, ft = fork_from
        body["fork_from_session_id"] = fs
        body["fork_from_message_id"] = fm
        body["fork_from_turn_id"] = ft
        cm["fork_from_session_id"] = fs
        cm["fork_from_message_id"] = fm
        cm["fork_from_turn_id"] = ft
        meta["forked_from_thread_id"] = fs
    cm["x-codex-turn-metadata"] = json.dumps(meta, separators=(",", ":"))
    headers = {
        "Authorization": f"Bearer {api_key}",
        "session-id": session_id,
        "thread-id": session_id,
        "X-Codex-Turn-Metadata": cm["x-codex-turn-metadata"],
    }
    code, _ = http_json("POST", "http://127.0.0.1:8080/v1/responses", body, headers)
    return code


def wait_chat(session_id: str, min_count: int, timeout: float = 45.0) -> int:
    deadline = time.time() + timeout
    last = 0
    while time.time() < deadline:
        last = int(
            ch_query(
                f"SELECT count() FROM observations WHERE name LIKE 'chat.%' "
                f"AND trace_id IN (SELECT id FROM traces WHERE session_id='{session_id}')"
            ).strip()
            or "0"
        )
        if last >= min_count:
            return last
        time.sleep(1.5)
    return last


def wait_name(session_id: str, name: str, min_count: int, timeout: float = 45.0) -> int:
    deadline = time.time() + timeout
    last = 0
    while time.time() < deadline:
        last = count_name(session_id, name)
        if last >= min_count:
            return last
        time.sleep(1.5)
    return last


def chat_summary(session_id: str) -> List[Tuple[str, str, str]]:
    raw = ch_query(
        f"""
SELECT name, metadata['message_id'],
  if(length(coalesce(nullIf(input,''), nullIf(output,'')))>80,
     concat(substring(coalesce(nullIf(input,''), nullIf(output,'')),1,80),'...'),
     coalesce(nullIf(input,''), nullIf(output,'')))
FROM observations
WHERE name LIKE 'chat.%'
  AND trace_id IN (SELECT id FROM traces WHERE session_id='{session_id}')
ORDER BY start_time
FORMAT TSV
"""
    ).strip()
    rows = []
    for line in raw.splitlines():
        parts = line.split("\t")
        if len(parts) >= 3:
            rows.append((parts[0], parts[1], parts[2]))
    return rows


def count_name(session_id: str, name: str) -> int:
    return int(
        ch_query(
            f"SELECT count() FROM observations WHERE name='{name}' "
            f"AND trace_id IN (SELECT id FROM traces WHERE session_id='{session_id}')"
        ).strip()
        or "0"
    )


def setup_api_key() -> Tuple[str, str, str, str]:
    """Returns api_key, token, group_id, key_id for cleanup."""
    email = os.environ["SUB2API_ADMIN_EMAIL"]
    password = os.environ["SUB2API_ADMIN_PASSWORD"]
    _, login = http_json(
        "POST",
        "http://127.0.0.1:8080/api/v1/auth/login",
        {"email": email, "password": password},
    )
    token = login["data"]["access_token"]
    _, compliance = http_json(
        "GET",
        "http://127.0.0.1:8080/api/v1/admin/compliance",
        headers={"Authorization": f"Bearer {token}"},
    )
    phrase = compliance["data"]["ack_phrase_en"]
    http_json(
        "POST",
        "http://127.0.0.1:8080/api/v1/admin/compliance/accept",
        {"phrase": phrase, "language": "en"},
        {"Authorization": f"Bearer {token}"},
    )
    run_id = f"complex-{int(time.time())}-{os.urandom(3).hex()}"
    group_name = f"{run_id}-g"
    key_name = f"{run_id}-k"
    _, g = http_json(
        "POST",
        "http://127.0.0.1:8080/api/v1/admin/groups",
        {
            "name": group_name,
            "description": "complex conversation scenarios",
            "platform": "openai",
            "rate_multiplier": 1,
            "is_exclusive": False,
            "status": "active",
        },
        {"Authorization": f"Bearer {token}"},
    )
    group_id = str(g["data"]["id"])
    http_json(
        "POST",
        "http://127.0.0.1:8080/api/v1/keys",
        {"name": key_name, "group_id": int(group_id)},
        {"Authorization": f"Bearer {token}"},
    )
    api_key = f"sk-complex-{os.urandom(16).hex()}"
    subprocess.check_call(
        [
            "docker",
            "compose",
            "--env-file",
            str(STACK / ".env"),
            "-f",
            str(STACK / "compose.yml"),
            "exec",
            "-T",
            "sub2api-postgres",
            "psql",
            "-U",
            "sub2api",
            "-d",
            "sub2api",
            "-v",
            "ON_ERROR_STOP=1",
            "-c",
            f"UPDATE api_keys SET key='{api_key}' WHERE name='{key_name}';",
        ],
        env={**os.environ, "DOCKER_HOST": "unix:///run/docker.sock"},
        stdout=subprocess.DEVNULL,
    )
    key_id = (
        subprocess.check_output(
            [
                "docker",
                "compose",
                "--env-file",
                str(STACK / ".env"),
                "-f",
                str(STACK / "compose.yml"),
                "exec",
                "-T",
                "sub2api-postgres",
                "psql",
                "-U",
                "sub2api",
                "-d",
                "sub2api",
                "-tAc",
                f"SELECT id FROM api_keys WHERE name='{key_name}' LIMIT 1",
            ],
            env={**os.environ, "DOCKER_HOST": "unix:///run/docker.sock"},
        )
        .decode()
        .strip()
    )
    # bump balance so requests aren't rejected for quota
    subprocess.check_call(
        [
            "docker",
            "compose",
            "--env-file",
            str(STACK / ".env"),
            "-f",
            str(STACK / "compose.yml"),
            "exec",
            "-T",
            "sub2api-postgres",
            "psql",
            "-U",
            "sub2api",
            "-d",
            "sub2api",
            "-v",
            "ON_ERROR_STOP=1",
            "-c",
            f"UPDATE users SET balance=1000000 WHERE email='{email}';",
        ],
        env={**os.environ, "DOCKER_HOST": "unix:///run/docker.sock"},
        stdout=subprocess.DEVNULL,
    )
    return api_key, token, group_id, key_id


def cleanup(token: str, group_id: str, key_id: str) -> None:
    if key_id:
        http_json("DELETE", f"http://127.0.0.1:8080/api/v1/keys/{key_id}", headers={"Authorization": f"Bearer {token}"})
    if group_id:
        http_json(
            "DELETE",
            f"http://127.0.0.1:8080/api/v1/admin/groups/{group_id}",
            headers={"Authorization": f"Bearer {token}"},
        )


def export_session(session_id: str, include_ancestors: bool) -> dict:
    env = os.environ.copy()
    env["LANGFUSE_HOST"] = "http://127.0.0.1:3000"
    cmd = [sys.executable, str(EXPORT), "--session-id", session_id]
    if include_ancestors:
        cmd.append("--include-ancestors")
    raw = subprocess.check_output(cmd, env=env)
    return json.loads(raw.decode())


def main() -> int:
    load_env()
    os.environ.setdefault("DOCKER_HOST", "unix:///run/docker.sock")
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    findings: List[str] = []
    ok = True

    def check(cond: bool, msg: str) -> None:
        nonlocal ok
        status = "PASS" if cond else "FAIL"
        findings.append(f"[{status}] {msg}")
        if not cond:
            ok = False

    api_key, token, group_id, key_id = setup_api_key()
    try:
        run = f"cx-{int(time.time())}-{os.urandom(2).hex()}"
        A, B, C = f"{run}-A", f"{run}-B", f"{run}-C"
        print(f"RUN sessions: A={A} B={B} C={C}")

        # --- Parent A: two turns of history ---
        post_responses(api_key, A, f"{run}-tA1", [msg("user", "msg-a-u1", f"{run} hello A1")])
        wait_chat(A, 1)
        post_responses(
            api_key,
            A,
            f"{run}-tA2",
            [
                msg("user", "msg-a-u1", f"{run} hello A1"),
                msg("assistant", "msg-a-a1", f"{run} reply A1"),
                msg("user", "msg-a-u2", f"{run} hello A2"),
            ],
        )
        wait_chat(A, 3)
        check(count_name(A, "chat.fork") == 0, "A has no fork")
        check(count_name(A, "chat.user") >= 2, "A has >=2 chat.user")
        check(count_name(A, "chat.assistant") >= 1, "A has chat.assistant from input history")

        # --- Child B: fork from A at msg-a-u2 ---
        post_responses(
            api_key,
            B,
            f"{run}-tB1",
            [
                msg("user", "msg-a-u2", f"{run} hello A2"),
                msg("user", "msg-b-u1", f"{run} hello B1"),
            ],
            fork_from=(A, "msg-a-u2", f"{run}-tA2"),
        )
        wait_chat(B, 2)
        check(count_name(B, "chat.fork") == 1, "B has exactly one chat.fork")
        check(count_name(A, "chat.fork") == 0, "A still has no fork after B created")

        # B turn 2: still sends fork fields (must not duplicate fork)
        post_responses(
            api_key,
            B,
            f"{run}-tB2",
            [
                msg("user", "msg-a-u2", f"{run} hello A2"),
                msg("user", "msg-b-u1", f"{run} hello B1"),
                msg("assistant", "msg-b-a1", f"{run} reply B1"),
                msg("user", "msg-b-u2", f"{run} hello B2"),
            ],
            fork_from=(A, "msg-a-u2", f"{run}-tA2"),
        )
        wait_chat(B, 4)
        check(count_name(B, "chat.fork") == 1, "B fork not duplicated on turn 2")

        # --- Grandchild C: nested fork from B at msg-b-u2 ---
        post_responses(
            api_key,
            C,
            f"{run}-tC1",
            [
                msg("user", "msg-b-u2", f"{run} hello B2"),
                msg("user", "msg-c-u1", f"{run} hello C1"),
            ],
            fork_from=(B, "msg-b-u2", f"{run}-tB2"),
        )
        wait_chat(C, 2)
        check(count_name(C, "chat.fork") == 1, "C has exactly one chat.fork (nested)")
        # fork metadata points to B
        fork_meta = ch_query(
            f"""
SELECT metadata['fork_from_session_id'], metadata['fork_from_message_id']
FROM observations
WHERE name='chat.fork'
  AND trace_id IN (SELECT id FROM traces WHERE session_id='{C}')
LIMIT 1
FORMAT TSV
"""
        ).strip().split("\t")
        check(fork_meta[0] == B, f"C fork_from_session is B (got {fork_meta[0]!r})")
        check(fork_meta[1] == "msg-b-u2", f"C fork_from_message is msg-b-u2 (got {fork_meta[1]!r})")

        # --- Multi compact on C ---
        # grow history
        post_responses(
            api_key,
            C,
            f"{run}-tC2",
            [
                msg("user", "msg-b-u2", f"{run} hello B2"),
                msg("user", "msg-c-u1", f"{run} hello C1"),
                msg("assistant", "msg-c-a1", f"{run} reply C1"),
                msg("user", "msg-c-u2", f"{run} hello C2"),
            ],
        )
        wait_chat(C, 4)
        # content-missing compact #1 (drop early messages)
        post_responses(
            api_key,
            C,
            f"{run}-tC3",
            [
                msg("user", "msg-c-u2", f"{run} hello C2"),
                msg("user", "msg-c-u3", f"{run} after compact1"),
            ],
        )
        wait_chat(C, 5)
        compact_after_1 = wait_name(C, "chat.compact", 1)
        check(compact_after_1 >= 1, f"C has content-missing chat.compact (>=1, got {compact_after_1})")

        # grow again then compact #2
        post_responses(
            api_key,
            C,
            f"{run}-tC4",
            [
                msg("user", "msg-c-u3", f"{run} after compact1"),
                msg("assistant", "msg-c-a3", f"{run} reply after compact1"),
                msg("user", "msg-c-u4", f"{run} hello C4"),
            ],
        )
        # tC4 also truncates older C history → another content-missing compact is expected
        wait_name(C, "chat.compact", compact_after_1 + 1)
        wait_chat(C, 7)
        post_responses(
            api_key,
            C,
            f"{run}-tC5",
            [msg("user", "msg-c-u4", f"{run} hello C4"), msg("user", "msg-c-u5", f"{run} after compact2")],
        )
        compact_after_2 = wait_name(C, "chat.compact", compact_after_1 + 2)
        check(compact_after_2 >= 2, f"C has further content-missing compact (>=2, got {compact_after_2})")

        # Codex-style request_kind compaction
        post_responses(
            api_key,
            C,
            f"{run}-tC6",
            [msg("user", "msg-c-u5", f"{run} after compact2")],
            request_kind="compaction",
        )
        compact_after_3 = wait_name(C, "chat.compact", compact_after_2 + 1)
        check(compact_after_3 > compact_after_2, f"C gained request_kind/compaction turn (before={compact_after_2}, after={compact_after_3})")

        # Early messages must still exist on C (anti-compression)
        early = ch_query(
            f"""
SELECT count() FROM observations
WHERE name='chat.user' AND metadata['message_id']='msg-c-u1'
  AND trace_id IN (SELECT id FROM traces WHERE session_id='{C}')
"""
        ).strip()
        check(early == "1", "msg-c-u1 survived multiple compacts on C")

        # Parent/child isolation
        check(count_name(A, "chat.compact") == 0, "A has no compact pollution")
        check(count_name(B, "chat.compact") == 0, "B has no compact pollution")

        # --- Export genealogy ---
        time.sleep(2)
        exported = export_session(C, include_ancestors=True)
        OUT_DIR.joinpath("complex_ancestors_export.json").write_text(
            json.dumps(exported, indent=2, ensure_ascii=False) + "\n"
        )
        events = exported["events"]
        ids = [e.get("message_id") for e in events]
        check(len(ids) == len(set(x for x in ids if x)), "export has no duplicate message_ids")
        check(all(e.get("name") != "chat.fork" for e in events), "export drops chat.fork markers")
        # nested chain should include A and B content markers
        texts = " ".join(
            str(e.get("input") or e.get("output") or "") for e in events
        )
        check(f"{run} hello A1" in texts, "export includes A1 from ancestor A")
        check(f"{run} hello B1" in texts or f"{run} hello B2" in texts, "export includes B lineage")
        check(f"{run} hello C1" in texts, "export includes C1")
        check(f"{run} after compact2" in texts or f"{run} hello C4" in texts, "export includes late C turns")
        compact_events = [e for e in events if e.get("name") == "chat.compact"]
        check(len(compact_events) >= 2, f"export keeps multiple compact markers (got {len(compact_events)})")

        # natural language sketch
        lines = [f"# Complex scenario transcript ({run})", "", f"- A `{A}`", f"- B `{B}` (fork←A@msg-a-u2)", f"- C `{C}` (fork←B@msg-b-u2)", ""]
        for e in events:
            name = e.get("name")
            content = e.get("input") if e.get("input") is not None else e.get("output")
            content = "" if content is None else str(content).replace("\n", " ")
            if len(content) > 120:
                content = content[:120] + "..."
            sess = e.get("session_id", "")
            tag = "A" if sess == A else "B" if sess == B else "C" if sess == C else "?"
            if name == "chat.user":
                lines.append(f"**[{tag} 用户]** {content}")
            elif name == "chat.assistant":
                lines.append(f"**[{tag} 助手]** {content}")
            elif name == "chat.compact":
                lines.append(f"**[{tag} 系统]** （发生上下文压缩）")
            else:
                lines.append(f"**[{tag} {name}]** {content}")
            lines.append("")
        OUT_DIR.joinpath("complex_ancestors_transcript.md").write_text("\n".join(lines) + "\n")

        # dump per-session summaries
        report = {
            "run": run,
            "sessions": {"A": A, "B": B, "C": C},
            "counts": {
                "A": {n: count_name(A, n) for n in ("chat.user", "chat.assistant", "chat.fork", "chat.compact")},
                "B": {n: count_name(B, n) for n in ("chat.user", "chat.assistant", "chat.fork", "chat.compact")},
                "C": {n: count_name(C, n) for n in ("chat.user", "chat.assistant", "chat.fork", "chat.compact")},
            },
            "export_event_count": len(events),
            "findings": findings,
            "verdict": "PASS" if ok else "FAIL",
        }
        OUT_DIR.joinpath("complex_scenario_report.json").write_text(json.dumps(report, indent=2) + "\n")

        print("\n--- findings ---")
        for f in findings:
            print(f)
        print(f"\nVERDICT: {'PASS' if ok else 'FAIL'}")
        print(f"report: {OUT_DIR/'complex_scenario_report.json'}")
        print(f"export: {OUT_DIR/'complex_ancestors_export.json'}")
        print(f"transcript: {OUT_DIR/'complex_ancestors_transcript.md'}")
        return 0 if ok else 2
    finally:
        cleanup(token, group_id, key_id)


if __name__ == "__main__":
    sys.exit(main())
