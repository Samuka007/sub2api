#!/usr/bin/env python3
"""Unit tests for hot-side Langfuse trace session graph recovery."""

from __future__ import annotations

import json
import contextlib
import io
import tempfile
import unittest
from pathlib import Path

from langfuse_session_graph import (
    build_session_graph,
    conversation_history,
    dedupe_traces,
    main,
    parse_trace,
    write_graph,
)


def _u(text: str) -> dict:
    return {"role": "user", "content": text}


def _a(text: str) -> dict:
    return {"role": "assistant", "content": text}


def _trace(
    trace_id: str,
    body: object,
    *,
    timestamp: str | None = None,
    session_id: str = "",
    thread_id: str = "",
    parent_thread_id: str = "",
    output: object | None = None,
    user_id: str = "user",
    protocol: str = "openai.responses",
) -> dict:
    metadata = {"entry_protocol": protocol, "client_model": "model"}
    if thread_id:
        metadata["thread_id"] = thread_id
    if parent_thread_id:
        metadata["forked_from_thread_id"] = parent_thread_id
    return {
        "id": trace_id,
        "timestamp": timestamp or f"2026-08-02T18:00:{len(trace_id):02d}Z",
        "project_id": "project",
        "user_id": user_id,
        "session_id": session_id or None,
        "metadata": metadata,
        "input": json.dumps(body) if not isinstance(body, str) else body,
        "output": json.dumps(output) if output is not None else None,
    }


def _assignments(result) -> dict[str, object]:
    return {item.trace_id: item for item in result.assignments}


class TraceParsingTest(unittest.TestCase):
    def test_session_and_thread_are_kept_distinct(self) -> None:
        fact = parse_trace(
            _trace(
                "t1",
                {
                    "client_metadata": {
                        "session_id": "root-session",
                        "thread_id": "branch-thread",
                    },
                    "input": "hello",
                },
            )
        )
        self.assertEqual(fact.explicit_session_id, "root-session")
        self.assertEqual(fact.thread_id, "branch-thread")
        self.assertNotEqual(fact.explicit_session_id, fact.thread_id)

    def test_trace_metadata_thread_and_parent_are_parsed(self) -> None:
        fact = parse_trace(
            _trace("t1", {"input": "hello"}, thread_id="child", parent_thread_id="parent")
        )
        self.assertEqual(fact.thread_id, "child")
        self.assertEqual(fact.parent_thread_id, "parent")

    def test_scalar_responses_input_is_single_user(self) -> None:
        fact = parse_trace(_trace("t1", {"input": "hello", "model": "gpt"}))
        self.assertEqual(fact.input_kind, "single_user")
        self.assertEqual(fact.history, ())

    def test_message_fingerprint_ignores_only_envelope_volatility(self) -> None:
        first, _, _ = conversation_history(
            {"messages": [{"role": "user", "content": {"text": "x", "status": "one"}}]}
        )
        second, _, _ = conversation_history(
            {"messages": [{"role": "user", "content": {"text": "x", "status": "two"}}]}
        )
        nested_first, _, _ = conversation_history(
            {"messages": [{"role": "user", "content": {"value": {"status": "one"}}}]}
        )
        nested_second, _, _ = conversation_history(
            {"messages": [{"role": "user", "content": {"value": {"status": "two"}}}]}
        )
        self.assertEqual(first, second)
        self.assertNotEqual(nested_first, nested_second)

    def test_message_fingerprint_keeps_tool_identity(self) -> None:
        first, _, _ = conversation_history(
            {
                "messages": [
                    {"role": "tool", "tool_call_id": "one", "content": "same"}
                ]
            }
        )
        second, _, _ = conversation_history(
            {
                "messages": [
                    {"role": "tool", "tool_call_id": "two", "content": "same"}
                ]
            }
        )
        self.assertNotEqual(first, second)

    def test_tool_output_counts_as_history_response(self) -> None:
        _, has_response, _ = conversation_history(
            {
                "messages": [
                    _u("question"),
                    {"type": "function_call_output", "call_id": "one", "output": "ok"},
                ]
            }
        )
        self.assertTrue(has_response)

    def test_recursive_session_envelope_does_not_treat_thread_as_session(self) -> None:
        session = parse_trace(
            _trace("session", {"request": {"context": {"sessionId": "root"}}})
        )
        thread = parse_trace(
            _trace("thread", {"request": {"context": {"threadId": "branch"}}})
        )
        self.assertEqual(session.explicit_session_id, "root")
        self.assertEqual(thread.explicit_session_id, "")

    def test_compact_hot_store_row_uses_precomputed_history(self) -> None:
        trace = _trace("compact", "")
        trace.update(
            {
                "_compact": True,
                "history_sequence": ["one", "two"],
                "history_has_response": 1,
                "history_turns": 1,
                "input_context": json.dumps({"conversation_id": "root"}),
            }
        )
        fact = parse_trace(trace)
        self.assertEqual(fact.history, ("one", "two"))
        self.assertEqual(fact.explicit_session_id, "root")

    def test_compact_user_only_history_is_not_treated_as_empty(self) -> None:
        trace = _trace("compact", "")
        trace.update(
            {
                "_compact": True,
                "history_sequence": ["one"],
                "history_has_response": 0,
                "history_turns": 1,
            }
        )
        self.assertEqual(parse_trace(trace).input_kind, "single_user")

    def test_compact_reader_can_classify_input_without_sending_body(self) -> None:
        trace = _trace("compact", "")
        trace.update(
            {
                "_compact": True,
                "history_sequence": [],
                "compact_input_kind": "single_user",
            }
        )
        self.assertEqual(parse_trace(trace).input_kind, "single_user")


