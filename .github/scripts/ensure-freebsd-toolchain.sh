#!/usr/bin/env bash
set -euo pipefail

: "${RUNNER_TEMP:?RUNNER_TEMP is required}"
: "${GITHUB_PATH:?GITHUB_PATH is required}"
tool_dir="${RUNNER_TEMP}/freebsd-tools"
mkdir -p "$tool_dir"
export PATH="${tool_dir}:${PATH}"
missing=()
for tool in clang ld.lld curl; do
  if command -v "$tool" >/dev/null 2>&1; then
    continue
  fi
  # Hosted images can include versioned LLVM binaries without their meta-packages.
  candidates=()
  for candidate in /usr/bin/"${tool}"-[0-9]*; do
    [[ -x "$candidate" ]] && candidates+=("$candidate")
  done
  if [[ ${#candidates[@]} -gt 0 ]]; then
    candidate="$(printf '%s\n' "${candidates[@]}" | sort -V | tail -n 1)"
    ln -sfn "$candidate" "${tool_dir}/${tool}"
  else
    package="$tool"
    [[ "$tool" == ld.lld ]] && package=lld
    missing+=("$package")
  fi
done
if [[ ${#missing[@]} -gt 0 ]]; then
  sudo apt-get -o Acquire::Retries=3 update
  sudo apt-get -o Acquire::Retries=3 install -y "${missing[@]}"
fi
for tool in clang ld.lld curl; do
  command -v "$tool"
  "$tool" --version | sed -n '1p'
done
printf '%s\n' "$tool_dir" >> "$GITHUB_PATH"
