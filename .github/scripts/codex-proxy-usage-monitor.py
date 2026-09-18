#!/usr/bin/env python3
"""Weekly whole-project update report with independent feature adaptation tracking."""

import argparse
import fnmatch
import json
from pathlib import Path
import re
import subprocess
import sys
from datetime import datetime, timezone
from urllib.parse import quote


ROOT = Path(__file__).resolve().parents[2]
MANIFEST = ROOT / ".github/upstream/codex-proxy-usage.json"
SHA = re.compile(r"[0-9a-f]{40}\Z")


def validate_manifest(manifest):
    if manifest.get("schema_version") != 1:
        raise ValueError("Unsupported tracking manifest schema")
    if manifest.get("repository") != "zyycn/codex-proxy-rs":
        raise ValueError("Unexpected upstream repository")
    if manifest.get("monitor_scope") != "entire_repository":
        raise ValueError("The monitor must cover the entire repository")
    if not SHA.fullmatch(manifest.get("repository_tracking_commit", "")):
        raise ValueError("A full repository tracking start commit is required")
    repository_reviewed = manifest.get("repository_reviewed_commit")
    if repository_reviewed is not None and not SHA.fullmatch(repository_reviewed):
        raise ValueError("A repository review baseline must be a full commit SHA")
    if manifest.get("repository_reviewed_release") and not repository_reviewed:
        raise ValueError("A repository reviewed release requires a repository review baseline")
    if not SHA.fullmatch(manifest.get("reviewed_commit", "")):
        raise ValueError("A full reviewed commit SHA is required")
    ids = set()
    for feature in manifest["features"]:
        if not feature.get("id") or feature["id"] in ids:
            raise ValueError("Feature IDs must be unique and nonempty")
        ids.add(feature["id"])
        if not SHA.fullmatch(feature.get("source_commit", "")):
            raise ValueError("A full feature source commit SHA is required")
        adapted = feature.get("adapted_commit")
        if adapted is not None and not SHA.fullmatch(adapted):
            raise ValueError("An adapted commit must be a full upstream SHA")
        if feature["status"] not in ("in_progress", "adapted", "deferred", "rejected"):
            raise ValueError("Unsupported feature status")
        if feature["status"] == "adapted" and (not adapted or not feature.get("validation")):
            raise ValueError("Adapted features require a source baseline and validation evidence")
        if not feature.get("watch_paths"):
            raise ValueError("Each feature requires watched paths")


def github_api(path, optional=False):
    result = subprocess.run(
        ["gh", "api", path, "-H", "X-GitHub-Api-Version: 2022-11-28"],
        capture_output=True, text=True, timeout=45, check=False,
    )
    if result.returncode:
        if optional and "HTTP 404" in result.stderr:
            return None
        # Do not log authentication headers or upstream-controlled response bodies.
        status = re.search(r"HTTP [0-9]{3}", result.stderr)
        detail = status.group(0) if status else f"exit {result.returncode}"
        raise RuntimeError(f"GitHub API failed ({detail}) for {path}")
    return json.loads(result.stdout)


def evaluate_comparison(comparison, patterns=None):
    files = comparison.get("files")
    status = comparison.get("status")
    # GitHub compare returns at most 300 changed files. Never turn a truncated,
    # malformed, or rewritten history response into an 'up to date' result.
    complete = isinstance(files, list) and len(files) < 300 and status in ("ahead", "identical")
    matched = []
    for item in files or []:
        names = [item.get("filename", ""), item.get("previous_filename", "")]
        if patterns is None or any(name and fnmatch.fnmatchcase(name, pattern) for name in names for pattern in patterns):
            matched.append({key: item[key] for key in ("filename", "previous_filename", "status") if key in item})
    return {
        "comparison_status": status or "unknown",
        "comparison_complete": complete,
        # Whole-project tracking also detects commits with no net file diff,
        # such as a change and its later revert within the weekly window.
        "review_required": bool(matched) or not complete or (patterns is None and status == "ahead"),
        "commit_count": comparison.get("total_commits", 0 if status == "identical" else None),
        "changed_files": matched,
        "limitation": None if complete else "History changed, response incomplete, or GitHub's 300-file limit was reached; review the full diff manually.",
    }


