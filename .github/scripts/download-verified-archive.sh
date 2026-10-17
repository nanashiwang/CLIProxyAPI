#!/usr/bin/env bash
# Download public build inputs; the caller pins the digest independently of transport.
set -euo pipefail

destination="${1:?destination is required}"
expected_sha256="${2:?SHA256 is required}"
shift 2
[[ "$expected_sha256" =~ ^[a-f0-9]{64}$ ]] || { echo 'Invalid pinned SHA256' >&2; exit 1; }
[[ "$#" -gt 0 ]] || { echo 'No archive sources supplied' >&2; exit 1; }
partial="${destination}.partial"
trap 'rm -f "$partial"' EXIT
rm -f "$destination"

for url in "$@"; do
  rm -f "$partial"
  printf 'Downloading pinned archive from %s\n' "$url"
  if curl --fail --location --show-error --silent \
    --proto '=https' --proto-redir '=https' \
    --connect-timeout 15 --max-time 180 --retry 2 --retry-max-time 240 \
    --output "$partial" "$url"; then
    actual="$(sha256sum "$partial")"
    if [[ "${actual%% *}" == "$expected_sha256" ]]; then
      printf 'Verified SHA256: %s\n' "$expected_sha256"
      mv "$partial" "$destination"
      exit 0
    fi
    printf 'Pinned SHA256 mismatch; rejecting %s\n' "$url" >&2
  else
    printf 'Archive download failed: %s\n' "$url" >&2
  fi
done
printf 'No source delivered the pinned archive\n' >&2
exit 1
