#!/usr/bin/env python3
"""Verify management contracts against the built server without production data."""
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


def main():
    binary = str(Path(sys.argv[1]).resolve())
    with tempfile.TemporaryDirectory(prefix="cpa-release-smoke-") as temp:
        root = Path(temp)
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        secret = secrets.token_hex(32)
        config = root / "config.yaml"
        config.write_text(
            f'host: "127.0.0.1"\nport: {port}\n'
            f'auth-dir: {json.dumps(str(root / "auths"))}\n'
            'remote-management:\n  allow-remote: false\n'
            f'  secret-key: "{secret}"\n'
            '  disable-control-panel: true\n  disable-auto-update-panel: true\n'
            f'api-keys: ["{secrets.token_hex(32)}"]\n'
            'usage-statistics-enabled: false\nlogging-to-file: false\n'
        )
        config.chmod(0o600)
        environment = os.environ.copy()
        # Do not accidentally use CI/developer storage or authentication overrides.
        for key in list(environment):
            if key.startswith(("PGSTORE_", "GITSTORE_", "OBJECTSTORE_")) or key == "MANAGEMENT_PASSWORD":
                environment.pop(key)
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        def get(path):
            req = urllib.request.Request(
                f"http://127.0.0.1:{port}/v0/management/{path}",
                headers={"Authorization": "Bearer " + secret},
            )
            with opener.open(req, timeout=3) as response:
                return json.load(response)
        with (root / "server.log").open("w") as output:
            process = subprocess.Popen(
                [binary, "--config", str(config), "--local-model"],
                cwd=root, env=environment, stdout=output, stderr=subprocess.STDOUT,
            )
            try:
                deadline = time.monotonic() + 45
                while True:
                    if process.poll() is not None:
                        raise RuntimeError("server exited before management became ready")
                    try:
                        get("config")
                        break
                    except (urllib.error.URLError, TimeoutError):
                        if time.monotonic() >= deadline:
                            raise RuntimeError("management API startup timeout") from None
                        time.sleep(0.2)
                for path, fields in [
                    ("account-pools", ("config", "keys", "credentials", "revision")),
                    ("auth-files", ("files",)),
                    ("usage", ("usage", "storage")),
                ]:
                    try:
                        data = get(path)
                    except urllib.error.HTTPError as error:
                        raise RuntimeError(f"{path} returned HTTP {error.code}") from None
                    if not isinstance(data, dict) or any(field not in data for field in fields):
                        raise RuntimeError(f"{path} response does not satisfy the management contract")
                    print(f"PASS /v0/management/{path}")
            finally:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


if __name__ == "__main__":
    main()
