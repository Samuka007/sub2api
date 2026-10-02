from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Callable, Protocol


ALLOWED_EVENTS = {"push", "workflow_dispatch"}
ACTIVE_STATUSES = {"queued", "in_progress", "pending", "requested", "waiting"}


class QualityGateError(RuntimeError):
    pass


@dataclass(frozen=True)
class VerifiedRun:
    run_id: int
    run_url: str
    artifact_name: str


class ActionsClient(Protocol):
    def list_runs(self, repository: str, workflow: str, commit: str) -> list[dict[str, object]]: ...

    def get_run(self, repository: str, run_id: int) -> dict[str, object]: ...

    def list_artifacts(self, repository: str, run_id: int) -> list[dict[str, object]]: ...


class GitHubActionsClient:
    def _get(self, path: str, **fields: object) -> dict[str, object]:
        command = ["gh", "api", "--method", "GET", path]
        for name, value in fields.items():
            command.extend(["-f", f"{name}={value}"])
        result = subprocess.run(
            command,
            check=False,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            timeout=60,
        )
        if result.returncode != 0:
            detail = result.stderr.strip() or result.stdout.strip() or "unknown gh api error"
            raise QualityGateError(f"GitHub Actions API request failed for {path}: {detail}")
        try:
            payload = json.loads(result.stdout)
        except json.JSONDecodeError as error:
            raise QualityGateError(f"GitHub Actions API returned invalid JSON for {path}: {error}") from error
        if not isinstance(payload, dict):
            raise QualityGateError(f"GitHub Actions API returned a non-object response for {path}")
        return payload

    def list_runs(self, repository: str, workflow: str, commit: str) -> list[dict[str, object]]:
        payload = self._get(
            f"repos/{repository}/actions/workflows/{workflow}/runs",
            head_sha=commit,
            per_page=100,
        )
        runs = payload.get("workflow_runs")
        if not isinstance(runs, list):
            raise QualityGateError("GitHub Actions API response did not contain workflow_runs")
        return [run for run in runs if isinstance(run, dict)]

    def get_run(self, repository: str, run_id: int) -> dict[str, object]:
        return self._get(f"repos/{repository}/actions/runs/{run_id}")

    def list_artifacts(self, repository: str, run_id: int) -> list[dict[str, object]]:
        payload = self._get(
            f"repos/{repository}/actions/runs/{run_id}/artifacts",
            per_page=100,
        )
        artifacts = payload.get("artifacts")
        if not isinstance(artifacts, list):
            raise QualityGateError("GitHub Actions API response did not contain artifacts")
        return [artifact for artifact in artifacts if isinstance(artifact, dict)]


def _latest_eligible_run(runs: list[dict[str, object]]) -> dict[str, object] | None:
    eligible = [run for run in runs if run.get("event") in ALLOWED_EVENTS]
    if not eligible:
        return None

    def run_order(run: dict[str, object]) -> int:
        value = run.get("run_number")
        return value if isinstance(value, int) else -1

    return max(eligible, key=run_order)


def wait_for_verified_run(
    client: ActionsClient,
    *,
    repository: str,
    workflow: str,
    commit: str,
    artifact_name: str,
    discovery_timeout: float,
    completion_timeout: float,
    artifact_timeout: float,
    poll_interval: float,
    monotonic: Callable[[], float] = time.monotonic,
    sleep: Callable[[float], None] = time.sleep,
) -> VerifiedRun:
    discovery_deadline = monotonic() + discovery_timeout
    candidate: dict[str, object] | None = None
    while candidate is None:
        candidate = _latest_eligible_run(client.list_runs(repository, workflow, commit))
        if candidate is not None:
            break
        if monotonic() >= discovery_deadline:
            raise QualityGateError(
                f"No Code Quality run appeared for commit {commit}; run Code Quality on the default branch before releasing"
            )
        sleep(poll_interval)

    raw_run_id = candidate.get("id")
    if not isinstance(raw_run_id, int):
        raise QualityGateError("Code Quality run did not contain a numeric id")
    run_id = raw_run_id

    completion_deadline = monotonic() + completion_timeout
    while True:
        run = client.get_run(repository, run_id)
        status = run.get("status")
        conclusion = run.get("conclusion")
        run_url = str(run.get("html_url") or candidate.get("html_url") or "")

        if status == "completed":
            if conclusion != "success":
                raise QualityGateError(
                    f"Code Quality run {run_id} concluded with {conclusion or 'an unknown result'}: {run_url}"
                )
            break
        if status not in ACTIVE_STATUSES:
            raise QualityGateError(f"Code Quality run {run_id} has unsupported status {status!r}: {run_url}")
        if monotonic() >= completion_deadline:
            raise QualityGateError(f"Timed out waiting for Code Quality run {run_id}: {run_url}")
        sleep(poll_interval)

    artifact_deadline = monotonic() + artifact_timeout
    while True:
        artifacts = client.list_artifacts(repository, run_id)
        for artifact in artifacts:
            if artifact.get("name") == artifact_name and artifact.get("expired") is False:
                return VerifiedRun(run_id=run_id, run_url=run_url, artifact_name=artifact_name)
        if monotonic() >= artifact_deadline:
            raise QualityGateError(
                f"Successful Code Quality run {run_id} did not publish a non-expired artifact named {artifact_name}"
            )
        sleep(poll_interval)


def _positive_number(value: str) -> float:
    number = float(value)
    if number <= 0:
        raise argparse.ArgumentTypeError("must be greater than zero")
    return number


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Wait for an existing Code Quality run and return its exact frontend artifact."
    )
    parser.add_argument("--repository", default=os.environ.get("GITHUB_REPOSITORY"), required=False)
    parser.add_argument("--workflow", default="code-quality.yml")
    parser.add_argument("--commit", required=True)
    parser.add_argument("--artifact-name")
    parser.add_argument("--output", default=os.environ.get("GITHUB_OUTPUT"))
    parser.add_argument("--discovery-timeout", type=_positive_number, default=120)
    parser.add_argument("--completion-timeout", type=_positive_number, default=3300)
    parser.add_argument("--artifact-timeout", type=_positive_number, default=30)
    parser.add_argument("--poll-interval", type=_positive_number, default=10)
    args = parser.parse_args()
    if not args.repository:
        parser.error("--repository or GITHUB_REPOSITORY is required")
    if not args.output:
        parser.error("--output or GITHUB_OUTPUT is required")
    return args


def main() -> int:
    args = parse_args()
    artifact_name = args.artifact_name or f"frontend-dist-{args.commit}"
    try:
        result = wait_for_verified_run(
            GitHubActionsClient(),
            repository=args.repository,
            workflow=args.workflow,
            commit=args.commit,
            artifact_name=artifact_name,
            discovery_timeout=args.discovery_timeout,
            completion_timeout=args.completion_timeout,
            artifact_timeout=args.artifact_timeout,
            poll_interval=args.poll_interval,
        )
    except QualityGateError as error:
        print(f"error: {error}", file=sys.stderr)
        return 1

    output = Path(args.output)
    with output.open("a", encoding="utf-8") as stream:
        stream.write(f"run_id={result.run_id}\n")
        stream.write(f"run_url={result.run_url}\n")
        stream.write(f"artifact_name={result.artifact_name}\n")
    print(f"Reusing Code Quality run {result.run_id}: {result.run_url}")
    print(f"Reusing frontend artifact: {result.artifact_name}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
