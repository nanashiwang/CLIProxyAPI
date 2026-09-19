#!/usr/bin/env python3
"""Verify management contracts against the built server without production data."""
import json
from datetime import datetime, timedelta, timezone
import os
from pathlib import Path
import secrets
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
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
        def request(path, method="GET", payload=None):
            req = urllib.request.Request(
                f"http://127.0.0.1:{port}/v0/management/{path}",
                method=method,
                data=json.dumps(payload).encode() if payload is not None else None,
                headers={"Authorization": "Bearer " + secret, "Content-Type": "application/json"},
            )
            with opener.open(req, timeout=3) as response:
                return json.load(response)

        def get(path):
            return request(path)
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
                verify_usage_insights(request)
            finally:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


def verify_usage_insights(request):
    """Exercise the real pagination/filter contracts without upstream traffic."""
    now = datetime.now(timezone.utc)
    records = []
    for index, (failed, generate) in enumerate([(False, True), (True, True), (False, False)]):
        records.append({
            "timestamp": (now - timedelta(minutes=index + 1)).isoformat(),
            "request_id": f"smoke-usage-{index}",
            "provider": "codex",
            "auth_id": "smoke-synthetic-account",
            "auth_index": "smoke-1",
            "auth_type": "oauth",
            "endpoint": "/v1/responses",
            "latency_ms": 1000,
            "ttft_ms": 100,
            "generate": generate,
            "failed": failed,
            "status_code": 503 if failed else 200,
            "tokens": {"input_tokens": 100, "output_tokens": 20, "total_tokens": 120},
            "billing": {"currency": "USD", "priced": False},
        })
        if index < 2:
            records[-1].update({
                "requested_model": "client-alias",
                "upstream_model": "sent-model",
                "upstream_response_model": "sent-model" if index == 0 else "returned-model",
                "upstream_response_model_source": "header" if index == 0 else "body",
                "reasoning_effort": "high" if index == 0 else "low",
                "client_transport": "ws" if index == 0 else "http",
                "upstream_transport": "sse" if index == 0 else "http",
                "client_ip": "192.0.2.41" if index == 0 else "2001:db8::42",
                "user_agent": "CPA-Usage-Smoke/1.0" if index == 0 else "CPA-Usage-Smoke/2.0",
            })
    imported = request("usage/import", "POST", {
        "version": 2,
        "usage": {"apis": {"smoke": {"models": {"smoke-model": {"details": records}}}}},
    })
    if imported.get("added") != 3:
        raise RuntimeError("usage insights fixture import failed")
    dashboard = request("usage/dashboard?range=24h")
    summary = dashboard.get("summary", {})
    if summary.get("total_requests") != 2 or summary.get("success_count") != 1:
        raise RuntimeError("usage dashboard must exclude prewarm records from inference totals")
    if dashboard.get("cost", {}).get("total_cost_usd") is not None:
        raise RuntimeError("unpriced usage must remain unknown rather than zero")
    first = request("usage/records?range=24h&page=1&page_size=1")
    second = request("usage/records?range=24h&page=2&page_size=1")
    if first.get("total") != 2 or len(first.get("items", [])) != 1:
        raise RuntimeError("usage records must provide bounded server pagination")
    if len(second.get("items", [])) != 1 or first["items"][0]["id"] == second["items"][0]["id"]:
        raise RuntimeError("usage record pages must not overlap")
    filtered = request("usage/records?range=24h&search=smoke-usage-1&page=1&page_size=20")
    if filtered.get("total") != 1 or filtered["items"][0].get("request_id") != "smoke-usage-1":
        raise RuntimeError("usage record search must filter retained records")
    record_id = first["items"][0]["id"]
    detail = request("usage/records/" + record_id)
    if detail.get("id") != record_id:
        raise RuntimeError("usage record detail must resolve the same stable record ID")
    if detail.get("requested_model") != "client-alias" or detail.get("model_match") != "matched":
        raise RuntimeError("model matching must compare sent and returned models, not the client alias")
    if second["items"][0].get("model_match") != "mismatch":
        raise RuntimeError("usage model mismatch was not preserved")
    returned = request("usage/records?range=24h&search=returned-model")
    if returned.get("total") != 1:
        raise RuntimeError("usage search must find the upstream reported model")
    legacy = request("usage/records?range=24h&search=smoke-usage-2&include_warmup=true")
    if legacy["items"][0].get("model_match") != "unknown":
        raise RuntimeError("legacy usage must not fabricate a model match")
    print("PASS usage model observations: mapped match, mismatch, unknown and model search")
    expected_metadata = {
        "reasoning_effort": "high", "client_transport": "ws", "upstream_transport": "sse",
        "client_ip": "192.0.2.41", "user_agent": "CPA-Usage-Smoke/1.0",
    }
    for field, expected in expected_metadata.items():
        if detail.get(field) != expected or first["items"][0].get(field) != expected:
            raise RuntimeError(f"usage record metadata must survive import, list and detail: {field}")
    for term in ("192.0.2.41", "CPA-Usage-Smoke/1.0", "sse", "high"):
        matches = request("usage/records?range=24h&search=" + urllib.parse.quote(term))
        if matches.get("total") != 1 or matches["items"][0].get("id") != record_id:
            raise RuntimeError("usage metadata search must identify the retained record")
    for field in expected_metadata:
        if legacy["items"][0].get(field):
            raise RuntimeError(f"historical usage must not fabricate metadata: {field}")
    exported = request("usage/export")
    exported_details = exported["usage"]["apis"]["smoke"]["models"]["smoke-model"]["details"]
    exported_first = next(row for row in exported_details if row["request_id"] == "smoke-usage-0")
    if any(exported_first.get(field) != expected for field, expected in expected_metadata.items()):
        raise RuntimeError("full usage backup must preserve request metadata")
    print("PASS usage columns: independent transports, reasoning, IP, User-Agent, search and backup")
    try:
        request("usage/records?page=0")
    except urllib.error.HTTPError as error:
        if error.code != 400:
            raise
    else:
        raise RuntimeError("usage records must reject invalid pagination")
    print("PASS usage dashboard, retained-record pagination, filtering, detail and unknown costs")


if __name__ == "__main__":
    main()
