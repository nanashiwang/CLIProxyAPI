import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zipfile

spec = importlib.util.spec_from_file_location("bundle", Path(__file__).with_name("build-deployment-bundle.py"))
bundle = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bundle)


class BundleTests(unittest.TestCase):
    def setUp(self):
        self.ui = b"<!doctype html><title>Management</title>"
        self.release = {"draft": False, "prerelease": False, "html_url": "https://github.com/example/release", "assets": [{"name": "management.html", "digest": "sha256:" + hashlib.sha256(self.ui).hexdigest()}]}
        self.paths = b"sdk/cliproxy/auth/codex_quota.go\0docs/CODEX_QUOTA_REFRESH_CN.md\0"

    def run_command(self, *args):
        if args[:2] == ("git", "rev-parse"):
            return b"commit\n"
        if args[:2] == ("git", "describe"):
            return b"v1-personal.1\n"
        if args[:2] == ("gh", "api"):
            return json.dumps(self.release).encode()
        if args[:3] == ("git", "diff", "--name-only"):
            return self.paths
        if args[:3] == ("git", "diff", "--binary"):
            return b"public source patch\n"
        if args[:2] == ("git", "ls-tree"):
            return b"docs/CODEX_QUOTA_REFRESH_CN.md\n"
        if args[:2] == ("git", "show"):
            return b"committed public documentation or license\n"
        if args[:2] == ("git", "log"):
            return "修复额度窗口刷新\n".encode()
        if args[:3] == ("gh", "release", "download"):
            (Path(args[-1]) / "management.html").write_bytes(self.ui)
            return b""
        raise AssertionError(args)

    def test_bundle_manifest_and_public_release_page_are_verified(self):
        with tempfile.TemporaryDirectory() as temp, patch.object(bundle, "run", self.run_command):
            output = Path(temp)
            bundle.build_bundle("v1-personal.2", "ui-v1", output)
            archive_path = next(output.glob("*.zip"))
            with zipfile.ZipFile(archive_path) as archive:
                self.assertIsNone(archive.testzip())
                manifest = json.loads(archive.read("manifest.json"))
                self.assertEqual(archive.read("management.html"), self.ui)
                self.assertEqual(manifest["base"], "v1-personal.1")
                for path, digest in manifest["files"].items():
                    self.assertEqual(hashlib.sha256(archive.read(path)).hexdigest(), digest)
                self.assertIn("修复额度窗口刷新", archive.read("DEPLOY_CN.md").decode())
            self.assertEqual(archive_path.with_suffix(".zip.sha256").read_text().split()[0], hashlib.sha256(archive_path.read_bytes()).hexdigest())

    def test_mismatched_management_digest_is_rejected(self):
        self.release["assets"][0]["digest"] = "sha256:wrong"
        with tempfile.TemporaryDirectory() as temp, patch.object(bundle, "run", self.run_command):
            with self.assertRaisesRegex(ValueError, "digest differs"):
                bundle.build_bundle("v1-personal.2", "ui-v1", Path(temp))
            self.assertEqual(list(Path(temp).glob("*.zip")), [])

    def test_runtime_or_private_paths_are_never_published(self):
        for path in [b".env\0", b"config.yaml\0", b"auths/account.json\0", b"secrets/server.pem\0", b".pool-leases.state\0"]:
            with self.subTest(path=path), tempfile.TemporaryDirectory() as temp, patch.object(bundle, "run", self.run_command):
                self.paths = path
                with self.assertRaisesRegex(ValueError, "private/runtime path"):
                    bundle.build_bundle("v1-personal.2", "ui-v1", Path(temp))


if __name__ == "__main__":
    unittest.main()
