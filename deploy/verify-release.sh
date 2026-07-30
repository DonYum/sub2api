#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: deploy/verify-release.sh [--insecure] <binary> <base-url>

Release gate for a systemd Sub2API deployment. It verifies that the binary was
built with the embedded frontend and that the live service exposes both API and
SPA routes.

Options:
  --insecure  Pass --insecure to curl (for a private/self-signed endpoint).
EOF
}

insecure=false
if [[ "${1:-}" == "--insecure" ]]; then
  insecure=true
  shift
fi

if [[ $# -ne 2 ]]; then
  usage >&2
  exit 2
fi

binary=$1
base_url=${2%/}

if [[ ! -f "$binary" ]]; then
  echo "FAIL binary not found: $binary" >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "FAIL go is required to inspect binary build metadata" >&2
  exit 1
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "FAIL curl is required for live smoke tests" >&2
  exit 1
fi

build_info=$(go version -m "$binary")
if ! grep -Eq '^[[:space:]]*build[[:space:]]+-tags=([^,]+,)*embed(,.*)?$' <<<"$build_info"; then
  echo "FAIL binary build tags do not include embed: $binary" >&2
  exit 1
fi
echo "PASS binary build tags include embed: $binary"

if command -v sha256sum >/dev/null 2>&1; then
  binary_sha256=$(sha256sum "$binary" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  binary_sha256=$(shasum -a 256 "$binary" | awk '{print $1}')
else
  echo "FAIL sha256sum or shasum is required to identify the verified binary" >&2
  exit 1
fi
echo "PASS binary sha256: $binary_sha256"

curl_args=(
  --silent
  --show-error
  --location
  --connect-timeout 5
  --max-time 15
  --retry 2
  --retry-delay 1
  --retry-all-errors
)
if [[ "$insecure" == true ]]; then
  curl_args+=(--insecure)
fi

check_route() {
  local path=$1
  local expected_status=$2
  local expected_content_type=${3:-}
  local result status content_type

  result=$(curl "${curl_args[@]}" --output /dev/null \
    --write-out $'%{http_code}\t%{content_type}' "${base_url}${path}")
  IFS=$'\t' read -r status content_type <<<"$result"

  if [[ "$status" != "$expected_status" ]]; then
    echo "FAIL ${path}: expected HTTP ${expected_status}, got ${status}" >&2
    exit 1
  fi
  if [[ -n "$expected_content_type" && "$content_type" != "$expected_content_type"* ]]; then
    echo "FAIL ${path}: expected content-type ${expected_content_type}*, got ${content_type:-<empty>}" >&2
    exit 1
  fi

  echo "PASS ${path}: HTTP ${status} ${content_type:-<empty>}"
}

check_route "/health" "200" "application/json"
check_route "/v1/models" "401" "application/json"
check_route "/" "200" "text/html"
check_route "/keys" "200" "text/html"

echo "PASS release gate: $base_url"
