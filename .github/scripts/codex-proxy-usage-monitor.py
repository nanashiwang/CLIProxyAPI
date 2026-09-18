#!/usr/bin/env python3
"""Read-only upstream review report; adaptation baselines are changed by reviewers."""

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


def evaluate_comparison(comparison, patterns):
    files = comparison.get("files")
    status = comparison.get("status")
    # GitHub compare returns at most 300 changed files. Never turn a truncated,
    # malformed, or rewritten history response into an 'up to date' result.
    complete = isinstance(files, list) and len(files) < 300 and status in ("ahead", "identical")
    matched = []
    for item in files or []:
        names = [item.get("filename", ""), item.get("previous_filename", "")]
        if any(name and fnmatch.fnmatchcase(name, pattern) for name in names for pattern in patterns):
            matched.append({key: item[key] for key in ("filename", "previous_filename", "status") if key in item})
    return {
        "comparison_status": status or "unknown",
        "comparison_complete": complete,
        "review_required": bool(matched) or not complete,
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
    review = checked(manifest["reviewed_commit"], all_patterns)
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
        "reviewed_commit": manifest["reviewed_commit"],
        "latest_release": release,
        "release_changed_since_review": bool(release and release["tag_name"] != manifest.get("reviewed_release")),
        "review": review,
        "features": features,
        "policy": "Read-only report. No merges, baseline updates, issue creation, comments or deployment.",
    }


def markdown(report):
    if report.get("error"):
        return "# codex-proxy-rs 用量跟踪\n\n检查失败，不能据此判断上游没有变化。详见 report.json。\n"
    repo = report["repository"]
    review = report["review"]
    lines = ["# codex-proxy-rs 用量跟踪", "", f"已审查基线：`{report['reviewed_commit']}`", f"当前 HEAD：`{report['head']}`", "",
             "相关上游变更需要审查。" if review["review_required"] else "已审查基线之后未发现所监控范围的变化。", "",
             "已审查不等于已适配；单项状态及适配来源只由验证后的人工记录决定。", "",
             "| 功能 | 本地状态 | 对比起点 | 来源变化 |", "| --- | --- | --- | --- |"]
    for feature in report["features"]:
        delta = feature["source_delta"]
        state = "需审查" if delta["review_required"] else "未发现变化"
        basis = "已适配来源" if feature["adapted_commit"] else "候选来源（尚未适配）"
        lines.append(f"| {feature['id']} | {feature['adaptation_status']} | {basis} | {state} |")
    if report["latest_release"]:
        lines += ["", f"最新正式 Release：{report['latest_release']['tag_name']}。Release 推进不会自动标记功能为已适配。"]
    if review["limitation"]:
        lines += ["", review["limitation"]]
    lines += ["", f"[完整提交差异](https://github.com/{repo}/compare/{report['reviewed_commit']}...{report['head']})", "", "关键路径变化（完整列表见 report.json）："]
    for item in review["changed_files"][:100]:
        # Upstream path text is data, not workflow instructions or HTML.
        name = item["filename"].replace("`", "'").replace("<", "&lt;").replace(">", "&gt;").replace("\n", " ")
        lines.append(f"- `{name}` ({item.get('status', 'unknown')})")
    if not review["changed_files"]:
        lines.append("- 未列出关键文件变化；比较不完整时仍需人工复核。")
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
