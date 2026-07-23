#!/usr/bin/env python3
"""Real Codex nested-fork + multi-compaction verification via app-server.

Flow:
  1) codex exec → parent session (through header proxy :18080)
  2) app-server: fork(parent)→B, turn, compact, turn
  3) app-server: fork(B)→C, turn, compact, compact, turn
  4) ClickHouse + export ancestors of C; self-judge

Requires: local sub2api :8080, Langfuse :3000, proxy :18080, env from stack .env.
Does not print credentials.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import threading
import time
from pathlib import Path
from queue import Empty, Queue
from typing import Any, Callable, Dict, List, Optional

STACK = Path("/opt/sub2api-stack")
OUT = Path("/opt/sub2api-model-trace/tmp")
EXPORT = Path("/opt/sub2api-model-trace/scripts/langfuse_session_export.py")
PROXY_SCRIPT = Path("/tmp/codex_header_proxy.py")
MARKER_FILE = Path("/tmp/codex_real_complex_marker.txt")
PARENT_FILE = Path("/tmp/codex_real_complex_parent.txt")
CHILD_B_FILE = Path("/tmp/codex_real_complex_B.txt")
CHILD_C_FILE = Path("/tmp/codex_real_complex_C.txt")


def load_env() -> None:
    for line in (STACK / ".env").read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, v = line.split("=", 1)
        os.environ.setdefault(k.strip(), v.strip().strip('"').strip("'"))
    os.environ.setdefault("DOCKER_HOST", "unix:///run/docker.sock")
    os.environ.setdefault("LANGFUSE_HOST", "http://127.0.0.1:3000")


def ensure_proxy() -> None:
    # cheap listen check
    import socket

    s = socket.socket()
    s.settimeout(0.3)
    try:
        s.connect(("127.0.0.1", 18080))
        s.close()
        return
    except OSError:
        pass
    if not PROXY_SCRIPT.exists():
        raise SystemExit(f"missing proxy script {PROXY_SCRIPT}")
    subprocess.Popen(
        [sys.executable, str(PROXY_SCRIPT)],
        stdout=open("/tmp/codex_header_proxy.log", "a"),
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    time.sleep(1)


def ch_query(sql: str) -> str:
    out = subprocess.check_output(
        [
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
            os.environ["CLICKHOUSE_PASSWORD"],
            "--query",
            sql,
        ]
    )
    return out.decode()


def count_name(session_id: str, name: str) -> int:
    return int(
        ch_query(
            f"SELECT count() FROM observations WHERE name='{name}' "
            f"AND trace_id IN (SELECT id FROM traces WHERE session_id='{session_id}')"
        ).strip()
        or "0"
    )


def wait_name(session_id: str, name: str, min_count: int, timeout: float = 90.0) -> int:
    deadline = time.time() + timeout
    last = 0
    while time.time() < deadline:
        last = count_name(session_id, name)
        if last >= min_count:
            return last
        time.sleep(2)
    return last


def wait_traces(session_id: str, min_count: int = 1, timeout: float = 90.0) -> int:
    deadline = time.time() + timeout
    last = 0
    while time.time() < deadline:
        last = int(
            ch_query(f"SELECT count() FROM traces WHERE session_id='{session_id}'").strip() or "0"
        )
        if last >= min_count:
            return last
        time.sleep(2)
    return last


class AppServer:
    def __init__(self) -> None:
        env = os.environ.copy()
        env["TERM"] = "dumb"
        env["PYTHONUNBUFFERED"] = "1"
        self.proc = subprocess.Popen(
            [
                "codex",
                "app-server",
                "--listen",
                "stdio://",
                "-c",
                'model_providers.OpenAI.base_url="http://127.0.0.1:18080"',
                "-c",
                'model="gpt-5.6-luna"',
                "-c",
                'approval_policy="never"',
                "-c",
                'sandbox_mode="danger-full-access"',
            ],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            bufsize=1,
            cwd="/opt/sub2api-model-trace",
            env=env,
        )
        assert self.proc.stdin and self.proc.stdout and self.proc.stderr
        self.out_q: Queue[dict] = Queue()
        self.err_lines: List[str] = []
        self.notifications: List[dict] = []
        self._next_id = 1
        threading.Thread(target=self._reader, daemon=True).start()
        threading.Thread(target=self._err_reader, daemon=True).start()

    def _reader(self) -> None:
        assert self.proc.stdout
        for line in self.proc.stdout:
            line = line.strip()
            if not line:
                continue
            try:
                self.out_q.put(json.loads(line))
            except json.JSONDecodeError:
                continue

    def _err_reader(self) -> None:
        assert self.proc.stderr
        for line in self.proc.stderr:
            self.err_lines.append(line.rstrip("\n"))

    def send(self, method: str, params: dict) -> int:
        req_id = self._next_id
        self._next_id += 1
        assert self.proc.stdin
        self.proc.stdin.write(json.dumps({"id": req_id, "method": method, "params": params}) + "\n")
        self.proc.stdin.flush()
        return req_id

    def wait_response(self, req_id: int, timeout: float = 90.0) -> dict:
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                msg = self.out_q.get(timeout=0.5)
            except Empty:
                if self.proc.poll() is not None:
                    raise RuntimeError(
                        f"app-server exited {self.proc.returncode}; stderr={self.err_lines[-40:]}"
                    )
                continue
            if "id" not in msg:
                self.notifications.append(msg)
                continue
            if msg.get("id") == req_id:
                return msg
        raise TimeoutError(f"timeout waiting response id={req_id}; stderr={self.err_lines[-40:]}")

    def wait_notification(self, method: str, predicate: Callable[[dict], bool], timeout: float = 180.0) -> dict:
        deadline = time.time() + timeout
        for msg in list(self.notifications):
            if msg.get("method") == method and predicate(msg):
                return msg
        while time.time() < deadline:
            try:
                msg = self.out_q.get(timeout=0.5)
            except Empty:
                if self.proc.poll() is not None:
                    raise RuntimeError(
                        f"app-server exited {self.proc.returncode}; stderr={self.err_lines[-40:]}"
                    )
                continue
            if "id" not in msg:
                self.notifications.append(msg)
                if msg.get("method") == method and predicate(msg):
                    return msg
                continue
        raise TimeoutError(f"timeout waiting notification {method}")

    def close(self) -> None:
        Path("/tmp/codex_complex_as_stderr.txt").write_text("\n".join(self.err_lines[-300:]))
        try:
            self.proc.terminate()
            self.proc.wait(timeout=5)
        except Exception:
            try:
                self.proc.kill()
            except Exception:
                pass


def create_parent(marker: str) -> str:
    prompt = (
        f"Reply with exactly one short sentence containing the marker {marker}-A1 and nothing else."
    )
    proc = subprocess.run(
        [
            "codex",
            "exec",
            "-c",
            'model_providers.OpenAI.base_url="http://127.0.0.1:18080"',
            "-c",
            'model="gpt-5.6-luna"',
            "-c",
            'approval_policy="never"',
            "-c",
            'sandbox_mode="danger-full-access"',
            prompt,
        ],
        cwd="/opt/sub2api-model-trace",
        capture_output=True,
        text=True,
        env={**os.environ, "TERM": "dumb"},
        timeout=180,
    )
    Path("/tmp/codex_complex_parent_out.txt").write_text(proc.stdout)
    Path("/tmp/codex_complex_parent_err.txt").write_text(proc.stderr)
    text = proc.stdout + "\n" + proc.stderr
    import re

    m = re.search(r"(019f[0-9a-f-]{30,})", text)
    if m:
        return m.group(1)
    # newest rollout
    sessions = sorted(
        Path("/root/.codex/sessions").rglob("rollout-*.jsonl"),
        key=lambda p: p.stat().st_mtime,
        reverse=True,
    )
    if not sessions:
        raise RuntimeError(f"parent session not found; exit={proc.returncode}")
    mm = re.search(r"(019f[0-9a-f-]{30,})", sessions[0].name)
    if not mm:
        raise RuntimeError(f"cannot parse session from {sessions[0]}")
    return mm.group(1)


def turn_start(server: AppServer, thread_id: str, text: str) -> str:
    resp = server.wait_response(
        server.send(
            "turn/start",
            {
                "threadId": thread_id,
                "input": [{"type": "text", "text": text}],
                "approvalPolicy": "never",
            },
        ),
        90,
    )
    if "error" in resp:
        raise RuntimeError(f"turn/start error: {resp['error']}")
    turn = (resp.get("result") or {}).get("turn") or {}
    turn_id = turn.get("id")
    server.wait_notification(
        "turn/completed",
        lambda m: ((m.get("params") or {}).get("turn") or {}).get("id") == turn_id
        or ((m.get("params") or {}).get("threadId") == thread_id),
        180,
    )
    return str(turn_id or "")


def compact_start(server: AppServer, thread_id: str) -> None:
    before = len(server.notifications)
    resp = server.wait_response(server.send("thread/compact/start", {"threadId": thread_id}), 60)
    if "error" in resp:
        raise RuntimeError(f"compact/start error: {resp['error']}")
    try:
        server.wait_notification(
            "thread/compacted",
            lambda m: (m.get("params") or {}).get("threadId") == thread_id,
            45,
        )
        return
    except TimeoutError:
        pass
    # compaction often completes as a turn
    try:
        server.wait_notification(
            "turn/completed",
            lambda m: len(server.notifications) > before,
            120,
        )
    except TimeoutError:
        # best-effort; caller will check Langfuse
        pass


def fork_thread(server: AppServer, parent_id: str) -> Dict[str, Any]:
    resp = server.wait_response(
        server.send(
            "thread/fork",
            {
                "threadId": parent_id,
                "cwd": "/opt/sub2api-model-trace",
                "approvalPolicy": "never",
            },
        ),
        90,
    )
    if "error" in resp:
        raise RuntimeError(f"thread/fork error: {resp['error']}")
    thread = (resp.get("result") or {}).get("thread") or {}
    return thread


def export_ancestors(session_id: str) -> dict:
    raw = subprocess.check_output(
        [sys.executable, str(EXPORT), "--session-id", session_id, "--include-ancestors"],
        env=os.environ.copy(),
    )
    return json.loads(raw.decode())


def main() -> int:
    load_env()
    OUT.mkdir(parents=True, exist_ok=True)
    ensure_proxy()
    Path("/tmp/codex_header_capture.jsonl").open("a").write("===CODEX_COMPLEX_REAL===\n")

    marker = f"CXREAL-{int(time.time())}-{os.urandom(3).hex()}"
    MARKER_FILE.write_text(marker)
    findings: List[str] = []
    ok = True

    def check(cond: bool, msg: str) -> None:
        nonlocal ok
        findings.append(f"[{'PASS' if cond else 'FAIL'}] {msg}")
        if not cond:
            ok = False

    print(f"MARKER={marker}", flush=True)
    parent = create_parent(marker)
    PARENT_FILE.write_text(parent)
    print(f"PARENT={parent}", flush=True)
    parent_traces = wait_traces(parent, 1, 90)
    check(parent_traces >= 1, f"parent has traces (got {parent_traces})")

    server = AppServer()
    try:
        server.wait_response(
            server.send("initialize", {"clientInfo": {"name": "codex-complex", "version": "0.0.1"}}),
            30,
        )

        # B = fork(parent)
        thread_b = fork_thread(server, parent)
        b = str(thread_b.get("id") or "")
        CHILD_B_FILE.write_text(b)
        print(f"B={b} forkedFrom={thread_b.get('forkedFromId')}", flush=True)
        check(bool(b), "fork B id present")
        check(thread_b.get("forkedFromId") == parent, "B.forkedFromId == parent")

        turn_start(
            server,
            b,
            f"Reply with exactly one short sentence containing the marker {marker}-B1 and nothing else.",
        )
        wait_traces(b, 1, 90)
        forks_b = wait_name(b, "chat.fork", 1, 90)
        check(forks_b == 1, f"B has exactly one chat.fork (got {forks_b})")

        compact_start(server, b)
        wait_name(b, "chat.compact", 1, 120)
        compact_b_1 = count_name(b, "chat.compact")
        check(compact_b_1 >= 1, f"B has >=1 chat.compact after first compact (got {compact_b_1})")

        turn_start(
            server,
            b,
            f"Reply with exactly one short sentence containing the marker {marker}-B2 and nothing else.",
        )
        wait_traces(b, 2, 90)
        check(count_name(b, "chat.fork") == 1, "B fork not duplicated after later turns")

        # C = nested fork(B)
        thread_c = fork_thread(server, b)
        c = str(thread_c.get("id") or "")
        CHILD_C_FILE.write_text(c)
        print(f"C={c} forkedFrom={thread_c.get('forkedFromId')}", flush=True)
        check(bool(c), "fork C id present")
        check(thread_c.get("forkedFromId") == b, "C.forkedFromId == B (nested)")

        turn_start(
            server,
            c,
            f"Reply with exactly one short sentence containing the marker {marker}-C1 and nothing else.",
        )
        wait_traces(c, 1, 90)
        forks_c = wait_name(c, "chat.fork", 1, 90)
        check(forks_c == 1, f"C has exactly one chat.fork (got {forks_c})")
        # fork metadata should point to B
        fork_row = ch_query(
            f"""
