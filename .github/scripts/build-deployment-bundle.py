#!/usr/bin/env python3
"""Build a public deployment bundle from committed source and a pinned UI release."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import zipfile


def run(*args):
    return subprocess.check_output(args)


def build_bundle(tag, management_tag, output_dir):
    repo = "nanashiwang/Cli-Proxy-API-Management-Center"
    commit = run("git", "rev-parse", f"{tag}^{{commit}}").decode().strip()
    if commit != run("git", "rev-parse", "HEAD").decode().strip():
        raise ValueError("Bundle must be built at its release commit")
    base = run("git", "describe", "--tags", "--abbrev=0", f"{tag}^").decode().strip()
    release = json.loads(run("gh", "api", f"repos/{repo}/releases/tags/{management_tag}"))
    if release["draft"] or release["prerelease"]:
        raise ValueError("Management release must be published and stable")
    asset = next(a for a in release["assets"] if a["name"] == "management.html")
    output_dir.mkdir(parents=True, exist_ok=True)
    bundle = output_dir / f"cpa-deployment-{tag}.zip"
    contents = {}
    paths = run("git", "diff", "--name-only", "-z", base, tag).decode().split("\0")
    for path in filter(None, paths):
        parts = Path(path).parts
        if Path(path).name in {".env", "config.yaml", "config.yml"} or "auths" in parts or path.endswith((".key", ".pem", ".cds", ".state")):
            raise ValueError(f"Refusing to publish private/runtime path: {path}")
    contents["backend.patch"] = run("git", "diff", "--binary", base, tag)
    contents["release-workflow.yaml"] = run("git", "show", f"{tag}:.github/workflows/release.yaml")
    for path in run("git", "ls-tree", "-r", "--name-only", tag, "docs").decode().splitlines():
        contents[path] = run("git", "show", f"{tag}:{path}")
    for path in ("LICENSE", "LICENSE.codex-proxy-rs", "NOTICE.codex-proxy-rs"):
        contents[path] = run("git", "show", f"{tag}:{path}")
    with tempfile.TemporaryDirectory() as temp:
        run("gh", "release", "download", management_tag, "--repo", repo, "--pattern", "management.html", "--dir", temp)
        ui = (Path(temp) / "management.html").read_bytes()
    digest = "sha256:" + hashlib.sha256(ui).hexdigest()
    if digest != asset.get("digest"):
        raise ValueError("Management page digest differs from GitHub release metadata")
    contents["management.html"] = ui
    changes = run("git", "log", "--reverse", "--format=%B", f"{base}..{tag}").decode()
    contents["DEPLOY_CN.md"] = f"""# CPA {tag} 部署包

## 更新说明

{changes.strip()}

## 使用方式

- 后端源码基线：`{base}`；目标提交：`{commit}`。在干净基线检出上使用 `git apply --check backend.patch` 后再应用。
- 正式管理页面：`{management_tag}`，直接复用已发布的 `management.html`；本包已按 GitHub 提供的 SHA-256 校验。
- 二进制从本后端 Release 选择与平台对应的安装包，按 `checksums.txt` 校验。
- 备份现有二进制、实际配置、token、cooldown store 和租约状态后，按现有服务方式更新并重启。不要删除租约文件/锁文件。
- 构建环境和参数见 `build-parameters.json` 与 `release-workflow.yaml`；功能说明见 `docs/`。
- 本包不含实际运行配置、密钥、token 或数据库，也不会自动更新线上服务。
- 回滚时恢复上一版二进制及必要的备份；先排空活动流，避免跨版本并行占用同一账号。
""".encode()
    contents["build-parameters.json"] = json.dumps({
        "go_version": "1.26.4", "entrypoint": "./cmd/server", "version": tag.removeprefix("v"),
        "commit": commit, "ldflags": "-s -w -X main.Version=<version> -X main.Commit=<commit> -X main.BuildDate=<UTC>",
        "linux_plugin": "CGO_ENABLED=1; GLIBC 2.17 baseline", "linux_portable": "CGO_ENABLED=0",
        "freebsd_arm64": "CGO_ENABLED=0; no-plugin", "authoritative_recipe": "release-workflow.yaml",
    }, ensure_ascii=False, indent=2).encode()
    manifest = {"version": tag, "commit": commit, "base": base, "management_tag": management_tag,
                "management_url": release["html_url"], "files": {p: hashlib.sha256(data).hexdigest() for p, data in sorted(contents.items())}}
    contents["manifest.json"] = json.dumps(manifest, ensure_ascii=False, indent=2).encode()
    with zipfile.ZipFile(bundle, "w", zipfile.ZIP_DEFLATED) as archive:
        for path, data in sorted(contents.items()):
            archive.writestr(path, data)
    with zipfile.ZipFile(bundle) as archive:
        if archive.testzip() is not None:
            raise ValueError("Bundle zip integrity check failed")
    sha = hashlib.sha256(bundle.read_bytes()).hexdigest()
    bundle.with_suffix(".zip.sha256").write_text(f"{sha}  {bundle.name}\n")
    print(json.dumps({"bundle": str(bundle), "sha256": sha, "commit": commit, "management": management_tag}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--tag", required=True)
    parser.add_argument("--management-tag", required=True)
    parser.add_argument("--output-dir", required=True, type=Path)
    args = parser.parse_args()
    build_bundle(args.tag, args.management_tag, args.output_dir)
