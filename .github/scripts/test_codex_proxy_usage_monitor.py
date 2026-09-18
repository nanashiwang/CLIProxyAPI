#!/usr/bin/env python3
"""Regression checks for review/adaptation boundaries and incomplete comparisons."""

import copy
import importlib.util
import json
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location("monitor", Path(__file__).with_name("codex-proxy-usage-monitor.py"))
monitor = importlib.util.module_from_spec(spec)
spec.loader.exec_module(monitor)


class MonitorTests(unittest.TestCase):
    def setUp(self):
        self.manifest = json.loads(monitor.MANIFEST.read_text())

    def test_checked_in_manifest(self):
        monitor.validate_manifest(self.manifest)

    def test_reviewed_head_does_not_imply_adaptation(self):
        manifest = copy.deepcopy(self.manifest)
        for feature in manifest["features"]:
            feature.update(status="in_progress", adapted_commit=None, validation=[])
        report = monitor.build_report(manifest, manifest["reviewed_commit"], None, lambda *args: self.fail("No comparison needed"))
        self.assertFalse(report["review"]["review_required"])
        for feature in report["features"]:
            self.assertEqual(feature["adaptation_status"], "in_progress")
            self.assertIsNone(feature["adapted_commit"])
            self.assertEqual(feature["comparison_basis"], "candidate_source_only")

    def test_feature_adaptation_baseline_is_independent(self):
        manifest = copy.deepcopy(self.manifest)
        manifest["features"][0].update(status="adapted", adapted_commit="a" * 40, validation=["test evidence"])
        calls = []

        def compare(base, head):
            calls.append(base)
            return {"status": "ahead", "files": [{"filename": "frontend/src/api/modules/usage.ts", "status": "modified"}]}

        report = monitor.build_report(manifest, manifest["reviewed_commit"], None, compare)
        self.assertFalse(report["review"]["review_required"])
        self.assertTrue(report["features"][0]["source_delta"]["review_required"])
        self.assertEqual(calls, ["a" * 40])

    def test_renamed_watched_file_is_detected(self):
        comparison = {"status": "ahead", "files": [{"filename": "moved/new.ts", "previous_filename": "frontend/src/views/usage/index.vue", "status": "renamed"}]}
        result = monitor.evaluate_comparison(comparison, ["frontend/src/views/usage/**"])
        self.assertTrue(result["review_required"])
        self.assertEqual(len(result["changed_files"]), 1)

    def test_truncated_or_rewritten_history_cannot_appear_clean(self):
        for comparison in ({"status": "ahead", "files": [{"filename": f"unrelated/{i}"} for i in range(300)]}, {"status": "diverged", "files": []}, {"status": "ahead"}):
            with self.subTest(comparison_status=comparison["status"]):
                result = monitor.evaluate_comparison(comparison, ["frontend/src/views/usage/**"])
                self.assertFalse(result["comparison_complete"])
                self.assertTrue(result["review_required"])

    def test_adapted_status_requires_actual_baseline_and_evidence(self):
        for adapted, evidence in ((None, ["pass"]), ("a" * 40, [])):
            manifest = copy.deepcopy(self.manifest)
            manifest["features"][0].update(status="adapted", adapted_commit=adapted, validation=evidence)
            with self.assertRaises(ValueError):
                monitor.validate_manifest(manifest)

    def test_shared_dependency_changes_require_review(self):
        result = monitor.evaluate_comparison({"status": "ahead", "files": [{"filename": "backend/Cargo.lock", "status": "modified"}]}, self.manifest["shared_watch_paths"])
        self.assertTrue(result["review_required"])


if __name__ == "__main__":
    unittest.main()
