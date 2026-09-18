#!/usr/bin/env bash
set -euo pipefail

: "${RUNNER_TEMP:?RUNNER_TEMP is required}"
: "${TARGET:?TARGET is required}"
: "${TARGET_GOARCH:?TARGET_GOARCH is required}"
: "${RELEASE_VERSION:?RELEASE_VERSION is required}"
: "${COMMIT:?COMMIT is required}"
: "${BUILD_DATE:?BUILD_DATE is required}"

if [[ "$TARGET" != freebsd-amd64 || "$TARGET_GOARCH" != amd64 ]]; then
  printf 'Unsupported FreeBSD CGO target: %s (%s)\n' "$TARGET" "$TARGET_GOARCH" >&2
  exit 1
fi

# Keep the original FreeBSD 14.3 toolchain baseline after its release archive moved.
# SHA256 from https://archive.freebsd.org/old-releases/amd64/14.3-RELEASE/MANIFEST
base_url='https://archive.freebsd.org/old-releases/amd64/14.3-RELEASE/base.txz'
base_sha256='e38b5cf756d60086a6c2f736eff19cc7685f7e2313e31d14342fc8df57200a92'
work_dir="$(mktemp -d "${RUNNER_TEMP}/freebsd-cgo.XXXXXX")"
trap 'sudo rm -rf "$work_dir"' EXIT
sysroot_dir="${work_dir}/sysroot"
base_archive="${work_dir}/base.txz"

wget --https-only --tries=3 --timeout=60 -q -O "$base_archive" "$base_url"
printf '%s  %s\n' "$base_sha256" "$base_archive" | sha256sum --check
mkdir -p "$sysroot_dir" "dist/${TARGET}/bin"
sudo tar -xf "$base_archive" -C "$sysroot_dir"

CGO_ENABLED=1 GOOS=freebsd GOARCH="$TARGET_GOARCH" \
  CGO_LDFLAGS='-fuse-ld=lld' \
  CC="clang --target=x86_64-unknown-freebsd14.3 --sysroot=${sysroot_dir}" \
  go build \
    -ldflags="-s -w -X main.Version=${RELEASE_VERSION} -X main.Commit=${COMMIT} -X main.BuildDate=${BUILD_DATE}" \
    -o "dist/${TARGET}/bin/cli-proxy-api" ./cmd/server/
