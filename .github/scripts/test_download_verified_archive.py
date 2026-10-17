#!/usr/bin/env python3
"""Exercise archive authentication and cleanup without downloading a sysroot."""
import hashlib
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("download-verified-archive.sh").resolve()
PAYLOAD = b"pinned sysroot fixture\n"
SHA256 = hashlib.sha256(PAYLOAD).hexdigest()


class DownloadTest(unittest.TestCase):
    def run_download(self, sources, expected_success, expected_calls):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "good").write_bytes(PAYLOAD)
            (root / "curl").write_text('''#!/usr/bin/env bash
set -eu
out=""
while [[ "$#" -gt 1 ]]; do
  if [[ "$1" == --output ]]; then out="$2"; shift; fi
  shift
done
printf '%s\\n' "$1" >> "$FIXTURE_ROOT/calls"
case "$1" in
  https://fixture/good) cp "$FIXTURE_ROOT/good" "$out" ;;
  https://fixture/corrupt) printf 'wrong bytes' > "$out" ;;
  *) printf 'partial response' > "$out"; exit 28 ;;
esac
''')
            (root / "curl").chmod(0o755)
            archive = root / "archive"
            archive.write_text("stale previous success")
            result = subprocess.run(
                ["bash", str(SCRIPT), str(archive), SHA256, *sources],
                env={**os.environ, "PATH": f"{root}:{os.environ['PATH']}", "FIXTURE_ROOT": str(root)},
                capture_output=True, text=True, timeout=10,
            )
            self.assertEqual(result.returncode == 0, expected_success, result.stdout + result.stderr)
            self.assertFalse(Path(str(archive) + ".partial").exists())
            self.assertEqual((root / "calls").read_text().splitlines(), expected_calls)
            if expected_success:
                self.assertEqual(archive.read_bytes(), PAYLOAD)
            else:
                self.assertFalse(archive.exists())

    def test_primary_success_stops(self):
        self.run_download(["https://fixture/good", "https://fixture/fail"], True, ["https://fixture/good"])

    def test_transport_failure_uses_fallback(self):
        sources = ["https://fixture/fail", "https://fixture/good"]
        self.run_download(sources, True, sources)

    def test_corruption_rejected_before_fallback(self):
        sources = ["https://fixture/corrupt", "https://fixture/good"]
        self.run_download(sources, True, sources)

    def test_all_fail_removes_stale_and_partial(self):
        sources = ["https://fixture/fail", "https://fixture/corrupt"]
        self.run_download(sources, False, sources)


if __name__ == "__main__":
    unittest.main()