def build_report(manifest, head, release, compare):
    cache = {}

    def checked(base, patterns):
        if base not in cache:
            cache[base] = ({"status": "identical", "files": []} if base == head else compare(base, head))
        return evaluate_comparison(cache[base], patterns)

    shared = manifest["shared_watch_paths"]
    all_patterns = shared + [pattern for feature in manifest["features"] for pattern in feature["watch_paths"]]
    repository_reviewed = manifest.get("repository_reviewed_commit")
    repository_base = repository_reviewed or manifest["repository_tracking_commit"]
    repository_release = manifest.get("repository_reviewed_release") or manifest.get("repository_tracking_release")
    release_changed = (release["tag_name"] if release else None) != repository_release
    review = checked(repository_base, None)
    feature_review = checked(manifest["reviewed_commit"], all_patterns)
    features = []
    for feature in manifest["features"]:
        adapted = feature.get("adapted_commit")
        base = adapted or feature["source_commit"]
        features.append({
            "id": feature["id"], "title": feature["title"],
            "adaptation_status": feature["status"],
            "source_commit": feature["source_commit"], "adapted_commit": adapted,
            "comparison_base": base,
            "comparison_basis": "adapted_source" if adapted else "candidate_source_only",
            "source_delta": checked(base, shared + feature["watch_paths"]),
        })
    return {
        "schema_version": 1,
        "checked_at": datetime.now(timezone.utc).isoformat(),
        "repository": manifest["repository"], "head": head,
        "monitor_scope": manifest["monitor_scope"],
        "repository_tracking_commit": manifest["repository_tracking_commit"],
        "repository_reviewed_commit": repository_reviewed,
        "repository_comparison_base": repository_base,
        "repository_comparison_basis": "reviewed_repository" if repository_reviewed else "tracking_start_only",
        "repository_release_baseline": repository_release,
        "reviewed_commit": manifest["reviewed_commit"],
        "latest_release": release,
        "release_changed_since_review": bool(release and release["tag_name"] != manifest.get("reviewed_release")),
        "release_changed_since_repository_baseline": release_changed,
        "review_required": review["review_required"] or release_changed,
        "review": review,
        "feature_review": feature_review,
        "features": features,
        "policy": "Weekly read-only whole-project report for AI review of source, fixes, features, dependencies, documentation, CI and formal releases. Feature adaptation is assessed independently. No automatic merges, baseline updates, issue creation, comments or deployment.",
    }


def markdown(report):
    if report.get("error"):
        return "# codex-proxy-rs 全项目更新跟踪\n\n检查失败，不能据此判断上游没有变化。详见 report.json。\n"
    repo = report["repository"]
    review = report["review"]
    basis = "全仓已审查基线" if report["repository_reviewed_commit"] else "全仓跟踪起点（尚未登记全仓审查）"
    lines = ["# codex-proxy-rs 全项目更新跟踪", "", f"{basis}：`{report['repository_comparison_base']}`", f"当前 HEAD：`{report['head']}`", "",
             "全项目更新需要评估。" if report["review_required"] else "全仓跟踪区间及正式 Release 均未发现新变化。", "",
             "检查覆盖源码、功能、修复、依赖、文档和 CI；每周一由 AI 评估是否适合迁移，不自动合并或部署。", "",
             f"以下五项统计功能的既有审查基线：`{report['reviewed_commit']}`。全仓变化不代表这些功能都需要移植；单项状态及适配来源只由验证后的审查记录决定。", "",
             "| 功能 | 本地状态 | 对比起点 | 来源变化 |", "| --- | --- | --- | --- |"]
    for feature in report["features"]:
        delta = feature["source_delta"]
        state = "需审查" if delta["review_required"] else "未发现变化"
        basis = "已适配来源" if feature["adapted_commit"] else "候选来源（尚未适配）"
        lines.append(f"| {feature['id']} | {feature['adaptation_status']} | {basis} | {state} |")
    latest = report["latest_release"]
    release_state = "发生变化，需要评估" if report["release_changed_since_repository_baseline"] else "未变化"
    lines += ["", f"正式 Release：{report['repository_release_baseline'] or '无'} → {latest['tag_name'] if latest else '无'}（{release_state}）。Release 推进不会自动标记功能为已适配。"]
    if latest:
        lines.append(f"[最新正式 Release]({latest['html_url']})")
    if review["limitation"]:
        lines += ["", review["limitation"]]
    lines += ["", f"[全仓提交差异](https://github.com/{repo}/compare/{report['repository_comparison_base']}...{report['head']})", "", "全仓文件变化（不按统计功能路径过滤，完整列表见 report.json）："]
    for item in review["changed_files"][:100]:
        # Upstream path text is data, not workflow instructions or HTML.
        name = item["filename"].replace("`", "'").replace("<", "&lt;").replace(">", "&gt;").replace("\n", " ")
        lines.append(f"- `{name}` ({item.get('status', 'unknown')})")
    if not review["changed_files"]:
        lines.append("- 未列出净文件变化；仍须检查提交及 Release 状态，比较不完整时需复核完整历史。")
    return "\n".join(lines) + "\n"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path, default=MANIFEST)
    parser.add_argument("--output-dir", type=Path, default=ROOT / "upstream-report")
    args = parser.parse_args()
    args.output_dir.mkdir(parents=True, exist_ok=True)
    exit_code = 0
    try:
        manifest = json.loads(args.manifest.read_text())
        validate_manifest(manifest)
        repo = manifest["repository"]
        metadata = github_api(f"repos/{repo}")
        branch = quote(metadata["default_branch"], safe="")
        head = github_api(f"repos/{repo}/commits/{branch}")["sha"]
        if not SHA.fullmatch(head):
            raise ValueError("GitHub returned an invalid HEAD SHA")
        latest = github_api(f"repos/{repo}/releases/latest", optional=True)
        release = {key: latest[key] for key in ("tag_name", "html_url", "published_at")} if latest else None
        report = build_report(manifest, head, release, lambda base, target: github_api(f"repos/{repo}/compare/{base}...{target}?per_page=1"))
    except Exception as error:
        report = {"schema_version": 1, "error": str(error), "review_required": True}
        exit_code = 1
    (args.output_dir / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    (args.output_dir / "summary.md").write_text(markdown(report))
    print(f"Review report written to {args.output_dir}")
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
