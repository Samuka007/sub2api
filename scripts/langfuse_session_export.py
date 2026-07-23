#!/usr/bin/env python3
"""Export a Langfuse session transcript from chat.* observations.

Environment (do not commit values):
  LANGFUSE_HOST          e.g. http://localhost:3000
  LANGFUSE_PUBLIC_KEY
  LANGFUSE_SECRET_KEY

Usage:
  python3 scripts/langfuse_session_export.py --session-id SESSION
  python3 scripts/langfuse_session_export.py --session-id SESSION --include-ancestors
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, Dict, List, Optional, Tuple

# Matches modeltrace.WholeThreadForkMessageID: Codex thread-level fork
# has no specific parent message id.
WHOLE_THREAD_FORK_MESSAGE_ID = "thread"


def _env(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise SystemExit(f"missing required env {name}")
    return value


def _request(host: str, path: str, public_key: str, secret_key: str) -> Any:
    url = host.rstrip("/") + path
    req = urllib.request.Request(url)
    raw = f"{public_key}:{secret_key}".encode("utf-8")
    req.add_header("Authorization", "Basic " + base64.b64encode(raw).decode("ascii"))
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read().decode("utf-8"))


def _metadata_dict(metadata: Any) -> Dict[str, Any]:
    if isinstance(metadata, dict):
        return metadata
    if isinstance(metadata, str) and metadata.strip():
        try:
            parsed = json.loads(metadata)
        except json.JSONDecodeError:
            return {}
        if isinstance(parsed, dict):
            return parsed
    return {}


def _message_id(metadata: Any) -> str:
    value = _metadata_dict(metadata).get("message_id")
    return str(value).strip() if value is not None else ""


def _seq(metadata: Any) -> int:
    value = _metadata_dict(metadata).get("seq")
    try:
        return int(value)
    except (TypeError, ValueError):
        return 0


def _event_sort_key(item: Dict[str, Any]) -> Tuple[str, int, str]:
    # Same-turn chat.* spans often share start_time; seq preserves write order
    # (user before assistant). message_id alone is not chronological.
    return (
        str(item.get("start_time") or ""),
        _seq(item.get("metadata")),
        str(item.get("message_id") or ""),
    )


def _list_traces(host: str, public_key: str, secret_key: str, session_id: str) -> List[Dict[str, Any]]:
    traces: List[Dict[str, Any]] = []
    page = 1
    while True:
        qs = urllib.parse.urlencode({"sessionId": session_id, "page": page, "limit": 100})
        payload = _request(host, f"/api/public/traces?{qs}", public_key, secret_key)
        data = payload.get("data") or []
        traces.extend(data)
        meta = payload.get("meta") or {}
        total_pages = int(meta.get("totalPages") or 1)
        if page >= total_pages or not data:
            break
        page += 1
    return traces


def _list_observations(
    host: str, public_key: str, secret_key: str, trace_id: str
) -> List[Dict[str, Any]]:
    observations: List[Dict[str, Any]] = []
    page = 1
    while True:
        qs = urllib.parse.urlencode({"traceId": trace_id, "page": page, "limit": 100})
        payload = _request(host, f"/api/public/observations?{qs}", public_key, secret_key)
        data = payload.get("data") or []
        observations.extend(data)
        meta = payload.get("meta") or {}
        total_pages = int(meta.get("totalPages") or 1)
        if page >= total_pages or not data:
            break
        page += 1
    return observations


def collect_chat_events(
    host: str, public_key: str, secret_key: str, session_id: str
) -> List[Dict[str, Any]]:
    events: List[Dict[str, Any]] = []
    for trace in _list_traces(host, public_key, secret_key, session_id):
        trace_id = str(trace.get("id") or "").strip()
        if not trace_id:
            continue
        for obs in _list_observations(host, public_key, secret_key, trace_id):
            name = str(obs.get("name") or "")
            if not name.startswith("chat."):
                continue
            metadata = obs.get("metadata") or {}
            events.append(
                {
                    "name": name,
                    "message_id": _message_id(metadata),
                    "metadata": metadata,
                    "input": obs.get("input"),
                    "output": obs.get("output"),
                    "start_time": obs.get("startTime") or obs.get("start_time") or "",
                    "trace_id": trace_id,
                    "session_id": session_id,
                }
            )
    events.sort(key=_event_sort_key)
    return events


def dedupe_earliest(events: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    """Keep the earliest event per non-empty message_id; order preserved."""
    seen = set()
    out: List[Dict[str, Any]] = []
    for event in events:
        message_id = str(event.get("message_id") or "").strip()
        if not message_id:
            out.append(event)
            continue
        if message_id in seen:
            continue
        seen.add(message_id)
        out.append(event)
    return out


def fork_parent(events: List[Dict[str, Any]]) -> Optional[Tuple[str, str]]:
    for event in events:
        if event.get("name") != "chat.fork":
            continue
        metadata = event.get("metadata") or {}
        if isinstance(metadata, str):
            try:
                metadata = json.loads(metadata)
            except json.JSONDecodeError:
                metadata = {}
        if not isinstance(metadata, dict):
            continue
        session_id = str(metadata.get("fork_from_session_id") or "").strip()
        message_id = str(metadata.get("fork_from_message_id") or "").strip()
        if session_id and message_id:
            return session_id, message_id
    return None


def ancestor_prefix(ancestors: List[Dict[str, Any]], fork_message_id: str) -> List[Dict[str, Any]]:
    """Parent line up to the fork point.

    - fork_message_id == thread: whole parent transcript
    - otherwise: events through the matching message_id (inclusive)
    """
    if fork_message_id == WHOLE_THREAD_FORK_MESSAGE_ID:
        return list(ancestors)
    prefix: List[Dict[str, Any]] = []
    for event in ancestors:
        prefix.append(event)
        if str(event.get("message_id") or "") == fork_message_id:
            break
    return prefix


def join_with_ancestors(
    child_events: List[Dict[str, Any]],
    ancestor_events: List[Dict[str, Any]],
    fork_message_id: str,
) -> List[Dict[str, Any]]:
    """Build full genealogy: ancestor prefix + child (no fork marker), deduped."""
    prefix = ancestor_prefix(ancestor_events, fork_message_id)
    child = [event for event in child_events if event.get("name") != "chat.fork"]
    return dedupe_earliest(prefix + child)


def export_session(
    host: str,
    public_key: str,
    secret_key: str,
    session_id: str,
    include_ancestors: bool,
    _stack: Optional[set] = None,
) -> List[Dict[str, Any]]:
    if _stack is None:
        _stack = set()
    if session_id in _stack:
        return []
    _stack.add(session_id)
    events = dedupe_earliest(collect_chat_events(host, public_key, secret_key, session_id))
    if not include_ancestors:
        return events
    parent = fork_parent(events)
    if not parent:
        return events
    parent_session, fork_message_id = parent
    ancestors = export_session(
        host, public_key, secret_key, parent_session, True, _stack
    )
    return join_with_ancestors(events, ancestors, fork_message_id)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--session-id", required=True)
    parser.add_argument("--include-ancestors", action="store_true")
    args = parser.parse_args()
    host = _env("LANGFUSE_HOST")
    public_key = _env("LANGFUSE_PUBLIC_KEY")
    secret_key = _env("LANGFUSE_SECRET_KEY")
    try:
        transcript = export_session(
            host, public_key, secret_key, args.session_id, args.include_ancestors
        )
    except urllib.error.URLError as exc:
        raise SystemExit(f"langfuse request failed: {exc}") from exc
    json.dump({"session_id": args.session_id, "events": transcript}, sys.stdout, indent=2)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
