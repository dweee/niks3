#!/usr/bin/env python3
import base64
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import unittest
from unittest.mock import patch

spec=importlib.util.spec_from_file_location("auth",Path(__file__).with_name("configure-attic-read.py"))
auth=importlib.util.module_from_spec(spec);spec.loader.exec_module(auth)

class PrivateReadTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.home = self.root / "home"
        self.home.mkdir()
        self.sha = "a" * 40
        self.event = {"repository": {"full_name": "owner/catalog", "owner": {"login": "owner"}},
                      "sender": {"login": "owner"}, "pull_request": {"head": {
                          "repo": {"full_name": "owner/catalog"}, "sha": self.sha}}}
        self.event_path = self.root / "event.json"
        self.event_path.write_text(json.dumps(self.event))
        self.claims = {"exp": int(time.time()) + 86400,
                       "https://jwt.attic.rs/v1": {"caches": {"flakey": {"r": 1}}}}
        self.env = {"RUNNER_TEMP": str(self.root), "GITHUB_EVENT_NAME": "pull_request",
                    "GITHUB_REPOSITORY": "owner/catalog", "GITHUB_REPOSITORY_OWNER": "owner",
                    "GITHUB_ACTOR": "owner", "GITHUB_EVENT_PATH": str(self.event_path),
                    "ATTIC_APPROVED_PR_SHA": self.sha, "ATTIC_PULL_TOKEN": self.jwt(self.claims)}

    def jwt(self, claims, header=None):
        def encode(value):
            return base64.urlsafe_b64encode(json.dumps(value).encode()).decode().rstrip("=")
        return encode(header or {"alg": "RS256"}) + "." + encode(claims) + ".c3ludGhldGlj"

    def rejected(self, env=None):
        with patch.dict(os.environ, self.env | (env or {}), clear=True), patch.object(Path, "home", return_value=self.home):
            with self.assertRaises((ValueError, KeyError, TypeError, AttributeError)):
                auth.configure(pr_read=True)
        self.assertFalse((self.root / "flakey-attic.netrc").exists())
        self.assertFalse((self.home / ".config/nix/nix.conf").exists())

    def test_reviewed_owner_pr_receives_only_private_pull_netrc(self):
        with patch.dict(os.environ, self.env, clear=True), patch.object(Path, "home", return_value=self.home):
            auth.configure(pr_read=True)
        netrc = self.root / "flakey-attic.netrc"
        self.assertEqual(netrc.stat().st_mode & 0o777, 0o600)
        self.assertIn("password " + self.env["ATTIC_PULL_TOKEN"], netrc.read_text())
        config = (self.home / ".config/nix/nix.conf").read_text()
        self.assertNotIn(self.env["ATTIC_PULL_TOKEN"], config)
        self.assertIn("netrc-file = " + str(netrc), config)
        self.assertIn("always-allow-substitutes = true", config)

    def test_unapproved_or_changed_sha_is_rejected_before_writes(self):
        for value in ("", "b" * 40, "not-a-sha"):
            with self.subTest(approved=value):
                self.rejected({"ATTIC_APPROVED_PR_SHA": value})

    def test_non_pr_event_is_rejected(self):
        for name in ("push", "pull_request_target", "workflow_dispatch", ""):
            with self.subTest(event=name):
                self.rejected({"GITHUB_EVENT_NAME": name})

    def test_non_owner_actor_or_repository_is_rejected(self):
        for update in ({"GITHUB_ACTOR": "collaborator"}, {"GITHUB_REPOSITORY_OWNER": "other"},
                       {"GITHUB_REPOSITORY": "other/catalog"}):
            with self.subTest(update=update):
                self.rejected(update)

    def test_fork_head_is_rejected(self):
        self.event["pull_request"]["head"]["repo"]["full_name"] = "fork/catalog"
        self.event_path.write_text(json.dumps(self.event))
        self.rejected()

    def test_event_owner_and_sender_are_checked(self):
        for field in ("repository", "sender"):
            changed = json.loads(json.dumps(self.event))
            if field == "repository": changed[field]["owner"]["login"] = "other"
            else: changed[field]["login"] = "other"
            self.event_path.write_text(json.dumps(changed))
            self.rejected()

    def test_malformed_missing_and_duplicate_event_fields_are_rejected(self):
        for value in ("{}", "null", '{"repository": {}, "repository": {}}', "invalid-json"):
            with self.subTest(value=value):
                self.event_path.write_text(value)
                self.rejected()

    def test_write_other_cache_admin_or_boolean_permissions_are_rejected(self):
        scopes = [{"caches": {"flakey": {"r": 1, "w": 1}}},
                  {"caches": {"flakey": {"r": 1}, "other": {"r": 1}}},
                  {"caches": {"*": {"r": 1}}},
                  {"caches": {"flakey": {"r": True}}},
                  {"caches": {"flakey": {"r": 1}}, "create_cache": True}]
        for scope in scopes:
            with self.subTest(scope=scope):
                self.rejected({"ATTIC_PULL_TOKEN": self.jwt(self.claims | {"https://jwt.attic.rs/v1": scope})})

    def test_expired_long_lived_future_or_missing_claims_are_rejected(self):
        now = int(time.time())
        for update in ({"exp": now - 1}, {"exp": now + 91 * 86400}, {"exp": True},
                       {"exp": None}, {"nbf": now + 60}, {"iat": now + 60},
                       {"iat": now - 91 * 86400}, {"nbf": True}, {"https://jwt.attic.rs/v1": None}):
            with self.subTest(update=update):
                self.rejected({"ATTIC_PULL_TOKEN": self.jwt(self.claims | update)})
        self.rejected({"ATTIC_PULL_TOKEN": self.jwt({})})

    def test_invalid_jwt_or_write_secret_environment_is_rejected(self):
        for token in ("", "a.b.c", "opaque", self.jwt(self.claims, {"alg": "none"}),
                      self.jwt(self.claims) + "\n", "a" * 16385):
            with self.subTest(token=token[:20]):
                self.rejected({"ATTIC_PULL_TOKEN": token})
        self.rejected({"ATTIC_TOKEN": "synthetic-write-token"})

    def test_cleanup_removes_netrc_and_managed_config_only(self):
        config = self.home / ".config/nix/nix.conf"
        config.parent.mkdir(parents=True)
        config.write_text("cores = 3\n")
        with patch.dict(os.environ, self.env, clear=True), patch.object(Path, "home", return_value=self.home):
            auth.configure(pr_read=True)
            auth.cleanup()
            auth.cleanup()
        self.assertFalse((self.root / "flakey-attic.netrc").exists())
        self.assertEqual(config.read_text().strip(), "cores = 3")

    def test_cli_refuses_invalid_scope_without_logging_credential(self):
        token = self.jwt(self.claims | {"https://jwt.attic.rs/v1": {"caches": {"flakey": {"r": 1, "w": 1}}}})
        result = subprocess.run(["python3", str(Path(__file__).with_name("configure-attic-read.py")), "--pr-read"],
                                env=os.environ | self.env | {"ATTIC_PULL_TOKEN": token}, capture_output=True, text=True)
        self.assertEqual(result.returncode, 1)
        self.assertNotIn(token, result.stdout + result.stderr)
        self.assertNotIn("Traceback", result.stderr)

    def test_native_workflows_keep_prs_credential_free_and_cleanup_before_export(self):
        workflows = sorted(Path(__file__).resolve().parents[1].joinpath(".github/workflows").glob("*-actions.yml"))
        self.assertTrue(workflows)
        for workflow in workflows:
            text = workflow.read_text()
            build = text.split("\n  publish:\n")[0]
            self.assertNotIn("ATTIC_PULL_TOKEN", build)
            read = build.split("    - name: Configure read-only Niks3 access with GitHub OIDC", 1)[1].split("    - name:", 1)[0]
            self.assertIn("github.event_name == 'push'", read)
            self.assertIn("github.event_name == 'workflow_dispatch'", read)
            self.assertIn("github.event.repository.default_branch", read)
            self.assertNotIn("pull_request", read)
            cleanup = build.index("--cleanup")
            self.assertLess(cleanup, build.index("ci/transfer-attic.py export"))
            self.assertIn("if: ${{ always() }}", build[build.rindex("    - name:", 0, cleanup):cleanup])
            self.assertIn('nix build "$target^*"', build)

    def test_netrc_is_private_and_token_does_not_enter_nix_config(self):
        with tempfile.TemporaryDirectory() as tmp:
            home=Path(tmp)/"home"; home.mkdir()
            token="synthetic-scoped-token"
            with patch.dict(os.environ,{"ATTIC_TOKEN":token,"RUNNER_TEMP":tmp},clear=True),patch.object(Path,"home",return_value=home): auth.configure()
            netrc=Path(tmp)/"flakey-attic.netrc"
            self.assertEqual(netrc.stat().st_mode & 0o777,0o600)
            self.assertEqual(netrc.read_text(),"machine attic.monke.fail login ci password "+token+"\n")
            config=(home/".config/nix/nix.conf").read_text()
            self.assertNotIn(token,config)
            self.assertIn("https://attic.monke.fail/flakey",config)
            self.assertIn("always-allow-substitutes = true",config)
            self.assertIn("netrc-file = "+str(netrc),config)

    def test_missing_or_multiline_tokens_are_rejected(self):
        for token in ["","two words","first\nsecond"]:
            with patch.dict(os.environ,{"ATTIC_TOKEN":token},clear=True):
                with self.assertRaises(ValueError): auth.configure()

    def test_no_pr_or_non_default_branch_receives_secret(self):
        for workflow in Path(__file__).resolve().parents[1].joinpath(".github/workflows").glob("*.yml"):
            text=workflow.read_text()
            if "Configure authenticated private cache" not in text: continue
            build=text.split("\n  publish:\n")[0]
            lines=build.splitlines()
            for index,line in enumerate(lines):
                if "secrets.ATTIC_TOKEN" in line:
                    preceding="\n".join(lines[max(0,index-5):index])
                    self.assertIn("github.event_name == 'push'",preceding)
                    self.assertIn("github.event_name == 'workflow_dispatch'",preceding)
                    self.assertIn("github.event.repository.default_branch",preceding)
                    self.assertNotIn("pull_request",preceding)

if __name__ == "__main__": unittest.main()
