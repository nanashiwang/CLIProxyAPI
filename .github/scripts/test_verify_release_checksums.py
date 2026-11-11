import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("checksums", Path(__file__).with_name("verify-release-checksums.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ReleaseChecksumsTests(unittest.TestCase):
    def setUp(self):
        self.name = "CLIProxyAPI_test_linux_amd64.tar.gz"
        self.release = {"assets": [{"name": self.name, "digest": "sha256:" + "a" * 64}, {"name": "checksums.txt"}]}
        self.valid = "a" * 64 + "  " + self.name + "\n"

    def test_current_release(self):
        module.verify_checksums(self.valid, self.release)

    def test_old_attempt_is_rejected(self):
        with self.assertRaises(ValueError):
            module.verify_checksums("b" * 64 + "  " + self.name + "\n", self.release)

    def test_missing_extra_and_duplicate_archives(self):
        for text in ("", self.valid + "c" * 64 + "  CLIProxyAPI_extra.zip\n", self.valid * 2):
            with self.subTest(text=text), self.assertRaises(ValueError):
                module.verify_checksums(text, self.release)

    def test_missing_digest_and_empty_release(self):
        for release in ({"assets": []}, {"assets": [{"name": self.name, "digest": None}]}):
            with self.subTest(release=release), self.assertRaises(ValueError):
                module.verify_checksums(self.valid, release)


if __name__ == "__main__":
    unittest.main()
