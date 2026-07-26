#!/usr/bin/env python3
"""Real Codex ~20-turn scenario with forks, compacts, and cross-branch activity.

Topology (turns are approximate; app-server drives each turn/compact):

  A: t1-t3 → compact → t4-t5
       ├─ fork→ B: t6-t8 → compact → t9-t10
       │         └─ fork→ C: t11-t12 → compact → t13 → compact → t14
       └─ fork→ D (sibling of B): t15-t16 → compact → t17
  Then cross back:
       A: t18
       B: t19
       C: t20

Requires: sub2api :8080, Langfuse :3000, proxy :18080, stack .env.
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
from typing import Any, Callable, Dict, List

STACK = Path("/opt/sub2api-stack")
OUT = Path("/opt/sub2api-model-trace/tmp")
EXPORT = Path("/opt/sub2api-model-trace/scripts/langfuse_session_export.py")
PROXY = Path("/tmp/codex_header_proxy.py")


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
    import socket

    s = socket.socket()
    s.settimeout(0.3)
    try:
        s.connect(("127.0.0.1", 18080))
        s.close()
        return
    except OSError:
        pass
    subprocess.Popen(
        [sys.executable, str(PROXY)],
        stdout=open("/tmp/codex_header_proxy.log", "a"),
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
    time.sleep(1)


def ch_query(sql: str) -> str:
    return subprocess.check_output(
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
    ).decode()


def count_name(session_id: str, name: str) -> int:
    return int(
        ch_query(
            f"SELECT count() FROM observations WHERE name='{name}' "
            f"AND trace_id IN (SELECT id FROM traces WHERE session_id='{session_id}')"
        ).strip()
        or "0"
    )


def wait_traces(session_id: str, min_count: int, timeout: float = 120.0) -> int:
    deadline = time.time() + timeout
    last = 0
    while time.time() < deadline:
        last = int(ch_query(f"SELECT count() FROM traces WHERE session_id='{session_id}'").strip() or "0")
        if last >= min_count:
            return last
        time.sleep(2)
    return last


def wait_name(session_id: str, name: str, min_count: int, timeout: float = 120.0) -> int:
    deadline = time.time() + timeout
    last = 0
    while time.time() < deadline:
        last = count_name(session_id, name)
        if last >= min_count:
            return last
        time.sleep(2)
    return last


class AppServer:
    def __init__(self) -> None:
        env = os.environ.copy()
        env["TERM"] = "dumb"
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
        self.q: Queue[dict] = Queue()
        self.err: List[str] = []
        self.notes: List[dict] = []
        self._id = 1
        threading.Thread(target=self._read, daemon=True).start()
        threading.Thread(target=self._eread, daemon=True).start()

    def _read(self) -> None:
        assert self.proc.stdout
        for line in self.proc.stdout:
            line = line.strip()
            if not line:
                continue
            try:
                self.q.put(json.loads(line))
            except json.JSONDecodeError:
                continue

    def _eread(self) -> None:
        assert self.proc.stderr
        for line in self.proc.stderr:
            self.err.append(line.rstrip("\n"))

    def send(self, method: str, params: dict) -> int:
        rid = self._id
        self._id += 1
        assert self.proc.stdin
        self.proc.stdin.write(json.dumps({"id": rid, "method": method, "params": params}) + "\n")
        self.proc.stdin.flush()
        return rid

    def wait_resp(self, rid: int, timeout: float = 90.0) -> dict:
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                msg = self.q.get(timeout=0.5)
            except Empty:
                if self.proc.poll() is not None:
                    raise RuntimeError(f"app-server exit {self.proc.returncode}: {self.err[-30:]}")
                continue
            if "id" not in msg:
                self.notes.append(msg)
                continue
            if msg.get("id") == rid:
                return msg
        raise TimeoutError(f"timeout resp {rid}")

    def wait_note(self, method: str, pred: Callable[[dict], bool], timeout: float = 180.0) -> dict:
        deadline = time.time() + timeout
        for msg in list(self.notes):
            if msg.get("method") == method and pred(msg):
                return msg
        while time.time() < deadline:
            try:
                msg = self.q.get(timeout=0.5)
            except Empty:
                if self.proc.poll() is not None:
                    raise RuntimeError(f"app-server exit {self.proc.returncode}: {self.err[-30:]}")
                continue
            if "id" not in msg:
                self.notes.append(msg)
                if msg.get("method") == method and pred(msg):
                    return msg
        raise TimeoutError(f"timeout note {method}")

    def close(self) -> None:
        Path("/tmp/codex_20turn_stderr.txt").write_text("\n".join(self.err[-400:]))
        try:
            self.proc.terminate()
            self.proc.wait(timeout=5)
        except Exception:
            try:
                self.proc.kill()
            except Exception:
                pass


def start_thread(server: AppServer) -> str:
    resp = server.wait_resp(
        server.send(
            "thread/start",
            {
                "cwd": "/opt/sub2api-model-trace",
                "approvalPolicy": "never",
            },
        ),
        60,
    )
    if "error" in resp:
        raise RuntimeError(f"thread/start error: {resp['error']}")
    thread = (resp.get("result") or {}).get("thread") or {}
    tid = str(thread.get("id") or "")
    if not tid:
        raise RuntimeError(f"thread/start missing id: {resp}")
    return tid


def turn(server: AppServer, thread_id: str, text: str, log: List[str], label: str) -> None:
    print(f"TURN {label} thread={thread_id[-12:]}", flush=True)
    resp = server.wait_resp(
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
        raise RuntimeError(f"{label} turn/start error: {resp['error']}")
    tid = ((resp.get("result") or {}).get("turn") or {}).get("id")
    server.wait_note(
        "turn/completed",
        lambda m: ((m.get("params") or {}).get("turn") or {}).get("id") == tid
        or ((m.get("params") or {}).get("threadId") == thread_id),
        240,
    )
    log.append(f"turn:{label}:{thread_id}")


def compact(server: AppServer, thread_id: str, log: List[str], label: str) -> None:
    print(f"COMPACT {label} thread={thread_id[-12:]}", flush=True)
    before = len(server.notes)
    resp = server.wait_resp(server.send("thread/compact/start", {"threadId": thread_id}), 60)
    if "error" in resp:
        raise RuntimeError(f"{label} compact error: {resp['error']}")
    try:
        server.wait_note(
            "thread/compacted",
            lambda m: (m.get("params") or {}).get("threadId") == thread_id,
            40,
        )
    except TimeoutError:
        try:
            server.wait_note("turn/completed", lambda m: len(server.notes) > before, 120)
        except TimeoutError:
            print(f"COMPACT {label} wait soft-timeout", flush=True)
    log.append(f"compact:{label}:{thread_id}")


def fork(server: AppServer, parent_id: str, log: List[str], label: str) -> Dict[str, Any]:
    print(f"FORK {label} from={parent_id[-12:]}", flush=True)
    resp = server.wait_resp(
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
        raise RuntimeError(f"{label} fork error: {resp['error']}")
    thread = (resp.get("result") or {}).get("thread") or {}
    log.append(f"fork:{label}:{thread.get('id')}:from:{parent_id}")
    return thread


def export_ancestors(session_id: str) -> dict:
    return json.loads(
        subprocess.check_output(
            [sys.executable, str(EXPORT), "--session-id", session_id, "--include-ancestors"],
            env=os.environ.copy(),
        ).decode()
    )


def write_transcript(path: Path, title: str, sessions: Dict[str, str], events: List[dict]) -> None:
    rev = {v: k for k, v in sessions.items()}
    lines = [f"# {title}", ""]
    for k, v in sessions.items():
        lines.append(f"- {k}: `{v}`")
    lines.append("")
    for e in events:
        name = e.get("name")
        content = e.get("input") if e.get("input") is not None else e.get("output")
        content = "" if content is None else str(content).replace("\n", " ")
        if "AGENTS.md" in content or content.startswith("# AGENTS"):
            content = "[AGENTS.md / environment bootstrap]"
        elif name == "chat.system":
            preview = content[:240] + ("..." if len(content) > 240 else "")
            content = f"{preview} _(len={len(content)})_"
        elif len(content) > 180:
            content = content[:180] + "..."
        tag = rev.get(str(e.get("session_id") or ""), "?")
        if name == "chat.system":
            lines.append(f"**[{tag} 系统]** {content}")
        elif name == "chat.user":
            lines.append(f"**[{tag} 用户]** {content}")
        elif name == "chat.assistant":
            lines.append(f"**[{tag} 助手]** {content}")
        elif name == "chat.compact":
            lines.append(f"**[{tag} 系统]** （上下文压缩）")
        else:
            lines.append(f"**[{tag} {name}]** {content}")
        lines.append("")
    path.write_text("\n".join(lines) + "\n")


def main() -> int:
    load_env()
    OUT.mkdir(parents=True, exist_ok=True)
    ensure_proxy()
    Path("/tmp/codex_header_capture.jsonl").open("a").write("===CODEX_20TURN===\n")

    marker = f"T20-{int(time.time())}-{os.urandom(2).hex()}"
    Path("/tmp/codex_20turn_marker.txt").write_text(marker)
    ops: List[str] = []
    findings: List[str] = []
    ok = True

    def check(cond: bool, msg: str) -> None:
        nonlocal ok
        findings.append(f"[{'PASS' if cond else 'FAIL'}] {msg}")
        if not cond:
            ok = False

    print(f"MARKER={marker}", flush=True)

    server = AppServer()
    try:
        server.wait_resp(
            server.send("initialize", {"clientInfo": {"name": "codex-20turn", "version": "0.0.1"}}),
            30,
        )

        # Entire tree lives in one app-server process (exec sessions are not visible here).
        a = start_thread(server)
        Path("/tmp/codex_20turn_A.txt").write_text(a)
        print(f"A={a}", flush=True)
        turn(
            server,
            a,
            f"Reply with exactly one short sentence containing the marker {marker}-A1 and nothing else.",
            ops,
            "A1",
        )
        wait_traces(a, 1, 120)
        turn(
            server,
            a,
            f"Reply with exactly one short sentence containing the marker {marker}-A2 and nothing else.",
            ops,
            "A2",
        )
        turn(
            server,
            a,
            f"Reply with exactly one short sentence containing the marker {marker}-A3 and nothing else.",
            ops,
            "A3",
        )
        compact(server, a, ops, "A-c1")
        turn(
            server,
            a,
            f"Reply with exactly one short sentence containing the marker {marker}-A4 and nothing else.",
            ops,
            "A4",
        )
        turn(
            server,
            a,
            f"Reply with exactly one short sentence containing the marker {marker}-A5 and nothing else.",
            ops,
            "A5",
        )

        # Fork B from A
        tb = fork(server, a, ops, "B")
        b = str(tb.get("id") or "")
        Path("/tmp/codex_20turn_B.txt").write_text(b)
        check(tb.get("forkedFromId") == a, "B forkedFrom A")
        turn(
            server,
            b,
            f"Reply with exactly one short sentence containing the marker {marker}-B6 and nothing else.",
            ops,
            "B6",
        )
        turn(
            server,
            b,
            f"Reply with exactly one short sentence containing the marker {marker}-B7 and nothing else.",
            ops,
            "B7",
        )
        turn(
            server,
            b,
            f"Reply with exactly one short sentence containing the marker {marker}-B8 and nothing else.",
            ops,
            "B8",
        )
        compact(server, b, ops, "B-c1")
        turn(
            server,
            b,
            f"Reply with exactly one short sentence containing the marker {marker}-B9 and nothing else.",
            ops,
            "B9",
        )
        turn(
            server,
            b,
            f"Reply with exactly one short sentence containing the marker {marker}-B10 and nothing else.",
            ops,
            "B10",
        )

        # Nested fork C from B
        tc = fork(server, b, ops, "C")
        c = str(tc.get("id") or "")
        Path("/tmp/codex_20turn_C.txt").write_text(c)
        check(tc.get("forkedFromId") == b, "C forkedFrom B (nested)")
        turn(
            server,
            c,
            f"Reply with exactly one short sentence containing the marker {marker}-C11 and nothing else.",
            ops,
            "C11",
        )
        turn(
            server,
            c,
            f"Reply with exactly one short sentence containing the marker {marker}-C12 and nothing else.",
            ops,
            "C12",
        )
        compact(server, c, ops, "C-c1")
        turn(
            server,
            c,
            f"Reply with exactly one short sentence containing the marker {marker}-C13 and nothing else.",
            ops,
            "C13",
        )
        compact(server, c, ops, "C-c2")
        turn(
            server,
            c,
            f"Reply with exactly one short sentence containing the marker {marker}-C14 and nothing else.",
            ops,
            "C14",
        )

        # Sibling fork D from A (cross branch)
        td = fork(server, a, ops, "D")
        d = str(td.get("id") or "")
        Path("/tmp/codex_20turn_D.txt").write_text(d)
        check(td.get("forkedFromId") == a, "D forkedFrom A (sibling of B)")
        turn(
            server,
            d,
            f"Reply with exactly one short sentence containing the marker {marker}-D15 and nothing else.",
            ops,
            "D15",
        )
        turn(
            server,
            d,
            f"Reply with exactly one short sentence containing the marker {marker}-D16 and nothing else.",
            ops,
            "D16",
        )
        compact(server, d, ops, "D-c1")
        turn(
            server,
            d,
            f"Reply with exactly one short sentence containing the marker {marker}-D17 and nothing else.",
            ops,
            "D17",
        )

        # Cross back to older lines
        turn(
            server,
            a,
            f"Reply with exactly one short sentence containing the marker {marker}-A18 and nothing else.",
            ops,
            "A18",
        )
        turn(
            server,
            b,
            f"Reply with exactly one short sentence containing the marker {marker}-B19 and nothing else.",
            ops,
            "B19",
        )
        turn(
            server,
            c,
            f"Reply with exactly one short sentence containing the marker {marker}-C20 and nothing else.",
            ops,
            "C20",
        )

        # Allow ingest
        time.sleep(8)
        sessions = {"A": a, "B": b, "C": c, "D": d}
        for name, sid in sessions.items():
            wait_traces(sid, 1, 60)

        # Structural checks
        check(count_name(a, "chat.fork") == 0, "A has no chat.fork")
        check(count_name(b, "chat.fork") == 1, f"B fork==1 (got {count_name(b, 'chat.fork')})")
        check(count_name(c, "chat.fork") == 1, f"C fork==1 (got {count_name(c, 'chat.fork')})")
        check(count_name(d, "chat.fork") == 1, f"D fork==1 (got {count_name(d, 'chat.fork')})")
        check(count_name(a, "chat.compact") >= 1, f"A compact>=1 (got {count_name(a, 'chat.compact')})")
        check(count_name(b, "chat.compact") >= 1, f"B compact>=1 (got {count_name(b, 'chat.compact')})")
        check(count_name(c, "chat.compact") >= 2, f"C compact>=2 (got {count_name(c, 'chat.compact')})")
        check(count_name(d, "chat.compact") >= 1, f"D compact>=1 (got {count_name(d, 'chat.compact')})")
        check(count_name(a, "chat.assistant") >= 1, f"A assistant>=1 (got {count_name(a, 'chat.assistant')})")
        check(count_name(a, "chat.system") >= 1, f"A system>=1 (got {count_name(a, 'chat.system')})")

        # C genealogy export
        exp_c = export_ancestors(c)
        OUT.joinpath("codex_20turn_C_ancestors_export.json").write_text(
            json.dumps(exp_c, indent=2, ensure_ascii=False) + "\n"
        )
        ev_c = exp_c.get("events") or []
        ids_c = [e.get("message_id") for e in ev_c if e.get("message_id")]
        check(len(ids_c) == len(set(ids_c)), "C ancestors export: no duplicate message_ids")
        check(all(e.get("name") != "chat.fork" for e in ev_c), "C ancestors export: no chat.fork")
        blob_c = " ".join(str(e.get("input") or e.get("output") or "") for e in ev_c)
        check(f"{marker}-A1" in blob_c or f"{marker}-A5" in blob_c, "C export includes A markers")
        check(f"{marker}-B" in blob_c, "C export includes B markers")
        check(f"{marker}-C" in blob_c, "C export includes C markers")
        # D should NOT appear in C ancestors (sibling, not ancestor)
        check(f"{marker}-D" not in blob_c, "C export does not include sibling D markers")
        write_transcript(
            OUT / "codex_20turn_C_ancestors_transcript.md",
            f"20-turn C genealogy ({marker})",
            {"A": a, "B": b, "C": c},
            ev_c,
        )

        # D genealogy should include A but not B/C
        exp_d = export_ancestors(d)
        OUT.joinpath("codex_20turn_D_ancestors_export.json").write_text(
            json.dumps(exp_d, indent=2, ensure_ascii=False) + "\n"
        )
        blob_d = " ".join(str(e.get("input") or e.get("output") or "") for e in (exp_d.get("events") or []))
        check(f"{marker}-A" in blob_d, "D export includes A markers")
        check(f"{marker}-B" not in blob_d, "D export excludes B (sibling branch)")
        check(f"{marker}-C" not in blob_d, "D export excludes C")
        check(f"{marker}-D" in blob_d, "D export includes D markers")
        write_transcript(
            OUT / "codex_20turn_D_ancestors_transcript.md",
            f"20-turn D genealogy ({marker})",
            {"A": a, "D": d},
            exp_d.get("events") or [],
        )

        turn_ops = [x for x in ops if x.startswith("turn:")]
        check(len(turn_ops) >= 20, f"at least 20 turns executed (got {len(turn_ops)})")

        report = {
            "marker": marker,
            "sessions": sessions,
            "ops": ops,
            "turn_count": len(turn_ops),
            "counts": {
                name: {
                    n: count_name(sid, n)
                    for n in (
                        "chat.system",
                        "chat.user",
                        "chat.assistant",
                        "chat.fork",
                        "chat.compact",
                    )
                }
                for name, sid in sessions.items()
            },
            "export_C_events": len(ev_c),
            "export_D_events": len(exp_d.get("events") or []),
            "findings": findings,
            "verdict": "PASS" if ok else "FAIL",
        }
        OUT.joinpath("codex_20turn_report.json").write_text(json.dumps(report, indent=2) + "\n")
        print("\n--- findings ---", flush=True)
        for f in findings:
            print(f, flush=True)
        print(json.dumps(report["counts"], indent=2), flush=True)
        print(f"\nVERDICT: {report['verdict']}", flush=True)
        print(f"report: {OUT/'codex_20turn_report.json'}", flush=True)
        print(f"C transcript: {OUT/'codex_20turn_C_ancestors_transcript.md'}", flush=True)
        print(f"D transcript: {OUT/'codex_20turn_D_ancestors_transcript.md'}", flush=True)
        return 0 if ok else 2
    finally:
        server.close()


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        Path("/tmp/codex_20turn_fatal.txt").write_text(repr(exc))
        print(f"FATAL: {exc}", file=sys.stderr)
        raise SystemExit(1)