class TraceDedupeTest(unittest.TestCase):
    def test_latest_trace_version_wins(self) -> None:
        old = _trace("same", {"input": "old"}, timestamp="2026-08-02T18:00:00Z")
        new = _trace("same", {"input": "new"}, timestamp="2026-08-02T18:01:00Z")
        unique, duplicate_count, invalid_count = dedupe_traces(
            [new, old, {"input": "bad"}]
        )
        self.assertEqual(duplicate_count, 1)
        self.assertEqual(invalid_count, 1)
        self.assertEqual(len(unique), 1)
        self.assertEqual(parse_trace(unique[0]).timestamp, "2026-08-02T18:01:00Z")

    def test_equal_timestamp_duplicate_is_deterministic(self) -> None:
        first = _trace("same", {"input": "first"})
        second = _trace("same", {"input": "second"})
        forward, _, _ = dedupe_traces([first, second])
        backward, _, _ = dedupe_traces([second, first])
        self.assertEqual(forward, backward)


class ExplicitAndThreadGraphTest(unittest.TestCase):
    def test_existing_session_anchors_all_traces_in_thread(self) -> None:
        traces = [
            _trace("anchor", {"input": "one"}, session_id="root", thread_id="thread"),
            _trace("missing", {"input": "two"}, thread_id="thread"),
        ]
        result = build_session_graph(traces)
        assignments = _assignments(result)
        self.assertEqual(assignments["anchor"].session_id, "root")
        self.assertEqual(assignments["missing"].session_id, "root")
        self.assertEqual(assignments["missing"].source, "thread.session_anchor")

    def test_multi_trace_thread_gets_provisional_branch_session(self) -> None:
        result = build_session_graph(
            [
                _trace("one", {"input": "one"}, thread_id="thread"),
                _trace("two", {"input": "two"}, thread_id="thread"),
                _trace("three", {"input": "three"}, thread_id="thread"),
            ]
        )
        assignments = _assignments(result)
        self.assertEqual(len({item.session_id for item in assignments.values()}), 1)
        node = next(node for node in result.nodes if node.session_id == assignments["one"].session_id)
        self.assertEqual(node.kind, "thread_provisional")
        self.assertEqual(node.thread_ids, ("thread",))

    def test_singleton_thread_without_other_evidence_is_excluded(self) -> None:
        result = build_session_graph([_trace("one", {"input": "one"}, thread_id="thread")])
        self.assertEqual(result.assignments, ())
        self.assertEqual(result.dispositions[0].reason, "single_user")

    def test_conflicting_session_anchors_do_not_guess_for_thread(self) -> None:
        result = build_session_graph(
            [
                _trace("one", {"input": "one"}, session_id="root-a", thread_id="thread"),
                _trace("two", {"input": "two"}, session_id="root-b", thread_id="thread"),
                _trace("missing", {"input": "three"}, thread_id="thread"),
            ]
        )
        assignments = _assignments(result)
        self.assertEqual(assignments["one"].session_id, "root-a")
        self.assertEqual(assignments["two"].session_id, "root-b")
        self.assertNotIn("missing", assignments)
        disposition = next(item for item in result.dispositions if item.trace_id == "missing")
        self.assertEqual(disposition.reason, "thread_session_conflict")

    def test_thread_fork_emits_graph_edge(self) -> None:
        result = build_session_graph(
            [
                _trace("p1", {"input": "parent one"}, thread_id="parent"),
                _trace("p2", {"input": "parent two"}, thread_id="parent"),
                _trace("c1", {"input": "child one"}, thread_id="child", parent_thread_id="parent"),
                _trace("c2", {"input": "child two"}, thread_id="child", parent_thread_id="parent"),
            ]
        )
        assignments = _assignments(result)
        self.assertIn(
            (assignments["p1"].session_id, assignments["c1"].session_id, "thread_fork"),
            {(edge.parent_session_id, edge.child_session_id, edge.kind) for edge in result.edges},
        )

    def test_thread_fork_parent_can_be_resolved_by_history(self) -> None:
        result = build_session_graph(
            [
                _trace(
                    "parent",
                    {"messages": [_u("one"), _a("answer")]},
                    thread_id="parent",
                ),
                _trace(
                    "child-one",
                    {"input": "one"},
                    thread_id="child",
                    parent_thread_id="parent",
                ),
                _trace(
                    "child-two",
                    {"input": "two"},
                    thread_id="child",
                    parent_thread_id="parent",
                ),
            ]
        )
        assignments = _assignments(result)
        self.assertIn(
            (assignments["parent"].session_id, assignments["child-one"].session_id),
            {(edge.parent_session_id, edge.child_session_id) for edge in result.edges},
        )


