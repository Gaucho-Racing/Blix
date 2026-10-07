import importlib
import io
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parents[1] / "images/ohmypi"))
runner = importlib.import_module("runner")
helper = importlib.import_module("github_credentials")
gh = importlib.import_module("gh")
policy = importlib.import_module("documentation_policy")


class GitInterfaceTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.repo = Path(directory.name) / "repo"
        self.repo.mkdir()
        self.results = Path(directory.name) / "results"
        self.results.mkdir()
        self.identity = {"commit_name": "Blix", "commit_email": "123+blix[bot]@users.noreply.github.com"}
        self.request = {
            "objective": "Fix a bug",
            "repository": "team/repo",
            "base_branch": "main",
            "requester_github_id": 123,
            "requester_name": "Human Developer",
            "requester_email": "human@example.com",
            "requester_github_login": "human-dev",
        }
        self.git("init", "-b", "main")
        self.git("config", "user.name", self.identity["commit_name"])
        self.git("config", "user.email", self.identity["commit_email"])
        (self.repo / "source.py").write_text("before\n")
        self.git("add", "--all")
        self.git("commit", "-m", "chore: initial fixture")
        self.base = self.git("rev-parse", "HEAD").strip()
        repository_patch = patch.object(runner, "REPOSITORY", self.repo)
        results_patch = patch.object(runner, "RESULTS", self.results)
        repository_patch.start()
        results_patch.start()
        self.addCleanup(repository_patch.stop)
        self.addCleanup(results_patch.stop)

    def git(self, *arguments):
        return subprocess.run(
            ["git", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", *arguments],
            cwd=self.repo,
            check=True,
            capture_output=True,
            text=True,
        ).stdout

    def commit_fix(self, trailer=True):
        (self.repo / "source.py").write_text("after\n")
        self.git("add", "--all")
        message = "fix: correct the bug"
        if trailer:
            message += "\n\nCo-authored-by: Human Developer <human@example.com>"
        self.git("commit", "-m", message)

    def test_committed_changes_are_in_patch_and_credit_both_authors(self):
        self.commit_fix()
        result = runner.collect_result(self.request, self.base, "Tests passed", self.identity)
        self.assertIn("+after", result["patch"])
        self.assertEqual(result["changed_files"], ["source.py"])
        self.assertIn("Co-authored-by: Human Developer", result["commit_message"])

    def test_missing_coauthor_fails_validation(self):
        self.commit_fix(trailer=False)
        with self.assertRaisesRegex(RuntimeError, "coauthor"):
            runner.collect_result(self.request, self.base, "Tests passed", self.identity)

    def test_changed_bot_identity_fails_validation(self):
        self.git("config", "user.name", "Other Author")
        self.commit_fix()
        with self.assertRaisesRegex(RuntimeError, "bot author"):
            runner.collect_result(self.request, self.base, "Tests passed", self.identity)

    def test_agent_chosen_pr_checks_requester_assignment(self):
        self.commit_fix()
        (self.results / "pr.json").write_text(json.dumps({"url": "https://github.com/team/repo/pull/42"}))
        request = self.request
        pr = {"draft": False, "base": {"ref": "main"}, "user": {"type": "Bot"}, "assignees": [{"login": "human-dev"}]}
        real_run = subprocess.run

        def execute(command, **kwargs):
            if command[0] == "gh":
                return SimpleNamespace(stdout=json.dumps(pr))
            return real_run(command, **kwargs)

        with patch.object(runner.subprocess, "run", side_effect=execute):
            result = runner.collect_result(request, self.base, "Published", self.identity)
            self.assertEqual(result["pr_url"], "https://github.com/team/repo/pull/42")
            pr["draft"] = True
            runner.collect_result(request, self.base, "Published as requested", self.identity)
            pr["assignees"] = []
            with self.assertRaisesRegex(RuntimeError, "not assigned"):
                runner.collect_result(request, self.base, "Published", self.identity)

    def test_prompts_describe_the_actual_workflow_and_standards(self):
        prompt = runner.build_system_prompt(self.request)
        self.assertIn("You own Git branches, commits, pushes, PRs", prompt)
        self.assertIn("Co-authored-by: Human Developer <human@example.com>", prompt)
        self.assertIn("human-dev", prompt)
        self.assertIn("Repository-specific conventions", prompt)
        self.assertNotIn("Do not commit, push", prompt)
        prompt = runner.build_system_prompt({**self.request, "requester_github_id": None})
        self.assertIn("leave repository files and Git history unchanged", prompt)

    def test_documentation_is_blocked_before_commit_and_push(self):
        policy.configure_policy(self.repo, self.results, self.base, False)
        (self.repo / "README.md").write_text("Unrequested documentation\n")
        self.git("add", "--all")
        commit = subprocess.run(
            ["git", "-c", "commit.gpgsign=false", "commit", "-m", "fix: unwanted docs"],
            cwd=self.repo,
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(commit.returncode, 0)
        self.assertIn("not explicitly authorized", commit.stderr)
        self.assertEqual(self.git("rev-parse", "HEAD").strip(), self.base)
        self.git("commit", "-m", "fix: bypassed hook")
        hook = self.results / "git-policy-hooks" / "pre-push"
        pushed_sha = self.git("rev-parse", "HEAD").strip()
        push = subprocess.run(
            [str(hook)],
            cwd=self.repo,
            capture_output=True,
            text=True,
            input=f"refs/heads/main {pushed_sha} refs/heads/main {'0' * 40}\n",
        )
        self.assertNotEqual(push.returncode, 0)
        self.assertIn("README.md", push.stderr)
        with self.assertRaisesRegex(RuntimeError, "not explicitly authorized"):
            runner.collect_result(self.request, self.base, "Done", self.identity)

    def test_explicit_documentation_authorization_allows_commit(self):
        policy.configure_policy(self.repo, self.results, self.base, True)
        (self.repo / "README.md").write_text("Requested documentation\n")
        self.git("add", "--all")
        subprocess.run(
            ["git", "-c", "commit.gpgsign=false", "commit", "-m", "docs: requested update"],
            cwd=self.repo,
            capture_output=True,
            text=True,
            check=True,
        )

    def test_documentation_paths_include_nested_files_and_deletions(self):
        for filename in ["README", "web/README.md", "docs/example.py", "CHANGELOG.txt", "guide.rst"]:
            with self.subTest(filename=filename), self.assertRaises(RuntimeError):
                policy.validate_files([filename], False)
        policy.validate_files(["src/model.py", "web/src/page.tsx"], False)


class CredentialHelperTests(unittest.TestCase):
    def test_pr_creation_checks_policy_before_getting_credentials(self):
        with (
            patch.object(sys, "argv", ["gh", "pr", "create"]),
            patch.object(gh, "check_policy", side_effect=RuntimeError("Documentation blocked")),
            patch.object(gh, "credentials") as credentials,
        ):
            with self.assertRaisesRegex(RuntimeError, "Documentation blocked"):
                gh.main()
            credentials.assert_not_called()

    def test_git_helper_never_sends_credentials_to_other_hosts(self):
        with (
            patch.object(sys, "argv", ["helper", "get"]),
            patch.object(sys, "stdin", io.StringIO("protocol=https\nhost=example.com\n")),
            patch.object(helper, "credentials") as credentials,
        ):
            helper.main()
            credentials.assert_not_called()

    def test_gh_renews_credentials_for_each_command(self):
        with (
            patch.object(sys, "argv", ["gh", "pr", "view"]),
            patch.object(gh, "credentials", return_value={"token": "renewed-token"}) as credentials,
            patch.object(gh.subprocess, "run", return_value=SimpleNamespace(returncode=0)) as execute,
        ):
            self.assertEqual(gh.main(), 0)
            credentials.assert_called_once()
            self.assertEqual(execute.call_args.kwargs["env"]["GH_TOKEN"], "renewed-token")
            self.assertEqual(execute.call_args.args[0], ["/usr/bin/gh", "pr", "view"])
