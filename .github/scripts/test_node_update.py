#!/usr/bin/env python3
"""Exercise updater preflight without touching a service or host installation."""
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[2] / 'deploy/cpa-node/update-node.sh'


class NodeUpdateTests(unittest.TestCase):
    def run_case(self, mode='systemd', version='7.2.135-personal.72', latest='7.2.135-personal.72', missing=False, discovery_failure=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'config.yaml').write_text('port: 8317\n')
            config_before = (root / 'config.yaml').stat().st_mtime_ns
            binary = root / 'cli-proxy-api'
            binary.write_text('#!/bin/sh\nprintf "%s\\n" ' + shlex.quote(f'CLIProxyAPI Version: {version}, Commit: fixture, BuiltAt: fixture') + '\n')
            binary.chmod(0o755)
            if missing:
                binary.unlink()
            trace = root / 'trace'
            body = f'''
source {shlex.quote(str(SCRIPT))}
require_root() {{ :; }}
docker() {{
  if [[ "$1" == inspect ]]; then return 0; fi
  if [[ "$1" == exec ]]; then "${{INSTALL_DIR}}/cli-proxy-api" -h; return $?; fi
  echo unexpected-docker-mutation >> {shlex.quote(str(trace))}; return 1
}}
wait_for_release_asset() {{
  {'fail "release unavailable"' if discovery_failure else 'RELEASE_VERSION=' + shlex.quote(latest)}
}}
'''
            for function in ('refresh_installer', 'set_config_value', 'install_self_updating_command', 'update_cpa', 'verify'):
                body += f'{function}() {{ echo {function} >> {shlex.quote(str(trace))}; }}\n'
            body += 'main\n'
            env = dict(os.environ, CPA_DEPLOY_MODE=mode, CPA_INSTALL_DIR=directory, CPA_DOCKER_DIR=directory)
            result = subprocess.run(['bash', '-c', body], env=env, capture_output=True, text=True, timeout=10)
            actions = trace.read_text().splitlines() if trace.exists() else []
            self.assertEqual((root / 'config.yaml').stat().st_mtime_ns, config_before)
            self.assertEqual(list(root.glob('config.yaml.bak-*')), [])
            return result, actions

    def test_equal_versions_have_no_side_effects(self):
        for mode in ('systemd', 'docker'):
            with self.subTest(mode=mode):
                result, actions = self.run_case(mode=mode)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn('already up to date', result.stdout)
                self.assertEqual(actions, [])

    def test_new_version_keeps_upgrade_flow(self):
        for mode in ('systemd', 'docker'):
            with self.subTest(mode=mode):
                result, actions = self.run_case(mode=mode, latest='7.2.135-personal.73')
                self.assertEqual(result.returncode, 0, result.stderr)
                expected = ['set_config_value', 'install_self_updating_command', 'update_cpa', 'verify']
                if mode == 'systemd':
                    expected.insert(0, 'refresh_installer')
                self.assertEqual(actions, expected)

    def test_unknown_and_missing_versions_abort_without_mutations(self):
        for mode in ('systemd', 'docker'):
            for options in ({'missing': True}, {'version': 'dev'}, {'version': 'unknown'}, {'discovery_failure': True}):
                with self.subTest(mode=mode, options=options):
                    result, actions = self.run_case(mode=mode, **options)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(actions, [])

    def test_prefixed_version_is_normalized(self):
        result, actions = self.run_case(version='v7.2.135-personal.72')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(actions, [])

    def test_slow_startup_retries_quietly(self):
        body = f'''
source {shlex.quote(str(SCRIPT))}
configured_port() {{ echo 8317; }}
SECONDS=0
calls=0
curl() {{
  calls=$((calls + 1))
  if ((calls < 44)); then echo connection-refused >&2; return 7; fi
  return 0
}}
sleep() {{ SECONDS=$((SECONDS + 1)); }}
wait_for_management_panel
[[ "$calls" -eq 44 ]]
'''
        result = subprocess.run(['bash', '-c', body], capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stderr, '')

    def test_readiness_has_a_deadline(self):
        body = f'''
source {shlex.quote(str(SCRIPT))}
configured_port() {{ echo 8317; }}
SECONDS=0
curl() {{ SECONDS=$((SECONDS + 5)); return 7; }}
sleep() {{ SECONDS=$((SECONDS + 1)); }}
if wait_for_management_panel; then exit 9; fi
[[ "$SECONDS" -ge 180 && "$SECONDS" -le 185 ]]
'''
        result = subprocess.run(['bash', '-c', body], capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == '__main__':
    unittest.main()