class ResponseChainGraphTest(unittest.TestCase):
    def test_previous_response_chain_recovers_scalar_inputs(self) -> None:
        result = build_session_graph(
            [
                _trace("one", {"input": "one"}, output={"id": "resp-one"}),
                _trace(
                    "two",
                    {"input": "two", "previous_response_id": "resp-one"},
                    output={"id": "resp-two"},
                ),
            ]
        )
        assignments = _assignments(result)
        self.assertEqual(len(assignments), 2)
        self.assertEqual(assignments["one"].session_id, assignments["two"].session_id)
        self.assertEqual(assignments["two"].source, "response_chain")

    def test_response_session_id_is_stable_when_chain_grows(self) -> None:
        first_two = [
            _trace("one", {"input": "one"}, output={"id": "resp-one"}),
            _trace(
                "two",
                {"input": "two", "previous_response_id": "resp-one"},
                output={"id": "resp-two"},
            ),
        ]
        initial = _assignments(build_session_graph(first_two))
        grown = _assignments(
            build_session_graph(
                first_two
                + [
                    _trace(
                        "three",
                        {"input": "three", "previous_response_id": "resp-two"},
                        output={"id": "resp-three"},
                    )
                ]
            )
        )
        self.assertEqual(initial["one"].session_id, grown["one"].session_id)

    def test_duplicate_response_id_does_not_guess_owner(self) -> None:
        result = build_session_graph(
            [
                _trace("one", {"input": "one"}, output={"id": "duplicate"}),
                _trace("two", {"input": "two"}, output={"id": "duplicate"}),
                _trace(
                    "child",
                    {"input": "child", "previous_response_id": "duplicate"},
                ),
            ]
        )
        self.assertEqual(result.assignments, ())
        disposition = next(
            item for item in result.dispositions if item.trace_id == "child"
        )
        self.assertEqual(disposition.reason, "duplicate_response_id")


class HistoryGraphTest(unittest.TestCase):
    def test_linear_history_uses_longest_leaf(self) -> None:
        prefix = [_u("u1")]
        result = build_session_graph(
            [
                _trace("one", {"messages": prefix}),
                _trace("two", {"messages": prefix + [_a("a1")]}),
                _trace("three", {"messages": prefix + [_a("a1"), _u("u2"), _a("a2")]}),
            ]
        )
        assignments = _assignments(result)
        self.assertEqual(len({item.session_id for item in assignments.values()}), 1)
        node = next(node for node in result.nodes if node.session_id == assignments["one"].session_id)
        self.assertEqual(node.kind, "history_leaf")
        self.assertEqual(node.representative_trace_id, "three")

    def test_shared_prefix_becomes_trunk_with_fork_edges(self) -> None:
        common = [_u("u1"), _a("a1")]
        result = build_session_graph(
            [
                _trace("prefix-one", {"messages": common}),
                _trace("prefix-two", {"messages": common + [_u("u2"), _a("a2")]}),
                _trace("left", {"messages": common + [_u("u2"), _a("a2"), _u("left"), _a("L")]}),
                _trace("right", {"messages": common + [_u("u2"), _a("a2"), _u("right"), _a("R")]}),
            ]
        )
        assignments = _assignments(result)
        self.assertEqual(assignments["prefix-one"].session_id, assignments["prefix-two"].session_id)
        trunk = next(node for node in result.nodes if node.session_id == assignments["prefix-one"].session_id)
        self.assertEqual(trunk.kind, "shared_prefix")
        child_sessions = {assignments["left"].session_id, assignments["right"].session_id}
        self.assertEqual(
            {(edge.parent_session_id, edge.child_session_id) for edge in result.edges if edge.kind == "history_fork"},
            {(trunk.session_id, child) for child in child_sessions},
        )
        self.assertEqual(result.report["shared_prefix_traces"], 2)

    def test_two_user_messages_without_response_remain_unresolved(self) -> None:
        result = build_session_graph(
            [_trace("weak", {"input": [_u("one"), _u("two")]})]
        )
        self.assertEqual(result.assignments, ())
        self.assertEqual(result.dispositions[0].reason, "user_only_history")

    def test_very_long_history_does_not_recurse(self) -> None:
        messages = [
            _u(f"u-{index}") if index % 2 == 0 else _a(f"a-{index}")
            for index in range(1400)
        ]
        result = build_session_graph([_trace("long", {"messages": messages})])
        self.assertEqual(len(result.assignments), 1)
        self.assertEqual(result.nodes[0].history_turns, 700)


