from __future__ import annotations

import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from tools import check_repository_governance as governance


SCRIPT = Path(__file__).with_name("check_repository_governance.py")


class RepositoryGovernanceTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        self.run_git("init", "-q")

        for relative_path in governance.REQUIRED_FILES:
            path = self.root / relative_path
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("fixture\n", encoding="utf-8")

        for relative_path, target in governance.REQUIRED_SYMLINKS.items():
            path = self.root / relative_path
            path.parent.mkdir(parents=True, exist_ok=True)
            path.symlink_to(target)

        (self.root / ".gitignore").write_text(
            "docs/superpowers/\n/openspec/changes/\ncode-reviews/\n/skills/\n",
            encoding="utf-8",
        )
        self.run_git("add", ".")

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def run_git(self, *args: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            ["git", *args],
            cwd=self.root,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=True,
        )

    def run_check(self) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [sys.executable, str(SCRIPT)],
            cwd=self.root,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )

    def test_accepts_complete_repository(self) -> None:
        result = self.run_check()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Repository governance check passed", result.stdout)

    def test_rejects_missing_required_file(self) -> None:
        (self.root / "AGENTS.md").unlink()

        result = self.run_check()

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("required non-empty file is missing: AGENTS.md", result.stderr)

    def test_rejects_forbidden_tracked_artifact(self) -> None:
        artifact = self.root / "docs/superpowers/leak.md"
        artifact.parent.mkdir(parents=True, exist_ok=True)
        artifact.write_text("leak\n", encoding="utf-8")
        self.run_git("add", "-f", "docs/superpowers/leak.md")

        result = self.run_check()

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("local working artifact is tracked: docs/superpowers/leak.md", result.stderr)

    def test_rejects_wrong_skill_discovery_symlink(self) -> None:
        link = self.root / ".codex/skills"
        link.unlink()
        link.symlink_to("../wrong/skills")

        result = self.run_check()

        self.assertNotEqual(result.returncode, 0)
        self.assertIn(
            "Skill discovery symlink has wrong target: .codex/skills -> ../wrong/skills",
            result.stderr,
        )

    def test_rejects_missing_ignore_boundaries(self) -> None:
        (self.root / ".gitignore").write_text("", encoding="utf-8")

        result = self.run_check()

        self.assertNotEqual(result.returncode, 0)
        for sample in governance.IGNORED_WORK_SAMPLES:
            self.assertIn(f"local working artifact is not ignored: {sample}", result.stderr)


if __name__ == "__main__":
    unittest.main()
