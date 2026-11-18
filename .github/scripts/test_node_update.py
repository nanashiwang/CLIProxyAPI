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
            for function in ('refresh_installer', 'set_config_value', 'migrate_nginx_management', 'install_self_updating_command', 'update_cpa', 'verify'):
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
                expected = ['set_config_value', 'migrate_nginx_management', 'install_self_updating_command', 'update_cpa', 'verify']
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


    def test_config_update_preserves_v7_and_v8_contracts(self):
        fixtures = [
            ('server:\n    port: 9123\nmanagement:\n    secret-key: preserved', True),
            ('port: 9123\nremote-management:\n  secret-key: preserved\n  disable-control-panel: true\n', False),
            ('server:\n  port: 9123\nmanagement:\n  secret-key: preserved\n  disable-control-panel: true\nobservability:\n  usage:\n    usage-statistics-enabled: false\napi-keys:\n  openai-compatibility:\n    - id: stable-provider\n      keys: []\n', True),
        ]
        for original, v8 in fixtures:
            with self.subTest(v8=v8), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                config = root / 'config.yaml'
                config.write_text(original)
                body = f'source {shlex.quote(str(SCRIPT))}\nset_config_value\nconfigured_port\n'
                result = subprocess.run(['bash', '-c', body], env=dict(os.environ, CPA_INSTALL_DIR=directory), capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn('9123', result.stdout)
                updated = config.read_text()
                self.assertIn('secret-key: preserved', updated)
                self.assertIn('disable-control-panel: false', updated)
                self.assertIn('https://github.com/nanashiwang/Cli-Proxy-API-Management-Center', updated)
                self.assertEqual(next(root.glob('config.yaml.bak-*')).read_text(), original)
                self.assertEqual(config.stat().st_mode & 0o777, 0o600)
                if v8:
                    self.assertNotIn('remote-management:', updated)
                    self.assertNotIn('\nusage-statistics-enabled:', updated)
                    self.assertIn('    usage-statistics-enabled: true', updated)
                    self.assertNotIn('preserved  ', updated)
                    if 'id: stable-provider' in original:
                        self.assertIn('id: stable-provider', updated)

    def test_inline_mapping_aborts_without_overwriting_config(self):
        with tempfile.TemporaryDirectory() as directory:
            config = Path(directory) / 'config.yaml'
            original = 'server:\n  port: 9123\nmanagement: {secret-key: preserved}\n'
            config.write_text(original)
            result = subprocess.run(['bash', '-c', f'source {shlex.quote(str(SCRIPT))}\nset_config_value'], env=dict(os.environ, CPA_INSTALL_DIR=directory), capture_output=True, text=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('expected block mapping', result.stderr)
            self.assertEqual(config.read_text(), original)

    def test_installer_handles_current_v8_template(self):
        installer = SCRIPT.with_name('install.sh').read_text()
        python = installer.split("  python3 - <<'PY'\n", 1)[1].split('\nPY\n', 1)[0]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config = root / 'config.yaml'
            config.write_text((SCRIPT.parents[2] / 'config.example.yaml').read_text())
            proxy = root / 'sing-box.json'
            proxy.write_text('{"inbounds":[{"listen_port":1080,"users":[{"username":"fixture","password":"fixture"}]}]}')
            env = dict(os.environ, CONFIG_PATH=str(config), SING_BOX_CONFIG=str(proxy), CPA_MANAGEMENT_KEY='fixture-management-key', CPA_API_KEYS='fixture-client-key', CPA_PORT='9123', CPA_COMMERCIAL_MODE='true', CPA_LOG_LIMIT_MB='512', CPA_ERROR_LOG_MAX_FILES='10', CPA_PANEL_REPOSITORY='https://example.invalid/panel')
            result = subprocess.run(['python3', '-c', python], env=env, capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            text = config.read_text()
            self.assertIn('  port: 9123', text)
            self.assertIn('  secret-key: "fixture-management-key"', text)
            self.assertIn('  api-keys: ["fixture-client-key"]', text)
            self.assertIn('    logging-to-file: true', text)
            self.assertIn('    usage-statistics-enabled: true', text)
            self.assertIn('  proxy-url: "socks5://fixture:fixture@127.0.0.1:1080/"', text)
            self.assertIn('v(?:0|8)/management', installer)


    def test_managed_nginx_acl_migrates_and_rolls_back_on_failure(self):
        old = r'location ~ ^/(management\.html|v0/management(?:/|$)) {'
        for fail in (False, True):
            with self.subTest(fail=fail), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                sites = root / 'sites'
                sites.mkdir()
                config = sites / 'fixture'
                original = old + '\n  include /etc/nginx/snippets/cpa-management-allow.conf;\n}\n'
                config.write_text(original)
                unrelated = sites / 'unrelated'
                unrelated.write_text(old + '\n}\n')
                binaries = root / 'bin'
                binaries.mkdir()
                for command in ('nginx', 'systemctl'):
                    executable = binaries / command
                    executable.write_text('#!/bin/sh\nexit ' + ('1' if fail and command == 'nginx' else '0') + '\n')
                    executable.chmod(0o755)
                env = dict(os.environ, CPA_INSTALL_DIR=directory, CPA_NGINX_SITES_DIR=str(sites), PATH=str(binaries) + os.pathsep + os.environ['PATH'])
                result = subprocess.run(['bash', '-c', f'source {shlex.quote(str(SCRIPT))}\nmigrate_nginx_management'], env=env, capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode == 0, not fail, result.stderr)
                self.assertEqual(config.read_text(), original if fail else original.replace('v0/management', 'v(?:0|8)/management'))
                self.assertEqual(unrelated.read_text(), old + '\n}\n')
                self.assertEqual(next(root.glob('nginx-backup-*')).joinpath('0').read_text(), original)


    def test_unreachable_panel_makes_upgrade_verification_fail(self):
        body = f"""
source {shlex.quote(str(SCRIPT))}
DEPLOY_MODE=systemd
configured_port() {{ echo 8317; }}
systemctl() {{ echo active; }}
wait_for_management_panel() {{ return 1; }}
verify
"""
        result = subprocess.run(['bash', '-c', body], capture_output=True, text=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('did not become reachable', result.stderr)


if __name__ == '__main__':
    unittest.main()
