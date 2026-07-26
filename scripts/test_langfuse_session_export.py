#!/usr/bin/env python3
"""Unit tests for ancestry join helpers (no Langfuse network)."""

from __future__ import annotations

import unittest

from langfuse_session_export import (
    WHOLE_THREAD_FORK_MESSAGE_ID,
    _event_sort_key,
    ancestor_prefix,
    dedupe_earliest,
    join_with_ancestors,
)


def _ev(name: str, message_id: str, session_id: str = "s") -> dict:
    return {
        "name": name,
        "message_id": message_id,
        "session_id": session_id,
        "input": message_id,
        "output": None,
        "start_time": "",
        "trace_id": "t",
        "metadata": {"message_id": message_id},
    }


class JoinAncestorsTest(unittest.TestCase):
    def test_thread_fork_includes_whole_parent_and_dedupes(self) -> None:
        parent = [
            _ev("chat.user", "agents", "parent"),
            _ev("chat.user", "t1", "parent"),
        ]
        child = [
            _ev("chat.fork", "fork:child", "child"),
            _ev("chat.user", "agents", "child"),  # duplicate of parent
            _ev("chat.user", "t1", "child"),
            _ev("chat.assistant", "a1", "child"),  # only on child
            _ev("chat.user", "fork-msg", "child"),
            _ev("chat.compact", "c1", "child"),
        ]
        out = join_with_ancestors(child, parent, WHOLE_THREAD_FORK_MESSAGE_ID)
        ids = [e["message_id"] for e in out]
        self.assertEqual(ids, ["agents", "t1", "a1", "fork-msg", "c1"])
        self.assertTrue(all(e["name"] != "chat.fork" for e in out))
        # earliest copy kept (parent session)
        self.assertEqual(out[0]["session_id"], "parent")
        self.assertEqual(out[1]["session_id"], "parent")

    def test_message_fork_cuts_at_message_id(self) -> None:
        parent = [
            _ev("chat.user", "m1", "parent"),
            _ev("chat.assistant", "m2", "parent"),
            _ev("chat.user", "m3", "parent"),
        ]
        child = [
            _ev("chat.fork", "fork:child", "child"),
            _ev("chat.user", "m4", "child"),
        ]
        prefix = ancestor_prefix(parent, "m2")
        self.assertEqual([e["message_id"] for e in prefix], ["m1", "m2"])
        out = join_with_ancestors(child, parent, "m2")
        self.assertEqual([e["message_id"] for e in out], ["m1", "m2", "m4"])

    def test_dedupe_earliest(self) -> None:
        events = [
            _ev("chat.user", "x", "a"),
            _ev("chat.user", "x", "b"),
            _ev("chat.user", "y", "b"),
        ]
        out = dedupe_earliest(events)
        self.assertEqual([(e["message_id"], e["session_id"]) for e in out], [("x", "a"), ("y", "b")])

    def test_same_timestamp_sorts_by_seq(self) -> None:
        ts = "2026-07-23T15:50:25.927Z"
        assistant = {
            "name": "chat.assistant",
            "message_id": "aaa",  # would sort before user if only message_id used
            "start_time": ts,
            "metadata": {"message_id": "aaa", "seq": 4},
        }
        user = {
            "name": "chat.user",
            "message_id": "zzz",
            "start_time": ts,
            "metadata": {"message_id": "zzz", "seq": 3},
        }
        ordered = sorted([assistant, user], key=_event_sort_key)
        self.assertEqual([e["name"] for e in ordered], ["chat.user", "chat.assistant"])


if __name__ == "__main__":
    unittest.main()
