#!/usr/bin/env python3
"""Write a release and verify ambiguous GitHub mutation responses."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

FIELDS = "apiUrl,tagName,targetCommitish,name,body,isDraft,isPrerelease"


def run_gh(*args):
    return subprocess.run(["gh", *args], capture_output=True, text=True)


def read_release(repo, tag):
    result = run_gh("release", "view", tag, "--repo", repo, "--json", FIELDS)
    if result.returncode:
        return None
    return json.loads(result.stdout)


def ensure_draft(repo, tag, commit, notes, publish=False, latest=None):
    existing = read_release(repo, tag)
    if existing and not existing["isDraft"] and not publish:
        raise RuntimeError("Refusing to turn an already published release into a draft")
    if publish:
        if not existing or existing["tagName"] != tag or existing["targetCommitish"] != commit:
            raise RuntimeError("Release source does not match the completed build")
        notes = existing["body"]
    payload = {
        "tag_name": tag,
        "target_commitish": commit,
        "name": tag,
        "body": notes,
        "draft": not publish,
        "prerelease": False,
    }
    if publish:
        payload["make_latest"] = latest
    endpoint = existing["apiUrl"] if existing else f"repos/{repo}/releases"
    method = "PATCH" if existing else "POST"
    with tempfile.TemporaryDirectory() as directory:
        path = Path(directory) / "release.json"
        path.write_text(json.dumps(payload), encoding="utf-8")
        result = run_gh("api", "--method", method, endpoint, "--input", str(path))
    # Some GitHub failures occur after the mutation. Read back the full state;
    # neither a successful exit code nor an existing draft alone is sufficient.
    actual = read_release(repo, tag)
    expected = {
        "tagName": tag,
        "targetCommitish": commit,
        "name": tag,
        "body": notes,
        "isDraft": not publish,
        "isPrerelease": False,
    }
    if actual is None or any(actual.get(key) != value for key, value in expected.items()):
        raise RuntimeError("Release draft readback does not match the requested tag, commit, notes and state")
    if result.returncode:
        print("GitHub write returned an error, but complete release readback matches", file=sys.stderr)
    print(f"Verified release state: {repo}@{tag} ({commit})")


if __name__ == "__main__":
    publish = sys.argv[1] == "--publish"
    ensure_draft(os.environ["GH_REPO"], os.environ["GITHUB_REF_NAME"],
                 os.environ["GITHUB_SHA"], None if publish else Path(sys.argv[1]).read_text(encoding="utf-8"),
                 publish=publish, latest=sys.argv[2] if publish else None)
