import importlib
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import MagicMock, patch

sys.path.insert(0, str(Path(__file__).parents[1] / "images/ohmypi"))
checkout = importlib.import_module("prepare_repository")
runner = importlib.import_module("runner")


class RepositoryFixture(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        self.source = root / "source"
        self.source.mkdir()
        self.workspace = root / "workspace"
        self.workspace.mkdir()
        self.repo = self.workspace / "repo"
        self.repo.mkdir()
        self.results = root / "results"
        self.results.mkdir()
        self.task = self.workspace / "task.json"
        self.task.write_text(json.dumps({"repository_access": ["gaucho-racing/*"]}))
        subprocess.run(["git", "init", "-b", "trunk", str(self.source)], capture_output=True, check=True)
        (self.source / "source.py").write_text("before\n")
        self.git("add", "--all")
        self.git(
            "-c",
            "user.name=Fixture",
            "-c",
            "user.email=fixture@example.com",
            "-c",
            "commit.gpgsign=false",
            "commit",
            "-m",
            "Initial fixture",
        )
        self.base = self.git("rev-parse", "HEAD").strip()
        self.git("branch", "release")
        config = root / "gitconfig"
        config.write_text(f'[url "{self.source.as_uri()}"]\n\tinsteadOf = https://github.com/Gaucho-Racing/Repo.git\n')
        for setting in (
            patch.dict(os.environ, {"GIT_CONFIG_GLOBAL": str(config), "GIT_CONFIG_NOSYSTEM": "1"}),
            patch.object(checkout, "REPOSITORY", self.repo),
            patch.object(checkout, "RESULTS", self.results),
            patch.object(checkout, "TASK_FILE", self.task),
        ):
            setting.start()
            self.addCleanup(setting.stop)

    def git(self, *arguments):
        return subprocess.run(["git", *arguments], cwd=self.source, capture_output=True, text=True, check=True).stdout


class PrepareRepositoryTests(RepositoryFixture):
    def test_default_branch_is_discovered_and_resume_preserves_changes(self):
        result = checkout.prepare("Gaucho-Racing/Repo")
        self.assertEqual(result["base_branch"], "trunk")
        self.assertEqual(result["base_sha"], self.base)
        (self.repo / "source.py").write_text("pending changes\n")
        self.assertEqual(checkout.prepare("Gaucho-Racing/Repo"), result)
        self.assertEqual((self.repo / "source.py").read_text(), "pending changes\n")
        policy = json.loads((self.results / "documentation-policy.json").read_text())
        self.assertFalse(policy["allowed"])
        self.assertEqual(policy["base"], self.base)

    def test_agent_can_select_an_explicit_release_branch(self):
        result = checkout.prepare("Gaucho-Racing/Repo", "release", documentation=True)
        self.assertEqual(result["base_branch"], "release")
        self.assertTrue(json.loads((self.results / "documentation-policy.json").read_text())["allowed"])

    def test_disallowed_repositories_and_invalid_branches_do_not_clone(self):
        for repository in ("gaucho-racing-other/repo", "other/gaucho-racing", "other/repo"):
            with self.subTest(repository=repository), self.assertRaisesRegex(ValueError, "SANDBOX_REPOSITORIES"):
                checkout.prepare(repository)
        with self.assertRaises(subprocess.CalledProcessError):
            checkout.prepare("Gaucho-Racing/Repo", "../main")
        self.assertEqual(list(self.repo.iterdir()), [])
        self.assertFalse((self.results / "checkout.json").exists())


class RunnerWorkspaceTests(RepositoryFixture):
    def test_runner_starts_without_a_checkout_and_reports_agent_selected_changes(self):
        request = {
            "objective": "Fix the bug in Repo without publishing",
            "context": "Use the repository's default branch",
            "repository_access": ["gaucho-racing/*"],
            "requester_name": "Human Developer",
            "requester_email": "human@example.com",
            "requester_github_id": 123,
        }
        self.task.write_text(json.dumps(request))
        identity = {"commit_name": "Blix", "commit_email": "blix@example.com"}
        session = MagicMock()

        def execute(brief):
            self.assertIn(request["objective"], brief)
            self.assertFalse((self.repo / ".git").exists())
            checkout.prepare("Gaucho-Racing/Repo")
            (self.repo / "source.py").write_text("fixed\n")
            return "Changed the implementation without publishing."

        session.run.side_effect = execute
        for setting in (
            patch.object(runner, "TASK_FILE", self.task),
            patch.object(runner, "WORKSPACE", self.workspace),
            patch.object(runner, "REPOSITORY", self.repo),
            patch.object(runner, "RESULTS", self.results),
            patch.object(runner.Path, "home", return_value=self.workspace),
            patch.object(runner, "credentials", return_value=identity),
            patch.dict(os.environ, {"LLM_GATEWAY_ENDPOINT": "https://example.com", "LLM_GATEWAY_API_KEY": "fixture"}),
        ):
            setting.start()
            self.addCleanup(setting.stop)
        with patch.object(runner, "OmpSession", return_value=session) as launch:
            runner.run()
        result = json.loads((self.results / "result.json").read_text())
        self.assertEqual(result["base_sha"], self.base)
        self.assertIn("+fixed", result["patch"])
        self.assertIsNone(result["pr_url"])
        command = launch.call_args.args[0]
        system_prompt = command[command.index("--system-prompt") + 1]
        self.assertNotIn(request["objective"], system_prompt)
        self.assertEqual(launch.call_args.kwargs["repository"], self.workspace)
