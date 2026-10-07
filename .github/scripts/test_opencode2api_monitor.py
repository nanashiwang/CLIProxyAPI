"""Offline dry-run of the actual workflow Bash with a strict fake gh.

Run: python -m unittest discover -s .github/scripts -p test_opencode2api_monitor.py
Requires PyYAML and jq; no credentials or network calls are used.
"""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml

WORKFLOW = Path(__file__).resolve().parents[1] / 'workflows/opencode2api-monitor.yml'
FAKE_GH = r'''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
args = sys.argv[1:]
f = json.loads(Path(os.environ['FIXTURE']).read_text())
with open('gh-calls.jsonl', 'a') as log:
    log.write(json.dumps(args) + '\n')
if args[0] == 'api':
    endpoint = next(a for a in args[1:] if a.startswith('repos/'))
    if f.get('api_error') and endpoint == 'repos/test/repo':
        sys.exit(1)
    if endpoint == 'repos/test/repo':
        print('true' if f['issues'] else 'false')
    elif '/actions/artifacts?' in endpoint:
        if f.get('artifact_error'):
            sys.exit(1)
        for page in f.get('pages', [{'artifacts': []}]):
            print(json.dumps(page))
    elif '/commits/' in endpoint:
        print(f.get('head', 'new-head'))
    elif '/tags?' in endpoint:
        print(json.dumps([{'name': f.get('tag', 'v2'), 'commit': {'sha': f.get('tag_sha', 'tag-sha')}}]))
    elif '/releases/latest' in endpoint:
        print(json.dumps({'tag_name': f.get('release', 'v2')}))
    elif '/compare/' in endpoint:
        print(json.dumps({'files': [{'status': 'modified', 'filename': 'gateway.go'}]}))
    elif endpoint == 'repos/test/upstream':
        print('main')
    else:
        sys.exit('Unexpected API call: ' + repr(args))
elif args[0] == 'issue':
    if not f['issues']:
        sys.exit('Issues must not be called when disabled')
    if args[1] == 'list':
        print(f.get('number', ''))
    elif args[1] == 'view':
        print(json.dumps(f.get('issue', {})))
    elif args[1] in ('create', 'comment'):
        if f.get('write_error'):
            sys.exit(1)
        assert Path(args[args.index('--body-file') + 1]).is_file()
    else:
        sys.exit('Unexpected Issue call: ' + repr(args))
else:
    sys.exit('Unexpected gh call: ' + repr(args))
'''


class MonitorDryRun(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = yaml.safe_load(WORKFLOW.read_text())
        cls.script = next(s['run'] for s in cls.workflow['jobs']['monitor']['steps'] if s.get('id') == 'review')

    def run_monitor(self, **fixture):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'gh').write_text(FAKE_GH)
            (root / 'gh').chmod(0o755)
            (root / 'fixture.json').write_text(json.dumps(fixture))
            env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ['PATH'],
                       FIXTURE=str(root / 'fixture.json'), UPSTREAM_REPO='test/upstream',
                       ADAPTED_COMMIT='adapted', ADAPTED_TAG='v1', GITHUB_REPOSITORY='test/repo',
                       GITHUB_SERVER_URL='https://github.com',
                       GITHUB_STEP_SUMMARY=str(root / 'summary'), GITHUB_OUTPUT=str(root / 'outputs'))
            result = subprocess.run(['bash', '-euo', 'pipefail', '-c', self.script], cwd=root,
                                    env=env, text=True, capture_output=True)
            files = {p.name: p.read_text() for p in root.iterdir() if p.is_file()}
            return result, files, [json.loads(line) for line in files.get('gh-calls.jsonl', '').splitlines()]

    @staticmethod
    def marker(head='new-head', tag='v2', tag_sha='tag-sha', release='v2'):
        return f'opencode2api-status head={head} tag={tag}@{tag_sha} release={release}'

    def artifact(self, expired=False, **state):
        name = 'opencode2api-review-' + hashlib.sha256(self.marker(**state).encode()).hexdigest()
        return {'name': name, 'expired': expired, 'workflow_run': {'id': 123}}

    def test_disabled_saves_complete_report_without_issue_calls(self):
        result, files, calls = self.run_monitor(issues=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(any(c[0] == 'issue' for c in calls))
        self.assertIn(self.marker(), files['issue.md'])
        self.assertIn('gateway.go', files['issue.md'])
        self.assertIn('CPA 影响分析', files['issue.md'])
        self.assertIn(files['issue.md'], files['summary'])
        self.assertEqual(files['outputs'], 'artifact_name=' + self.artifact()['name'] + '\n')
        self.assertTrue(any('--paginate' in c for c in calls))

    def test_disabled_duplicate_on_later_page_links_existing_report(self):
        result, files, calls = self.run_monitor(issues=False, pages=[{'artifacts': []}, {'artifacts': [self.artifact()]}])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn('outputs', files)
        self.assertNotIn('issue.md', files)
        self.assertIn('https://github.com/test/repo/actions/runs/123', files['summary'])
        self.assertFalse(any(c[0] == 'issue' for c in calls))

    def test_expired_or_different_state_is_saved_again(self):
        for artifact in [self.artifact(expired=True), self.artifact(head='other'),
                         self.artifact(tag='other'), self.artifact(tag_sha='moved'), self.artifact(release='other')]:
            with self.subTest(artifact=artifact):
                result, files, _ = self.run_monitor(issues=False, pages=[{'artifacts': [artifact]}])
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn('outputs', files)

    def test_enabled_creates_or_comments_as_before(self):
        for number, operation in [('', 'create'), ('42', 'comment')]:
            with self.subTest(operation=operation):
                result, files, calls = self.run_monitor(issues=True, number=number)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertTrue(any(c[:2] == ['issue', operation] for c in calls))
                self.assertNotIn('outputs', files)
                self.assertFalse(any('/actions/artifacts?' in ' '.join(c) for c in calls))

    def test_enabled_duplicate_in_body_or_comment_does_not_write(self):
        for issue in [{'body': self.marker()}, {'comments': [{'body': self.marker()}]}]:
            result, _, calls = self.run_monitor(issues=True, number='42', issue=issue)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertFalse(any(c[:2] in [['issue', 'create'], ['issue', 'comment']] for c in calls))

    def test_no_change_does_not_check_destination(self):
        result, files, calls = self.run_monitor(issues=False, head='adapted', tag='v1', release='v1')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn('issue.md', files)
        self.assertFalse(any('repos/test/repo' in ' '.join(c) for c in calls))

    def test_unrelated_api_or_issue_write_failures_remain_failures(self):
        for fixture in [{'issues': False, 'api_error': True}, {'issues': False, 'artifact_error': True},
                        {'issues': True, 'write_error': True}]:
            result, _, _ = self.run_monitor(**fixture)
            self.assertNotEqual(result.returncode, 0)

    def test_upload_contract_and_shell_syntax(self):
        result = subprocess.run(['bash', '-n'], input=self.script, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.workflow['permissions']['actions'], 'read')
        upload = self.workflow['jobs']['monitor']['steps'][-1]
        self.assertEqual(upload['uses'], 'actions/upload-artifact@v4')
        self.assertEqual(upload['if'], "steps.review.outputs.artifact_name != ''")
        self.assertEqual(upload['with']['path'], 'issue.md')
        self.assertEqual(upload['with']['if-no-files-found'], 'error')
        self.assertEqual(upload['with']['retention-days'], 90)


if __name__ == '__main__':
    unittest.main()
