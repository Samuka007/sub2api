#!/usr/bin/env python3
"""Build a deterministic session graph from hot-store Langfuse trace JSONL.

The processor is deliberately storage-agnostic: a hot-store reader streams one
JSON object per trace, this command emits trace-to-session assignments and a
session graph manifest, and a later archive writer stores each trace payload
once. Message bodies never appear in the graph; histories are represented by
stable SHA-256 fingerprints.

Example:
  python3 scripts/langfuse_session_graph.py \
    --input /tmp/traces.jsonl \
    --graph-output /tmp/session-graph.json \
    --assignments-output /tmp/session-assignments.jsonl \
    --objects-output /tmp/session-message-objects.jsonl
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
from collections import Counter, defaultdict, deque
from dataclasses import asdict, dataclass, field, replace
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, Iterable, Iterator, List, Optional, Sequence, Set, Tuple


CLAUDE_LEGACY_SESSION = re.compile(
    r"^user_[a-fA-F0-9]{64}_account_[a-fA-F0-9-]*_session_([a-fA-F0-9-]{36})$"
)
HISTORY_KEYS = {
    "messages",
    "input",
    "contents",
    "history",
    "conversation_history",
    "chat_history",
    "message_history",
}
GENERIC_HISTORY_KEYS = HISTORY_KEYS - {"messages", "input", "contents"}
HISTORY_SEARCH_SKIP_KEYS = {
    "content",
    "prompt",
    "tools",
    "functions",
    "parameters",
    "properties",
    "schema",
    "input_schema",
    "tool_choice",
}
EXPLICIT_ID_SKIP_KEYS = HISTORY_KEYS | HISTORY_SEARCH_SKIP_KEYS
JSON_ENVELOPE_KEYS = {
    "body",
    "context",
    "data",
    "envelope",
    "metadata",
    "client_metadata",
    "payload",
    "request",
}
VOLATILE_CONTENT_KEYS = {
    "cache_control",
    "created_at",
    "finish_reason",
    "index",
    "status",
}
ASSISTANT_ITEM_TYPES = {
    "function_call",
    "reasoning",
    "custom_tool_call",
    "web_search_call",
}
TOOL_ITEM_TYPES = {"function_call_output", "tool_result"}
MAX_SEARCH_DEPTH = 32

Scope = Tuple[str, str, str]
History = Tuple[str, ...]


@dataclass(frozen=True)
class TraceFact:
    trace_id: str
    timestamp: str
    scope: Scope
    existing_session_id: str
    explicit_session_id: str
    explicit_session_source: str
    thread_id: str
    thread_source: str
    parent_thread_id: str
    previous_response_id: str
    response_id: str
    history: History
    history_objects: Tuple[Tuple[str, Any], ...]
    has_response: bool
    history_turns: int
    input_kind: str
    model: str


@dataclass(frozen=True)
class TraceAssignment:
    trace_id: str
    session_id: str
    source: str
    confidence: str


@dataclass(frozen=True)
class SessionNode:
    session_id: str
    kind: str
    source: str
    confidence: str
    trace_ids: Tuple[str, ...]
    thread_ids: Tuple[str, ...]
    representative_trace_id: str
    history_turns: int


@dataclass(frozen=True)
class SessionEdge:
    parent_session_id: str
    child_session_id: str
    kind: str
    fork_history_length: int = 0


@dataclass(frozen=True)
class TraceDisposition:
    trace_id: str
    status: str
    reason: str


@dataclass(frozen=True)
class TraceHistory:
    trace_id: str
    parent_trace_id: str
    prefix_length: int
    append_message_ids: History


@dataclass(frozen=True)
class MessageObject:
    message_id: str
    value: Any


@dataclass(frozen=True)
class GraphResult:
    nodes: Tuple[SessionNode, ...]
    edges: Tuple[SessionEdge, ...]
    assignments: Tuple[TraceAssignment, ...]
    dispositions: Tuple[TraceDisposition, ...]
    histories: Tuple[TraceHistory, ...]
    message_objects: Tuple[MessageObject, ...]
    report: Dict[str, Any]


@dataclass
class _NodeBuilder:
    session_id: str
    kind: str
    source: str
    confidence: str
    trace_ids: Set[str] = field(default_factory=set)
    thread_ids: Set[str] = field(default_factory=set)
    representative_trace_id: str = ""
    representative_key: Tuple[int, int, str, str] = (0, 0, "", "")
    history_turns: int = 0


@dataclass
class _TrieNode:
    parent: Optional["_TrieNode"] = None
    token: str = ""
    sequence: History = ()
    children: Dict[str, "_TrieNode"] = field(default_factory=dict)
    facts: List[TraceFact] = field(default_factory=list)
    descendant_leaves: List["_TrieNode"] = field(default_factory=list)
    resolved_session_id: str = ""
    conflict: bool = False


def _as_json(value: Any) -> Any:
    if not isinstance(value, str):
        return value
    raw = value.strip()
    if not raw:
        return value
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return value


def _metadata_dict(value: Any) -> Dict[str, Any]:
    parsed = _as_json(value)
    return parsed if isinstance(parsed, dict) else {}


def _string_id(value: Any) -> str:
    if isinstance(value, str):
        return value.strip()
    if isinstance(value, int) and not isinstance(value, bool):
        return str(value)
    return ""


def _normalized_key(key: str) -> str:
    return re.sub(r"[_\-.]", "", key.strip().lower())


def _at_path(document: Any, *path: str) -> Any:
    current = document
    for part in path:
        if not isinstance(current, dict):
            return None
        current = current.get(part)
    return current


def _first_path(document: Any, paths: Sequence[Tuple[str, ...]]) -> Tuple[str, str]:
    for path in paths:
        value = _string_id(_at_path(document, *path))
        if value:
            return value, "body." + ".".join(path)
    return "", ""


def _claude_session(document: Any) -> Tuple[str, str]:
    raw = _at_path(document, "metadata", "user_id")
    parsed = _as_json(raw)
    if isinstance(parsed, dict):
        device_id = _string_id(parsed.get("device_id"))
        session_id = _string_id(parsed.get("session_id"))
        if device_id and session_id:
            return session_id, "body.metadata.user_id"
        return "", ""
    if isinstance(raw, str):
        match = CLAUDE_LEGACY_SESSION.fullmatch(raw.strip())
        if match:
            return match.group(1), "body.metadata.user_id.legacy"
    return "", ""


def _extract_session(document: Any) -> Tuple[str, str]:
    if not isinstance(document, dict):
        return "", ""
    value, source = _first_path(
        document,
        (
            ("session_id",),
            ("conversation_id",),
            ("metadata", "session_id"),
            ("metadata", "conversation_id"),
            ("metadata", "user_id", "session_id"),
            ("client_metadata", "session_id"),
            ("client_metadata", "conversation_id"),
        ),
    )
    if value:
        return value, source
    claude_session = _claude_session(document)
    if claude_session[0]:
        return claude_session

    queue: deque[Tuple[Any, int]] = deque([(document, 0)])
    valid_keys = {
        "session",
        "sessionid",
        "sessionuuid",
        "conversation",
        "conversationid",
        "conversationuuid",
    }
    while queue:
        node, depth = queue.popleft()
        if depth >= MAX_SEARCH_DEPTH:
            continue
        if isinstance(node, list):
            queue.extend(
                (item, depth + 1) for item in node if isinstance(item, (dict, list))
            )
            continue
        if not isinstance(node, dict):
            continue
        for key in sorted(node):
            if _normalized_key(key) in valid_keys:
                value = _string_id(node[key])
                if value:
                    return value, "body.recursive." + key
        for key in sorted(node):
            normalized = key.strip().lower()
            if normalized in EXPLICIT_ID_SKIP_KEYS:
                continue
            value = node[key]
            if isinstance(value, (dict, list)):
                queue.append((value, depth + 1))
            elif normalized in JSON_ENVELOPE_KEYS:
                parsed = _as_json(value)
                if isinstance(parsed, (dict, list)):
                    queue.append((parsed, depth + 1))
    return "", ""


def _compact_document(trace: Dict[str, Any]) -> Dict[str, Any]:
    document: Dict[str, Any] = {}
    for key in ("session_id", "conversation_id", "previous_response_id"):
        value = _as_json(trace.get("input_" + key))
        if value not in (None, ""):
            document[key] = value
    for key in JSON_ENVELOPE_KEYS:
        value = _as_json(trace.get("input_" + key))
        if isinstance(value, (dict, list)):
            document[key] = value
    return document


def _header_value(trace: Dict[str, Any], *names: str) -> str:
    headers = _metadata_dict(trace.get("headers"))
    lowered = {str(key).lower(): value for key, value in headers.items()}
    for name in names:
        value = _string_id(lowered.get(name.lower()))
        if value:
            return value
    return ""


def _extract_thread(
    trace: Dict[str, Any], document: Any, metadata: Dict[str, Any]
) -> Tuple[str, str]:
    if isinstance(document, dict):
        value, source = _first_path(
            document,
            (
                ("client_metadata", "thread_id"),
                ("thread_id",),
                ("metadata", "thread_id"),
            ),
        )
        if value:
            return value, source
    value = _string_id(metadata.get("thread_id") or metadata.get("threadId"))
    if value:
        return value, "trace.metadata.thread_id"
    value = _header_value(trace, "thread-id", "thread_id")
    return (value, "header.thread_id") if value else ("", "")


def _turn_metadata_parent(raw: Any) -> str:
    parsed = _as_json(raw)
    if not isinstance(parsed, dict):
        return ""
    for key in (
        "fork_from_session_id",
        "forked_from_thread_id",
        "forkedFromId",
        "parent_thread_id",
        "parentThreadId",
    ):
        value = _string_id(parsed.get(key))
        if value:
            return value
    return ""


def _extract_parent_thread(
    trace: Dict[str, Any], document: Any, metadata: Dict[str, Any]
) -> str:
    keys = (
        "forked_from_thread_id",
        "forkedFromId",
        "parent_thread_id",
        "parentThreadId",
        "fork_from_session_id",
    )
    for container in (
        document if isinstance(document, dict) else {},
        _at_path(document, "metadata") if isinstance(document, dict) else {},
        _at_path(document, "client_metadata") if isinstance(document, dict) else {},
        metadata,
    ):
        if not isinstance(container, dict):
            continue
        for key in keys:
            value = _string_id(container.get(key))
            if value:
                return value
        for key in ("x-codex-turn-metadata", "x_codex_turn_metadata"):
            value = _turn_metadata_parent(container.get(key))
            if value:
                return value
    return _turn_metadata_parent(_header_value(trace, "x-codex-turn-metadata"))


def _canonical_json(value: Any) -> Any:
    if isinstance(value, dict):
        return {key: _canonical_json(item) for key, item in sorted(value.items())}
    if isinstance(value, list):
        return [_canonical_json(item) for item in value]
    return value


def _stable_content(value: Any) -> Any:
    value = _as_json(value)
    if isinstance(value, dict):
        return {
            key: _canonical_json(item)
            for key, item in sorted(value.items())
            if key not in VOLATILE_CONTENT_KEYS
        }
    if isinstance(value, list):
        return [
            _stable_content(item) if isinstance(item, dict) else _canonical_json(item)
            for item in value
        ]
    return value


def _fingerprint(value: Any) -> str:
    raw = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(raw.encode("utf-8")).hexdigest()


def _message_signature(item: Any) -> Optional[Dict[str, Any]]:
    if not isinstance(item, dict):
        return None
    role = str(item.get("role") or "").strip().lower()
    item_type = str(item.get("type") or "").strip().lower()
    if role == "model":
        role = "assistant"
    if not role and item_type in ASSISTANT_ITEM_TYPES:
        role = "assistant"
    if not role and item_type in TOOL_ITEM_TYPES:
        role = "tool"
    if not role or role in {"system", "developer"}:
        return None
    signature: Dict[str, Any] = {"role": role}
    if item_type:
        signature["type"] = item_type
    if "content" in item:
        content = _stable_content(item.get("content"))
        include_attributes = True
    elif "parts" in item:
        content = _stable_content(item.get("parts"))
        include_attributes = True
    else:
        content = _stable_content(
            {
                key: value
                for key, value in item.items()
                if key not in {"role", "type"} | VOLATILE_CONTENT_KEYS
            }
        )
        include_attributes = False
    signature["content"] = content
    attributes = (
        {
            key: _stable_content(value)
            for key, value in sorted(item.items())
            if key
            not in {"role", "type", "content", "parts"} | VOLATILE_CONTENT_KEYS
        }
        if include_attributes
        else {}
    )
    if attributes:
        signature["attributes"] = attributes
    return signature


def _walk_history_lists(node: Any, depth: int = 0) -> Iterator[Tuple[str, List[Any]]]:
    if depth >= MAX_SEARCH_DEPTH:
        return
    if isinstance(node, list):
        for item in node:
            if isinstance(item, (dict, list)):
                yield from _walk_history_lists(item, depth + 1)
        return
    if not isinstance(node, dict):
        return
    for key, value in node.items():
        normalized = key.strip().lower()
        if normalized in HISTORY_KEYS and isinstance(value, list):
            yield normalized, value
        if normalized in HISTORY_SEARCH_SKIP_KEYS:
            continue
        if isinstance(value, (dict, list)):
            yield from _walk_history_lists(value, depth + 1)


def _conversation_history_details(
    document: Any,
) -> Tuple[History, bool, int, Tuple[Tuple[str, Any], ...]]:
    best: History = ()
    best_has_response = False
    best_turns = 0
    best_objects: Tuple[Tuple[str, Any], ...] = ()
    best_priority = len(HISTORY_KEYS) + 1
    priority = {
        "chat_history": 1,
        "contents": 2,
        "conversation_history": 3,
        "history": 4,
        "input": 5,
        "message_history": 6,
        "messages": 7,
    }
    for key, items in _walk_history_lists(document):
        signatures = [signature for item in items if (signature := _message_signature(item))]
        if signatures:
            candidate = tuple(sys.intern(_fingerprint(signature)) for signature in signatures)
            candidate_objects = tuple(zip(candidate, signatures))
            turns = sum(signature["role"] == "user" for signature in signatures)
            has_response = any(
                signature["role"] in {"assistant", "tool"}
                for signature in signatures
            )
        elif key in GENERIC_HISTORY_KEYS:
            values = tuple(_stable_content(item) for item in items)
            candidate = tuple(sys.intern(_fingerprint(item)) for item in values)
            candidate_objects = tuple(zip(candidate, values))
            turns = (len(candidate) + 1) // 2
            has_response = len(candidate) >= 2
        else:
            candidate = ()
            candidate_objects = ()
            turns = 0
            has_response = False
        candidate_priority = priority.get(key, len(priority) + 1)
        if (len(candidate), -candidate_priority) > (len(best), -best_priority):
            best = candidate
            best_has_response = has_response
            best_turns = turns
            best_objects = candidate_objects
            best_priority = candidate_priority
    return best, best_has_response, best_turns, best_objects


def conversation_history(document: Any) -> Tuple[History, bool, int]:
    """Return the longest stable message history without exposing message bodies."""
    history, has_response, turns, _objects = _conversation_history_details(document)
    return history, has_response, turns


def _input_kind(
    raw_input: Any,
    document: Any,
    history: History,
    has_response: bool,
    turns: int,
) -> str:
    if history:
        if has_response:
            return "history"
        if len(history) == 1 and turns == 1:
            return "single_user"
        return "user_only_history"
    if raw_input is None or (isinstance(raw_input, str) and not raw_input.strip()):
        return "empty_input"
    if isinstance(document, str) and document.strip():
        return "single_user"
    if isinstance(document, dict):
        scalar = document.get("input")
        if isinstance(scalar, str) and scalar.strip():
            return "single_user"
    return "no_recoverable_history"


def parse_trace(trace: Dict[str, Any]) -> TraceFact:
    metadata = _metadata_dict(trace.get("metadata"))
    raw_input = trace.get("input")
    compact = bool(trace.get("_compact"))
    document = _compact_document(trace) if compact else _as_json(raw_input)
    explicit_session_id, explicit_session_source = _extract_session(document)
    thread_id, thread_source = _extract_thread(trace, document, metadata)
    parent_thread_id = _extract_parent_thread(trace, document, metadata)
    if compact:
        history = tuple(
            sys.intern(_string_id(item))
            for item in (trace.get("history_sequence") or [])
            if _string_id(item)
        )
        has_response = bool(trace.get("history_has_response"))
        try:
            turns = max(0, int(trace.get("history_turns") or 0))
        except (TypeError, ValueError):
            turns = 0
        history_objects: Tuple[Tuple[str, Any], ...] = ()
    else:
        history, has_response, turns, history_objects = _conversation_history_details(document)
    previous_response_id = ""
    if isinstance(document, dict):
        previous_response_id = _string_id(document.get("previous_response_id"))
    output = _as_json(trace.get("output"))
    response_id = ""
    protocol = _string_id(metadata.get("entry_protocol"))
    if protocol == "openai.responses" and isinstance(output, dict):
        response_id = _string_id(output.get("id"))
    if not response_id:
        response_id = _string_id(trace.get("output_response_id") or metadata.get("response_id"))
    input_kind = _input_kind(raw_input, document, history, has_response, turns)
    compact_input_kind = _string_id(trace.get("compact_input_kind"))
    if compact and not history and compact_input_kind in {
        "empty_input",
        "single_user",
        "no_recoverable_history",
    }:
        input_kind = compact_input_kind
    return TraceFact(
        trace_id=_string_id(trace.get("id")),
        timestamp=_string_id(trace.get("timestamp") or trace.get("startTime")),
        scope=(
            _string_id(trace.get("project_id") or trace.get("projectId")),
            _string_id(trace.get("user_id") or trace.get("userId")),
            protocol,
        ),
        existing_session_id=_string_id(trace.get("session_id") or trace.get("sessionId")),
        explicit_session_id=explicit_session_id,
        explicit_session_source=explicit_session_source,
        thread_id=thread_id,
        thread_source=thread_source,
        parent_thread_id=parent_thread_id,
        previous_response_id=previous_response_id,
        response_id=response_id,
        history=history,
        history_objects=history_objects,
        has_response=has_response,
        history_turns=turns,
        input_kind=input_kind,
        model=_string_id(metadata.get("client_model") or metadata.get("model")),
    )


def _timestamp_key(value: Any) -> Tuple[int, float, str]:
    raw = _string_id(value)
    if not raw:
        return 0, 0.0, ""
    try:
        parsed = datetime.fromisoformat(raw.replace("Z", "+00:00"))
        if parsed.tzinfo is None:
            parsed = parsed.replace(tzinfo=timezone.utc)
        return 1, parsed.timestamp(), raw
    except ValueError:
        return 0, 0.0, raw


def dedupe_traces(traces: Iterable[Dict[str, Any]]) -> Tuple[List[Dict[str, Any]], int, int]:
    """Keep the latest deterministic record for every non-empty trace ID."""
    latest: Dict[str, Tuple[Tuple[int, float, str], Dict[str, Any]]] = {}
    valid_rows = 0
    invalid_rows = 0
    for trace in traces:
        trace_id = _string_id(trace.get("id"))
        if not trace_id:
            invalid_rows += 1
            continue
        valid_rows += 1
        timestamp_key = _timestamp_key(trace.get("timestamp") or trace.get("startTime"))
        previous = latest.get(trace_id)
        if previous is None or timestamp_key > previous[0]:
            latest[trace_id] = (timestamp_key, trace)
        elif timestamp_key == previous[0] and _fingerprint(trace) > _fingerprint(previous[1]):
            latest[trace_id] = (timestamp_key, trace)
    unique = [
        item[1]
        for trace_id, item in sorted(
            latest.items(), key=lambda pair: (pair[1][0], pair[0])
        )
    ]
    return unique, valid_rows - len(unique), invalid_rows


class _UnionFind:
    def __init__(self, values: Iterable[str]) -> None:
        self.parent = {value: value for value in values}

    def find(self, value: str) -> str:
        root = value
        while self.parent[root] != root:
            root = self.parent[root]
        while self.parent[value] != value:
            parent = self.parent[value]
            self.parent[value] = root
            value = parent
        return root

    def union(self, left: str, right: str) -> None:
        left_root = self.find(left)
        right_root = self.find(right)
        if left_root == right_root:
            return
        if left_root < right_root:
            self.parent[right_root] = left_root
        else:
            self.parent[left_root] = right_root


def _generated_session(kind: str, scope: Scope, anchor: Any) -> str:
    return kind + ":" + _fingerprint({"scope": scope, "anchor": anchor})


def _build_scope_trie(
    facts: Sequence[TraceFact],
) -> Tuple[_TrieNode, Dict[History, _TrieNode], List[_TrieNode]]:
    root = _TrieNode()
    lookup: Dict[History, _TrieNode] = {(): root}
    for fact in facts:
        if not fact.history:
            continue
        node = root
        for token in fact.history:
            child = node.children.get(token)
            if child is None:
                child = _TrieNode(parent=node, token=token)
                node.children[token] = child
            node = child
        node.sequence = fact.history
        lookup[fact.history] = node
        node.facts.append(fact)

    leaves: List[_TrieNode] = []
    stack: List[Tuple[_TrieNode, bool]] = [(root, False)]
    while stack:
        node, visited = stack.pop()
        if not visited:
            stack.append((node, True))
            stack.extend((child, False) for child in node.children.values())
            continue
        descendants: List[_TrieNode] = []
        for child in node.children.values():
            descendants.extend(child.descendant_leaves)
        if not node.children and node.facts and any(fact.has_response for fact in node.facts):
            descendants = [node]
            leaves.append(node)
        node.descendant_leaves = descendants
    return root, lookup, leaves


def _history_anchor(leaf: _TrieNode) -> str:
    candidates: List[TraceFact] = []
    node: Optional[_TrieNode] = leaf
    while node is not None:
        candidates.extend(node.facts)
        node = node.parent
    if not candidates:
        return _fingerprint(leaf.sequence)
    return min(
        candidates,
        key=lambda fact: (_timestamp_key(fact.timestamp), fact.trace_id),
    ).trace_id


def _ambiguous_chains(
    items: Sequence[Tuple[TraceFact, Set[str]]]
) -> List[List[Tuple[TraceFact, Set[str]]]]:
    groups: List[List[Tuple[TraceFact, Set[str]]]] = []
    for item in sorted(
        items,
        key=lambda pair: (
            len(pair[0].history),
            pair[0].timestamp,
            pair[0].trace_id,
        ),
    ):
        fact = item[0]
        compatible = [
            group
            for group in groups
            if fact.history[: len(group[-1][0].history)] == group[-1][0].history
        ]
        if compatible:
            group = max(compatible, key=lambda value: len(value[-1][0].history))
            group.append(item)
        else:
            groups.append([item])
    return groups


def build_session_graph(traces: Iterable[Dict[str, Any]]) -> GraphResult:
    unique_traces, duplicate_count, invalid_count = dedupe_traces(traces)
    message_value_pool: Dict[str, Any] = {}
    facts: List[TraceFact] = []
    for trace in unique_traces:
        fact = parse_trace(trace)
        for message_id, value in fact.history_objects:
            message_value_pool.setdefault(message_id, value)
        facts.append(replace(fact, history_objects=()))
    unique_count = len(unique_traces)
    del unique_traces
    facts = sorted(
        (fact for fact in facts if fact.trace_id),
        key=lambda fact: (_timestamp_key(fact.timestamp), fact.trace_id),
    )
    project_ids = {fact.scope[0] for fact in facts}
    if len(project_ids) > 1:
        raise ValueError("one processing window must contain exactly one project_id")
    project_id = next(iter(project_ids), "")
    fact_by_id = {fact.trace_id: fact for fact in facts}
    assignments: Dict[str, TraceAssignment] = {}
    nodes: Dict[str, _NodeBuilder] = {}
    edges: Set[SessionEdge] = set()
    blocked: Dict[str, str] = {}
    shared_prefix_traces = 0

    def ensure_node(session_id: str, kind: str, source: str, confidence: str) -> _NodeBuilder:
        node = nodes.get(session_id)
        if node is None:
            node = _NodeBuilder(session_id, kind, source, confidence)
            nodes[session_id] = node
        return node

    def assign(fact: TraceFact, session_id: str, source: str, confidence: str, kind: str) -> None:
        if fact.trace_id in assignments:
            return
        assignments[fact.trace_id] = TraceAssignment(fact.trace_id, session_id, source, confidence)
        node = ensure_node(session_id, kind, source, confidence)
        node.trace_ids.add(fact.trace_id)
        if fact.thread_id:
            node.thread_ids.add(fact.thread_id)
        representative_key = (fact.history_turns, len(fact.history), fact.timestamp, fact.trace_id)
        if representative_key > node.representative_key:
            node.representative_key = representative_key
            node.representative_trace_id = fact.trace_id
            node.history_turns = fact.history_turns

    # Explicit root-session signals always win; thread identifiers remain separate.
    for fact in facts:
        if fact.existing_session_id:
            assign(fact, fact.existing_session_id, "existing_session", "high", "explicit")
        elif fact.explicit_session_id:
            assign(
                fact,
                fact.explicit_session_id,
                fact.explicit_session_source or "explicit_session",
                "high",
                "explicit",
            )

    by_thread: Dict[Tuple[Scope, str], List[TraceFact]] = defaultdict(list)
    for fact in facts:
        if fact.thread_id:
            by_thread[(fact.scope, fact.thread_id)].append(fact)

    for key, thread_facts in sorted(by_thread.items()):
        anchors = {
            assignments[fact.trace_id].session_id
            for fact in thread_facts
            if fact.trace_id in assignments
        }
        if len(anchors) == 1:
            session_id = next(iter(anchors))
            for fact in thread_facts:
                assign(fact, session_id, "thread.session_anchor", "high", "explicit")
        elif len(anchors) > 1:
            for fact in thread_facts:
                if fact.trace_id not in assignments:
                    blocked[fact.trace_id] = "thread_session_conflict"
        elif len(thread_facts) > 1:
            scope, thread_id = key
            session_id = _generated_session("thread", scope, thread_id)
            for fact in thread_facts:
                assign(fact, session_id, "thread.explicit", "medium", "thread_provisional")

    # A Responses previous_response_id chain is stronger than scalar input shape.
    union = _UnionFind(fact.trace_id for fact in facts)
    response_owners: Dict[Tuple[Scope, str], List[TraceFact]] = defaultdict(list)
    for fact in facts:
        if fact.response_id:
            response_owners[(fact.scope, fact.response_id)].append(fact)
    for fact in facts:
        if not fact.previous_response_id:
            continue
        owners = response_owners.get((fact.scope, fact.previous_response_id), [])
        if len(owners) == 1:
            union.union(owners[0].trace_id, fact.trace_id)
        elif len(owners) > 1 and fact.trace_id not in assignments:
            blocked[fact.trace_id] = "duplicate_response_id"
    response_components: Dict[str, List[TraceFact]] = defaultdict(list)
    for fact in facts:
        response_components[union.find(fact.trace_id)].append(fact)
    for component in sorted(response_components.values(), key=lambda values: values[0].trace_id):
        if len(component) < 2:
            continue
        anchored = {
            assignments[fact.trace_id].session_id
            for fact in component
            if fact.trace_id in assignments
        }
        if len(anchored) > 1:
            for fact in component:
                if fact.trace_id not in assignments:
                    blocked[fact.trace_id] = "response_chain_conflict"
            continue
        component_ids = {fact.trace_id for fact in component}
        roots = [
            fact
            for fact in component
            if not fact.previous_response_id
            or not any(
                owner.trace_id in component_ids
                for owner in response_owners.get(
                    (fact.scope, fact.previous_response_id), []
                )
            )
        ]
        root = min(
            roots or component,
            key=lambda fact: (_timestamp_key(fact.timestamp), fact.trace_id),
        )
        session_id = (
            next(iter(anchored))
            if anchored
            else _generated_session("response", root.scope, root.trace_id)
        )
        kind = nodes[session_id].kind if session_id in nodes else "response_chain"
        confidence = "high" if anchored else "medium"
        for fact in component:
            assign(fact, session_id, "response_chain", confidence, kind)

    # Exact message-prefix tries recover linear histories and explicit shared trunks.
    by_scope: Dict[Scope, List[TraceFact]] = defaultdict(list)
    for fact in facts:
        if fact.history:
            by_scope[fact.scope].append(fact)
    for scope, scope_facts in sorted(by_scope.items()):
        _root, lookup, leaves = _build_scope_trie(scope_facts)
        leaf_sessions: Dict[int, str] = {}
        for leaf in leaves:
            path_facts: List[TraceFact] = []
            node: Optional[_TrieNode] = leaf
            while node is not None:
                path_facts.extend(node.facts)
                node = node.parent
            anchored = {
                assignments[fact.trace_id].session_id
                for fact in path_facts
                if fact.trace_id in assignments
            }
            if len(anchored) > 1:
                leaf.conflict = True
                continue
            session_id = (
                next(iter(anchored))
                if anchored
                else _generated_session("history", scope, _history_anchor(leaf))
            )
            leaf.resolved_session_id = session_id
            leaf_sessions[id(leaf)] = session_id
            if session_id not in nodes:
                ensure_node(session_id, "history_leaf", "history.longest", "medium")

        ambiguous: List[Tuple[TraceFact, Set[str]]] = []
        for fact in scope_facts:
            if fact.trace_id in assignments or fact.trace_id in blocked:
                continue
            node = lookup.get(fact.history)
            valid_leaves = [
                leaf
                for leaf in (node.descendant_leaves if node else [])
                if not leaf.conflict
            ]
            candidate_sessions = {
                leaf_sessions[id(leaf)]
                for leaf in valid_leaves
                if id(leaf) in leaf_sessions
            }
            if len(candidate_sessions) == 1:
                session_id = next(iter(candidate_sessions))
                kind = nodes[session_id].kind
                confidence = "high" if kind == "explicit" else "medium"
                assign(fact, session_id, "history.prefix", confidence, kind)
            elif len(candidate_sessions) > 1:
                ambiguous.append((fact, candidate_sessions))

        for group in _ambiguous_chains(ambiguous):
            first = group[0][0]
            session_id = _generated_session(
                "shared", scope, {"trace_id": first.trace_id, "history": first.history}
            )
            for fact, _candidate_sessions in group:
                assign(fact, session_id, "history.shared_prefix", "medium", "shared_prefix")
            shared_prefix_traces += len(group)
            fork_length = max(len(fact.history) for fact, _ in group)
            candidate_sessions = set().union(*(candidates for _fact, candidates in group))
            for child_session_id in candidate_sessions:
                if child_session_id != session_id:
                    edges.add(
                        SessionEdge(
                            session_id,
                            child_session_id,
                            "history_fork",
                            fork_length,
                        )
                    )

    # Explicit thread ancestry is emitted after both sides have a resolved node.
    for (scope, thread_id), thread_facts in sorted(by_thread.items()):
        child_sessions = {
            assignments[fact.trace_id].session_id
            for fact in thread_facts
            if fact.trace_id in assignments
        }
        parent_threads = {fact.parent_thread_id for fact in thread_facts if fact.parent_thread_id}
        if len(child_sessions) != 1:
            continue
        child_session = next(iter(child_sessions))
        for parent_thread in parent_threads:
            parent_facts = by_thread.get((scope, parent_thread), [])
            parent_sessions = {
                assignments[fact.trace_id].session_id
                for fact in parent_facts
                if fact.trace_id in assignments
            }
            parent_session = next(iter(parent_sessions)) if len(parent_sessions) == 1 else ""
            if parent_session and parent_session != child_session:
                edges.add(SessionEdge(parent_session, child_session, "thread_fork"))

    dispositions: List[TraceDisposition] = []
    for fact in facts:
        if fact.trace_id in assignments:
            continue
        if fact.trace_id in blocked:
            reason = blocked[fact.trace_id]
            status = "unresolved"
        elif fact.input_kind == "empty_input":
            reason = "empty_input"
            status = "excluded"
        elif fact.input_kind == "single_user":
            reason = "single_user"
            status = "excluded"
        elif fact.input_kind == "user_only_history":
            reason = "user_only_history"
            status = "unresolved"
        elif fact.input_kind == "history":
            reason = "no_unique_history_session"
            status = "unresolved"
        else:
            reason = "no_recoverable_history"
            status = "excluded"
        dispositions.append(TraceDisposition(fact.trace_id, status, reason))

    trace_order = {fact.trace_id: index for index, fact in enumerate(facts)}
    built_nodes = tuple(
        SessionNode(
            session_id=node.session_id,
            kind=node.kind,
            source=node.source,
            confidence=node.confidence,
            trace_ids=tuple(
                sorted(
                    node.trace_ids,
                    key=lambda trace_id: (trace_order[trace_id], trace_id),
                )
            ),
            thread_ids=tuple(sorted(node.thread_ids)),
            representative_trace_id=node.representative_trace_id,
            history_turns=node.history_turns,
        )
        for node in sorted(nodes.values(), key=lambda item: item.session_id)
        if node.trace_ids
    )
    built_edges = tuple(
        sorted(
            edges,
            key=lambda edge: (
                edge.parent_session_id,
                edge.child_session_id,
                edge.kind,
            ),
        )
    )
    built_assignments = tuple(sorted(assignments.values(), key=lambda item: item.trace_id))
    built_dispositions = tuple(sorted(dispositions, key=lambda item: item.trace_id))

    history_rows: List[TraceHistory] = []
    history_references = 0
    assigned_history_by_session: Dict[str, List[TraceFact]] = defaultdict(list)
    for fact in facts:
        assignment = assignments.get(fact.trace_id)
        if assignment is None or not fact.history:
            continue
        assigned_history_by_session[assignment.session_id].append(fact)
    for session_id in sorted(assigned_history_by_session):
        prior: List[TraceFact] = []
        session_facts = sorted(
            assigned_history_by_session[session_id],
            key=lambda fact: (trace_order[fact.trace_id], fact.trace_id),
        )
        for fact in session_facts:
            candidates = [
                candidate
                for candidate in prior
                if len(candidate.history) <= len(fact.history)
                and fact.history[: len(candidate.history)] == candidate.history
            ]
            parent = max(
                candidates,
                key=lambda candidate: (
                    len(candidate.history),
                    trace_order[candidate.trace_id],
                    candidate.trace_id,
                ),
                default=None,
            )
            prefix_length = len(parent.history) if parent is not None else 0
            append_ids = fact.history[prefix_length:]
            history_references += len(fact.history)
            history_rows.append(
                TraceHistory(
                    trace_id=fact.trace_id,
                    parent_trace_id=parent.trace_id if parent is not None else "",
                    prefix_length=prefix_length,
                    append_message_ids=append_ids,
                )
            )
            prior.append(fact)
    built_histories = tuple(sorted(history_rows, key=lambda item: item.trace_id))
    built_message_objects = tuple(
        MessageObject(message_id, message_value_pool[message_id])
        for message_id in sorted(
            {
                message_id
                for facts_in_session in assigned_history_by_session.values()
                for fact in facts_in_session
                for message_id in fact.history
                if message_id in message_value_pool
            }
        )
    )
    unique_history_fingerprints = len(
        {
            message_id
            for facts_in_session in assigned_history_by_session.values()
            for fact in facts_in_session
            for message_id in fact.history
        }
    )
    status_counts = Counter(item.status for item in built_dispositions)
    reason_counts = Counter(item.reason for item in built_dispositions)
    assigned_count = len(built_assignments)
    unresolved_count = status_counts["unresolved"]
    eligible_count = assigned_count + unresolved_count
    report: Dict[str, Any] = {
        "project_id": project_id,
        "trace_rows_read": unique_count + duplicate_count + invalid_count,
        "duplicate_trace_rows": duplicate_count,
        "invalid_trace_rows": invalid_count,
        "traces_scanned": len(facts),
        "traces_assigned": assigned_count,
        "traces_excluded": status_counts["excluded"],
        "traces_unresolved": unresolved_count,
        "session_nodes": len(built_nodes),
        "session_edges": len(built_edges),
        "shared_prefix_traces": shared_prefix_traces,
        "history_references": history_references,
        "unique_history_fingerprints": unique_history_fingerprints,
        "deduplicated_history_references": history_references
        - unique_history_fingerprints,
        "materialized_history_objects": len(built_message_objects),
        "raw_coverage": assigned_count / len(facts) if facts else 0.0,
        "eligible_coverage": assigned_count / eligible_count if eligible_count else 1.0,
        "disposition_reasons": dict(sorted(reason_counts.items())),
    }
    return GraphResult(
        nodes=built_nodes,
        edges=built_edges,
        assignments=built_assignments,
        dispositions=built_dispositions,
        histories=built_histories,
        message_objects=built_message_objects,
        report=report,
    )


def _temporary_output(path: Path) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    return path.with_name(f"{path.name}.tmp.{os.getpid()}")


def write_graph(path: Path, result: GraphResult) -> None:
    payload = {
        "version": 1,
        "report": result.report,
        "nodes": [asdict(item) for item in result.nodes],
        "edges": [asdict(item) for item in result.edges],
        "histories": [asdict(item) for item in result.histories],
        "dispositions": [asdict(item) for item in result.dispositions],
    }
    temporary = _temporary_output(path)
    try:
        with temporary.open("w", encoding="utf-8") as handle:
            json.dump(payload, handle, ensure_ascii=False, indent=2, sort_keys=True)
            handle.write("\n")
        os.replace(temporary, path)
    except BaseException:
        temporary.unlink(missing_ok=True)
        raise


def write_assignments(path: Path, result: GraphResult) -> None:
    temporary = _temporary_output(path)
    try:
        with temporary.open("w", encoding="utf-8") as handle:
            for item in result.assignments:
                json.dump(asdict(item), handle, ensure_ascii=False, sort_keys=True)
                handle.write("\n")
        os.replace(temporary, path)
    except BaseException:
        temporary.unlink(missing_ok=True)
        raise


def write_message_objects(path: Path, result: GraphResult) -> None:
    temporary = _temporary_output(path)
    try:
        with temporary.open("w", encoding="utf-8") as handle:
            for item in result.message_objects:
                json.dump(asdict(item), handle, ensure_ascii=False, sort_keys=True)
                handle.write("\n")
        os.replace(temporary, path)
    except BaseException:
        temporary.unlink(missing_ok=True)
        raise


def _read_jsonl(path: str) -> Iterator[Dict[str, Any]]:
    handle = sys.stdin if path == "-" else open(path, encoding="utf-8")
    try:
        for line_number, line in enumerate(handle, 1):
            if not line.strip():
                continue
            try:
                item = json.loads(line)
            except json.JSONDecodeError as error:
                raise SystemExit(f"invalid JSONL at line {line_number}: {error.msg}") from error
            if not isinstance(item, dict):
                raise SystemExit(f"invalid JSONL at line {line_number}: expected object")
            yield item
    finally:
        if handle is not sys.stdin:
            handle.close()


def main(argv: Optional[Sequence[str]] = None) -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, help="trace JSONL path, or - for stdin")
    parser.add_argument("--graph-output", required=True, type=Path)
    parser.add_argument("--assignments-output", required=True, type=Path)
    parser.add_argument("--objects-output", required=True, type=Path)
    args = parser.parse_args(argv)
    output_paths = [args.graph_output, args.assignments_output, args.objects_output]
    if len({path.resolve() for path in output_paths}) != len(output_paths):
        parser.error("output paths must be distinct")
    existing_paths = [str(path) for path in output_paths if path.exists()]
    if existing_paths:
        parser.error("immutable output path already exists: " + ", ".join(existing_paths))
    try:
        result = build_session_graph(_read_jsonl(args.input))
    except ValueError as error:
        parser.error(str(error))
    write_message_objects(args.objects_output, result)
    write_assignments(args.assignments_output, result)
    # The graph is the commit marker for an immutable processing window.
    write_graph(args.graph_output, result)
    json.dump(result.report, sys.stdout, ensure_ascii=False, sort_keys=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
