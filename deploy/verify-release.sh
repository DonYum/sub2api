#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: deploy/verify-release.sh [--insecure] --expected-sha <sha256> \
       [--unit-url <url>] [--public-url <url>] <installed-binary>

Release gate for a systemd Sub2API deployment. It verifies that the binary was
built with the embedded frontend and that the live service exposes both API and
SPA routes.

Options:
  --expected-sha  SHA-256 of the release artifact expected to be installed.
  --unit-url      Direct URL of the systemd unit (for example, localhost).
  --public-url    Public URL served through the production ingress.
  --insecure      Pass --insecure to curl (for private/self-signed endpoints).
EOF
}

insecure=false
expected_sha=
unit_url=
public_url=

while [[ $# -gt 0 ]]; do
  case "$1" in
    --insecure)
      insecure=true
      shift
      ;;
    --expected-sha)
      if [[ $# -lt 2 ]]; then
        echo "FAIL --expected-sha requires a value" >&2
        exit 2
      fi
      expected_sha=${2:-}
      shift 2
      ;;
    --unit-url)
      if [[ $# -lt 2 ]]; then
        echo "FAIL --unit-url requires a value" >&2
        exit 2
      fi
      unit_url=${2:-}
      shift 2
      ;;
    --public-url)
      if [[ $# -lt 2 ]]; then
        echo "FAIL --public-url requires a value" >&2
        exit 2
      fi
      public_url=${2:-}
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    --*)
      echo "FAIL unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
    *)
      break
      ;;
  esac
done

if [[ $# -ne 1 || -z "$expected_sha" || ( -z "$unit_url" && -z "$public_url" ) ]]; then
  usage >&2
  exit 2
fi

binary=$1
expected_sha=$(printf '%s' "$expected_sha" | tr '[:upper:]' '[:lower:]')
unit_url=${unit_url%/}
public_url=${public_url%/}

if [[ ! "$expected_sha" =~ ^[[:xdigit:]]{64}$ ]]; then
  echo "FAIL expected SHA-256 must be 64 hexadecimal characters" >&2
  exit 2
fi

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
if [[ "$binary_sha256" != "$expected_sha" ]]; then
  echo "FAIL installed binary sha256: expected $expected_sha, got $binary_sha256" >&2
  exit 1
fi
echo "PASS installed binary sha256 matches expected artifact: $binary_sha256"

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
  local surface=$1
  local base_url=$2
  local path=$3
  local expected_status=$4
  local expected_content_type=${5:-}
  local result status content_type

  result=$(curl "${curl_args[@]}" --output /dev/null \
    --write-out $'%{http_code}\t%{content_type}' "${base_url}${path}")
  IFS=$'\t' read -r status content_type <<<"$result"

  if [[ "$status" != "$expected_status" ]]; then
    echo "FAIL ${surface} ${path}: expected HTTP ${expected_status}, got ${status}" >&2
    exit 1
  fi
  if [[ -n "$expected_content_type" && "$content_type" != "$expected_content_type"* ]]; then
    echo "FAIL ${surface} ${path}: expected content-type ${expected_content_type}*, got ${content_type:-<empty>}" >&2
    exit 1
  fi

  echo "PASS ${surface} ${path}: HTTP ${status} ${content_type:-<empty>}"
}

check_surface() {
  local surface=$1
  local base_url=$2

  check_route "$surface" "$base_url" "/health" "200" "application/json"
  check_route "$surface" "$base_url" "/v1/models" "401" "application/json"
  check_route "$surface" "$base_url" "/" "200" "text/html"
  check_route "$surface" "$base_url" "/keys" "200" "text/html"
}

if [[ -n "$unit_url" ]]; then
  check_surface "unit" "$unit_url"
else
  echo "SKIP unit surface: --unit-url was not provided"
fi

if [[ -n "$public_url" ]]; then
  check_surface "public" "$public_url"
else
  echo "SKIP public surface: --public-url was not provided"
fi

if [[ -n "$unit_url" && -n "$public_url" ]]; then
  echo "PASS full release gate: unit and public surfaces verified"
else
  echo "PASS partial release gate: one surface remains unverified"
fi