class OutputTest(unittest.TestCase):
    def test_mixed_projects_are_rejected(self) -> None:
        first = _trace("first", {"input": "one"})
        second = _trace("second", {"input": "two"})
        second["project_id"] = "other-project"
        with self.assertRaisesRegex(ValueError, "one processing window"):
            build_session_graph([first, second])

    def test_history_is_stored_as_incremental_content_addressed_objects(self) -> None:
        first_history = [_u("one"), _a("answer")]
        result = build_session_graph(
            [
                _trace("first", {"messages": first_history}),
                _trace(
                    "second",
                    {"messages": first_history + [_u("two"), _a("second answer")]},
                ),
            ]
        )
        histories = {item.trace_id: item for item in result.histories}
        self.assertEqual(histories["first"].parent_trace_id, "")
        self.assertEqual(len(histories["first"].append_message_ids), 2)
        self.assertEqual(histories["second"].parent_trace_id, "first")
        self.assertEqual(histories["second"].prefix_length, 2)
        self.assertEqual(len(histories["second"].append_message_ids), 2)
        self.assertEqual(len(result.message_objects), 4)
        self.assertEqual(result.report["history_references"], 6)
        self.assertEqual(result.report["deduplicated_history_references"], 2)

    def test_graph_output_is_deterministic_and_contains_coverage(self) -> None:
        traces = [
            _trace("two", {"input": "two"}, thread_id="thread"),
            _trace("one", {"input": "one"}, thread_id="thread"),
            _trace("empty", ""),
        ]
        first = build_session_graph(traces)
        second = build_session_graph(reversed(traces))
        with tempfile.TemporaryDirectory() as directory:
            first_path = Path(directory) / "first.json"
            second_path = Path(directory) / "second.json"
            write_graph(first_path, first)
            write_graph(second_path, second)
            first_payload = json.loads(first_path.read_text(encoding="utf-8"))
            second_payload = json.loads(second_path.read_text(encoding="utf-8"))
        self.assertEqual(first_payload, second_payload)
        self.assertEqual(first_payload["report"]["traces_scanned"], 3)
        self.assertEqual(first_payload["report"]["traces_assigned"], 2)
        self.assertAlmostEqual(first_payload["report"]["raw_coverage"], 2 / 3)

    def test_cli_writes_objects_assignments_and_graph(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            input_path = root / "traces.jsonl"
            input_path.write_text(
                json.dumps(
                    _trace(
                        "trace",
                        {"messages": [_u("question"), _a("answer")]},
                    )
                )
                + "\n",
                encoding="utf-8",
            )
            graph_path = root / "graph.json"
            assignments_path = root / "assignments.jsonl"
            objects_path = root / "objects.jsonl"
            with contextlib.redirect_stdout(io.StringIO()):
                main(
                    [
                        "--input",
                        str(input_path),
                        "--graph-output",
                        str(graph_path),
                        "--assignments-output",
                        str(assignments_path),
                        "--objects-output",
                        str(objects_path),
                    ]
                )
            self.assertTrue(graph_path.is_file())
            self.assertEqual(len(assignments_path.read_text().splitlines()), 1)
            self.assertEqual(len(objects_path.read_text().splitlines()), 2)

    def test_cli_rejects_reused_output_path(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            input_path = root / "traces.jsonl"
            input_path.write_text("", encoding="utf-8")
            existing = root / "graph.json"
            existing.write_text("old", encoding="utf-8")
            with contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit):
                    main(
                        [
                            "--input",
                            str(input_path),
                            "--graph-output",
                            str(existing),
                            "--assignments-output",
                            str(root / "assignments.jsonl"),
                            "--objects-output",
                            str(root / "objects.jsonl"),
                        ]
                    )


if __name__ == "__main__":
    unittest.main()
