#!/usr/bin/env python3
"""Reject checksum files derived from obsolete workflow artifacts."""
import json
from pathlib import Path
import re
import sys


def verify_checksums(text, release):
    expected = {}
    for asset in release["assets"]:
        name = asset["name"]
        if name.startswith("CLIProxyAPI_") and name.endswith((".tar.gz", ".zip")):
            digest = asset.get("digest", "") or ""
            if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
                raise ValueError(f"Missing GitHub digest for {name}")
            expected[name] = digest[7:]
    actual = {}
    for line in text.splitlines():
        match = re.fullmatch(r"([0-9a-f]{64}) [ *](.+)", line)
        if not match or match[2] in actual:
            raise ValueError("Invalid or duplicate checksum entry")
        actual[match[2]] = match[1]
    if not expected or actual != expected:
        raise ValueError("Checksums do not match the current Release archives")


if __name__ == "__main__":
    verify_checksums(Path(sys.argv[1]).read_text(), json.loads(Path(sys.argv[2]).read_text()))
    print("Verified current Release archive digests")
