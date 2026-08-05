from __future__ import annotations

import unittest

from tools.wait_for_code_quality import QualityGateError, wait_for_verified_run


class FakeClock:
    def __init__(self) -> None:
        self.now = 0.0

    def monotonic(self) -> float:
        return self.now

    def sleep(self, seconds: float) -> None:
        self.now += seconds


class FakeClient:
    def __init__(
        self,
        *,
        runs: list[list[dict[str, object]]],
        run_states: list[dict[str, object]] | None = None,
        artifacts: list[list[dict[str, object]]] | None = None,
    ) -> None:
        self.runs = runs
        self.run_states = run_states or []
        self.artifacts = artifacts or []
        self._run_query = 0
        self._state_query = 0
        self._artifact_query = 0

    def list_runs(self, repository: str, workflow: str, commit: str) -> list[dict[str, object]]:
        index = min(self._run_query, len(self.runs) - 1)
        self._run_query += 1
        return self.runs[index]

    def get_run(self, repository: str, run_id: int) -> dict[str, object]:
        index = min(self._state_query, len(self.run_states) - 1)
        self._state_query += 1
        return self.run_states[index]

    def list_artifacts(self, repository: str, run_id: int) -> list[dict[str, object]]:
        index = min(self._artifact_query, len(self.artifacts) - 1)
        self._artifact_query += 1
        return self.artifacts[index]


class WaitForCodeQualityTest(unittest.TestCase):
    def test_waits_for_latest_push_run_and_returns_exact_commit_artifact(self) -> None:
        clock = FakeClock()
        client = FakeClient(
            runs=[[{
                "id": 41,
                "event": "push",
                "run_number": 8,
                "status": "in_progress",
                "conclusion": None,
                "html_url": "https://example.test/runs/41",
            }, {
                "id": 99,
                "event": "schedule",
                "run_number": 9,
                "status": "completed",
                "conclusion": "success",
                "html_url": "https://example.test/runs/99",
            }]],
            run_states=[
                {"id": 41, "status": "in_progress", "conclusion": None, "html_url": "https://example.test/runs/41"},
                {"id": 41, "status": "completed", "conclusion": "success", "html_url": "https://example.test/runs/41"},
            ],
            artifacts=[[{
                "id": 7,
                "name": "frontend-dist-deadbeef",
                "expired": False,
            }]],
        )

        result = wait_for_verified_run(
            client,
            repository="Alle-Group/sub2api",
            workflow="code-quality.yml",
            commit="deadbeef",
            artifact_name="frontend-dist-deadbeef",
            discovery_timeout=10,
            completion_timeout=30,
            artifact_timeout=10,
            poll_interval=1,
            monotonic=clock.monotonic,
            sleep=clock.sleep,
        )

        self.assertEqual(result.run_id, 41)
        self.assertEqual(result.artifact_name, "frontend-dist-deadbeef")
        self.assertEqual(result.run_url, "https://example.test/runs/41")

    def test_latest_eligible_run_failure_is_not_hidden_by_older_success(self) -> None:
        clock = FakeClock()
        client = FakeClient(
            runs=[[{
                "id": 42,
                "event": "workflow_dispatch",
                "run_number": 10,
                "status": "completed",
                "conclusion": "failure",
                "html_url": "https://example.test/runs/42",
            }, {
                "id": 41,
                "event": "push",
                "run_number": 8,
                "status": "completed",
                "conclusion": "success",
                "html_url": "https://example.test/runs/41",
            }]],
            run_states=[{
                "id": 42,
                "status": "completed",
                "conclusion": "failure",
                "html_url": "https://example.test/runs/42",
            }],
        )

        with self.assertRaisesRegex(QualityGateError, "concluded with failure"):
            wait_for_verified_run(
                client,
                repository="Alle-Group/sub2api",
                workflow="code-quality.yml",
                commit="deadbeef",
                artifact_name="frontend-dist-deadbeef",
                discovery_timeout=10,
                completion_timeout=30,
                artifact_timeout=10,
                poll_interval=1,
                monotonic=clock.monotonic,
                sleep=clock.sleep,
            )

    def test_fails_when_no_code_quality_run_appears(self) -> None:
        clock = FakeClock()
        client = FakeClient(runs=[[]])

        with self.assertRaisesRegex(QualityGateError, "No Code Quality run appeared"):
            wait_for_verified_run(
                client,
                repository="Alle-Group/sub2api",
                workflow="code-quality.yml",
                commit="deadbeef",
                artifact_name="frontend-dist-deadbeef",
                discovery_timeout=2,
                completion_timeout=30,
                artifact_timeout=10,
                poll_interval=1,
                monotonic=clock.monotonic,
                sleep=clock.sleep,
            )

    def test_fails_when_successful_run_has_no_reusable_frontend_artifact(self) -> None:
        clock = FakeClock()
        client = FakeClient(
            runs=[[{
                "id": 41,
                "event": "push",
                "run_number": 8,
                "status": "completed",
                "conclusion": "success",
                "html_url": "https://example.test/runs/41",
            }]],
            run_states=[{
                "id": 41,
                "status": "completed",
                "conclusion": "success",
                "html_url": "https://example.test/runs/41",
            }],
            artifacts=[[]],
        )

        with self.assertRaisesRegex(QualityGateError, "did not publish a non-expired artifact"):
            wait_for_verified_run(
                client,
                repository="Alle-Group/sub2api",
                workflow="code-quality.yml",
                commit="deadbeef",
                artifact_name="frontend-dist-deadbeef",
                discovery_timeout=10,
                completion_timeout=30,
                artifact_timeout=2,
                poll_interval=1,
                monotonic=clock.monotonic,
                sleep=clock.sleep,
            )


if __name__ == "__main__":
    unittest.main()
