import importlib.util
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("ensure_release_draft", Path(__file__).with_name("ensure-release-draft.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class EnsureReleaseDraftTest(unittest.TestCase):
    def snapshot(self, **changes):
        result = dict(apiUrl="https://api.github.com/repos/example/repo/releases/1",
                      tagName="v1", targetCommitish="commit", name="v1", body="notes",
                      isDraft=True, isPrerelease=False)
        result.update(changes)
        return result

    def check(self, initial, actual, write_status=0):
        def run(*args):
            self.assertEqual(args[:3], ("api", "--method", "PATCH" if initial else "POST"))
            data = json.loads(Path(args[-1]).read_text())
            self.assertEqual(data["tag_name"], "v1")
            self.assertEqual(data["target_commitish"], "commit")
            self.assertTrue(data["draft"])
            return subprocess.CompletedProcess(args, write_status, "", "ambiguous response")
        with patch.object(module, "read_release", side_effect=[initial, actual]), patch.object(module, "run_gh", side_effect=run):
            module.ensure_draft("example/repo", "v1", "commit", "notes")

    def test_failed_write_requires_complete_matching_readback(self):
        self.check(None, self.snapshot(), write_status=1)
        self.check(self.snapshot(), self.snapshot(), write_status=1)
        for field, value in {"tagName":"untagged", "targetCommitish":"wrong", "name":"wrong", "body":"old", "isDraft":False, "isPrerelease":True}.items():
            with self.subTest(field=field), self.assertRaises(RuntimeError):
                self.check(self.snapshot(), self.snapshot(**{field:value}), write_status=1)

    def test_successful_write_still_requires_readback(self):
        self.check(None, self.snapshot())
        with self.assertRaises(RuntimeError):
            self.check(None, None)

    def test_published_release_is_not_reverted(self):
        with patch.object(module, "read_release", return_value=self.snapshot(isDraft=False)), patch.object(module, "run_gh") as write:
            with self.assertRaises(RuntimeError):
                module.ensure_draft("example/repo", "v1", "commit", "notes")
            write.assert_not_called()

    def test_publish_preserves_tag_commit_and_notes(self):
        def run(*args):
            data = json.loads(Path(args[-1]).read_text())
            self.assertEqual(data["tag_name"], "v1")
            self.assertEqual(data["target_commitish"], "commit")
            self.assertEqual(data["body"], "notes")
            self.assertEqual(data["make_latest"], "true")
            self.assertFalse(data["draft"])
            return subprocess.CompletedProcess(args, 1, "", "ambiguous response")
        with patch.object(module, "read_release", side_effect=[self.snapshot(), self.snapshot(isDraft=False)]), patch.object(module, "run_gh", side_effect=run):
            module.ensure_draft("example/repo", "v1", "commit", None, publish=True, latest="true")
        with patch.object(module, "read_release", return_value=self.snapshot(targetCommitish="wrong")), patch.object(module, "run_gh") as write:
            with self.assertRaises(RuntimeError):
                module.ensure_draft("example/repo", "v1", "commit", None, publish=True, latest="true")
            write.assert_not_called()


if __name__ == "__main__":
    unittest.main()
