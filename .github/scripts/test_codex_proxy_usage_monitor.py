#!/usr/bin/env python3
"""Regression checks for review/adaptation boundaries and incomplete comparisons."""

import copy
import importlib.util
import json
from pathlib import Path
import re
import unittest


spec = importlib.util.spec_from_file_location("monitor", Path(__file__).with_name("codex-proxy-usage-monitor.py"))
monitor = importlib.util.module_from_spec(spec)
spec.loader.exec_module(monitor)


class MonitorTests(unittest.TestCase):
    def setUp(self):
        self.checked_in_manifest = json.loads(monitor.MANIFEST.read_text())
        self.manifest = copy.deepcopy(self.checked_in_manifest)
        # Keep scenario baselines explicit instead of assuming every adapted
        # feature in the live manifest still points to the original review.
        for feature in self.manifest["features"]:
            feature["source_commit"] = self.manifest["reviewed_commit"]
            if feature.get("adapted_commit"):
                feature["adapted_commit"] = self.manifest["reviewed_commit"]

    def test_checked_in_manifest(self):
        monitor.validate_manifest(self.checked_in_manifest)

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

    def test_distinct_adapted_and_candidate_sources_are_both_reviewed(self):
        manifest = copy.deepcopy(self.manifest)
        manifest["features"][0].update(status="adapted", adapted_commit="a" * 40, validation=["test evidence"])
        manifest["features"][1].update(status="in_progress", source_commit="b" * 40, adapted_commit=None, validation=[])
        calls = []

        def compare(base, head):
            calls.append((base, head))
            return {"status": "ahead", "files": [{"filename": "frontend/src/api/modules/usage.ts"}]}

        report = monitor.build_report(manifest, manifest["reviewed_commit"], None, compare)
        self.assertEqual(calls, [("a" * 40, manifest["reviewed_commit"]), ("b" * 40, manifest["reviewed_commit"])])
        self.assertEqual(report["features"][0]["comparison_basis"], "adapted_source")
        self.assertEqual(report["features"][1]["comparison_basis"], "candidate_source_only")
        self.assertTrue(all(feature["source_delta"]["review_required"] for feature in report["features"][:2]))
        self.assertFalse(report["review"]["review_required"])
        self.assertIsNone(report["repository_reviewed_commit"])

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

    def test_whole_project_changes_do_not_mark_usage_features_for_adaptation(self):
        files = [
            {"filename": "backend/crates/gateway-core/src/admission/new_policy.rs", "status": "added"},
            {"filename": "docs/new-feature.md", "status": "modified"},
            {"filename": ".github/workflows/build.yml", "status": "modified"},
            {"filename": "tools/new-tool/Cargo.lock", "status": "added"},
        ]
        original = copy.deepcopy(self.manifest)
        release = {"tag_name": self.manifest["repository_tracking_release"]}
        report = monitor.build_report(self.manifest, "a" * 40, release,
                                      lambda *args: {"status": "ahead", "files": files, "total_commits": 4})
        self.assertEqual(report["monitor_scope"], "entire_repository")
        self.assertTrue(report["review_required"])
        self.assertEqual(report["review"]["changed_files"], files)
        self.assertFalse(report["feature_review"]["review_required"])
        self.assertTrue(all(not feature["source_delta"]["review_required"] for feature in report["features"]))
        self.assertEqual(self.manifest, original)

    def test_initial_repository_tracking_does_not_claim_a_full_review(self):
        report = monitor.build_report(self.manifest, self.manifest["repository_tracking_commit"],
                                      {"tag_name": self.manifest["repository_tracking_release"], "html_url": "https://example.test/release"},
                                      lambda *args: self.fail("No comparison needed"))
        self.assertIsNone(report["repository_reviewed_commit"])
        self.assertEqual(report["repository_comparison_basis"], "tracking_start_only")
        self.assertFalse(report["review_required"])
        self.assertIn("全仓跟踪起点（尚未登记全仓审查）", monitor.markdown(report))

    def test_repository_and_feature_review_baselines_remain_independent(self):
        manifest = copy.deepcopy(self.manifest)
        manifest["repository_reviewed_commit"] = "a" * 40
        manifest["repository_reviewed_release"] = "v3.11.0"
        report = monitor.build_report(manifest, "a" * 40, {"tag_name": "v3.11.0"},
                                      lambda *args: {"status": "ahead", "files": [{"filename": "frontend/src/api/modules/usage.ts"}]})
        self.assertEqual(report["repository_comparison_basis"], "reviewed_repository")
        self.assertFalse(report["review_required"])
        self.assertTrue(report["feature_review"]["review_required"])
        self.assertTrue(all(feature["source_delta"]["review_required"] for feature in report["features"]))
        self.assertEqual(report["reviewed_commit"], self.manifest["reviewed_commit"])

    def test_release_only_update_requires_review_and_is_visible(self):
        release = {"tag_name": "v3.11.0", "html_url": "https://example.test/releases/v3.11.0"}
        report = monitor.build_report(self.manifest, self.manifest["repository_tracking_commit"], release,
                                      lambda *args: self.fail("No commit comparison needed"))
        self.assertFalse(report["review"]["review_required"])
        self.assertTrue(report["release_changed_since_repository_baseline"])
        self.assertTrue(report["review_required"])
        summary = monitor.markdown(report)
        self.assertIn("v3.10.0 → v3.11.0（发生变化，需要评估）", summary)
        self.assertIn("全项目更新需要评估", summary)
        self.assertNotIn("均未发现新变化", summary)
        self.assertTrue(all(not feature["source_delta"]["review_required"] for feature in report["features"]))

    def test_commits_with_no_net_file_change_are_still_reported(self):
        comparison = {"status": "ahead", "files": [], "total_commits": 2}
        whole_project = monitor.evaluate_comparison(comparison)
        feature = monitor.evaluate_comparison(comparison, ["frontend/src/views/usage/**"])
        self.assertTrue(whole_project["review_required"])
        self.assertEqual(whole_project["commit_count"], 2)
        self.assertFalse(feature["review_required"])

    def test_monitor_scope_and_repository_baseline_are_validated(self):
        for patch in ({"monitor_scope": "usage_only"}, {"repository_tracking_commit": "short"},
                      {"repository_reviewed_commit": "short"}, {"repository_reviewed_release": "v3.11.0"}):
            manifest = copy.deepcopy(self.manifest)
            manifest.update(patch)
            with self.subTest(patch=patch), self.assertRaises(ValueError):
                monitor.validate_manifest(manifest)

    def test_workflow_runs_once_each_monday(self):
        workflow = (monitor.ROOT / ".github/workflows/codex-proxy-usage-monitor.yml").read_text()
        self.assertEqual(re.findall(r"cron:\s*'([^']+)'", workflow), ["43 2 * * 1"])
        self.assertIn("name: codex-proxy-project-monitor", workflow)
        self.assertIn("workflow_dispatch:", workflow)


if __name__ == "__main__":
    unittest.main()