SELECT metadata['fork_from_session_id'], metadata['fork_from_message_id']
FROM observations
WHERE name='chat.fork' AND trace_id IN (SELECT id FROM traces WHERE session_id='{c}')
LIMIT 1 FORMAT TSV
"""
        ).strip().split("\t")
        if len(fork_row) >= 2:
            check(fork_row[0] == b, f"C chat.fork fork_from_session == B (got {fork_row[0]!r})")
            check(fork_row[1] == "thread", f"C chat.fork message_id is thread (got {fork_row[1]!r})")

        compact_start(server, c)
        wait_name(c, "chat.compact", 1, 120)
        compact_c_1 = count_name(c, "chat.compact")
        check(compact_c_1 >= 1, f"C first compact (>=1, got {compact_c_1})")

        compact_start(server, c)
        wait_name(c, "chat.compact", compact_c_1 + 1, 120)
        compact_c_2 = count_name(c, "chat.compact")
        check(compact_c_2 > compact_c_1, f"C second compact increased count ({compact_c_1}→{compact_c_2})")

        turn_start(
            server,
            c,
            f"Reply with exactly one short sentence containing the marker {marker}-C2 and nothing else.",
        )
        wait_traces(c, 2, 90)
        check(count_name(c, "chat.fork") == 1, "C fork not duplicated")
        check(count_name(parent, "chat.fork") == 0, "parent has no fork pollution")
        check(count_name(b, "chat.fork") == 1, "B still single fork after C created")

        # export genealogy of C
        time.sleep(3)
        exported = export_ancestors(c)
        OUT.joinpath("codex_complex_ancestors_export.json").write_text(
            json.dumps(exported, indent=2, ensure_ascii=False) + "\n"
        )
        events = exported.get("events") or []
        ids = [e.get("message_id") for e in events if e.get("message_id")]
        check(len(ids) == len(set(ids)), "export has no duplicate message_ids")
        check(all(e.get("name") != "chat.fork" for e in events), "export drops chat.fork")
        blob = " ".join(str(e.get("input") or e.get("output") or "") for e in events)
        check(f"{marker}-A1" in blob or f"{marker}-A1" in json.dumps(events), "export mentions A1 marker lineage")
        # B/C markers may be in assistant outputs or user inputs
        check(marker in blob, "export contains run marker")
        sessions_in_export = {e.get("session_id") for e in events}
        check(parent in sessions_in_export or b in sessions_in_export, "export includes ancestor session(s)")
        check(c in sessions_in_export, "export includes C events")

        # natural language transcript
        lines = [
            f"# Real Codex complex scenario ({marker})",
            "",
            f"- parent A: `{parent}`",
            f"- child B: `{b}` (fork←A)",
            f"- child C: `{c}` (fork←B, nested)",
            "",
        ]
        for e in events:
            name = e.get("name")
            content = e.get("input") if e.get("input") is not None else e.get("output")
            content = "" if content is None else str(content).replace("\n", " ")
            if "AGENTS.md" in content or content.startswith("# AGENTS"):
                content = "[AGENTS.md / environment bootstrap]"
            elif len(content) > 160:
                content = content[:160] + "..."
            sid = str(e.get("session_id") or "")
            tag = "A" if sid == parent else "B" if sid == b else "C" if sid == c else "?"
            if name == "chat.user":
                lines.append(f"**[{tag} 用户]** {content}")
            elif name == "chat.assistant":
                lines.append(f"**[{tag} 助手]** {content}")
            elif name == "chat.compact":
                lines.append(f"**[{tag} 系统]** （上下文压缩）")
            else:
                lines.append(f"**[{tag} {name}]** {content}")
            lines.append("")
        OUT.joinpath("codex_complex_ancestors_transcript.md").write_text("\n".join(lines) + "\n")

        report = {
            "marker": marker,
            "sessions": {"A": parent, "B": b, "C": c},
            "counts": {
                "A": {
                    n: count_name(parent, n)
                    for n in ("chat.user", "chat.assistant", "chat.fork", "chat.compact")
                },
                "B": {
                    n: count_name(b, n)
                    for n in ("chat.user", "chat.assistant", "chat.fork", "chat.compact")
                },
                "C": {
                    n: count_name(c, n)
                    for n in ("chat.user", "chat.assistant", "chat.fork", "chat.compact")
                },
            },
            "export_events": len(events),
            "findings": findings,
            "verdict": "PASS" if ok else "FAIL",
        }
        OUT.joinpath("codex_complex_report.json").write_text(json.dumps(report, indent=2) + "\n")
        print("\n--- findings ---", flush=True)
        for f in findings:
            print(f, flush=True)
        print(f"\nVERDICT: {report['verdict']}", flush=True)
        print(f"report: {OUT/'codex_complex_report.json'}", flush=True)
        print(f"transcript: {OUT/'codex_complex_ancestors_transcript.md'}", flush=True)
        return 0 if ok else 2
    finally:
        server.close()


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as exc:
        Path("/tmp/codex_complex_fatal.txt").write_text(repr(exc))
        print(f"FATAL: {exc}", file=sys.stderr)
        sys.exit(1)
