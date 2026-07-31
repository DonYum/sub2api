#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: deploy/verify-release.sh [--insecure] --expected-sha <sha256> \
       [--unit-name <name>] [--unit-url <url>] [--public-url <url>] \
       [--metrics-url <url>] <installed-binary>

Release gate for a systemd Sub2API deployment. It verifies that the binary was
built with the embedded frontend and that the live service exposes both API and
SPA routes.

Options:
  --expected-sha  SHA-256 of the release artifact expected to be installed.
  --unit-name     systemd unit whose running executable must match the artifact
                  (default: sub2api.service).
  --unit-url      Direct URL of the systemd unit (for example, localhost).
  --public-url    Public URL served through the production ingress.
  --metrics-url   Optional Prometheus metrics endpoint to verify.
  --insecure      Pass --insecure to curl (for private/self-signed endpoints).
EOF
}

insecure=false
expected_sha=
unit_name=sub2api.service
unit_url=
public_url=
metrics_url=

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
    --unit-name)
      if [[ $# -lt 2 ]]; then
        echo "FAIL --unit-name requires a value" >&2
        exit 2
      fi
      unit_name=${2:-}
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
    --metrics-url)
      if [[ $# -lt 2 ]]; then
        echo "FAIL --metrics-url requires a value" >&2
        exit 2
      fi
      metrics_url=${2:-}
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
metrics_url=${metrics_url%/}

if [[ ! "$expected_sha" =~ ^[[:xdigit:]]{64}$ ]]; then
  echo "FAIL expected SHA-256 must be 64 hexadecimal characters" >&2
  exit 2
fi

if [[ ! -f "$binary" ]]; then
  echo "FAIL binary not found: $binary" >&2
  exit 1
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "FAIL curl is required for live smoke tests" >&2
  exit 1
fi

if ! command -v systemctl >/dev/null 2>&1; then
  echo "FAIL systemctl is required to identify the running service" >&2
  exit 1
fi

if command -v go >/dev/null 2>&1; then
  build_tags=$(go version -m "$binary" 2>/dev/null \
    | awk '$1 == "build" && $2 ~ /^-tags=/ { print $2 }' || true)
  build_info_source="go version -m"
else
  build_tags=$(LC_ALL=C grep -a -o 'build.-tags=[[:alnum:]_.,-]*' "$binary" \
    | sed 's/^build.//' || true)
  build_info_source="binary build info"
fi

if ! grep -Eq '^-tags=([^,]+,)*embed(,.*)?$' <<<"$build_tags"; then
  echo "FAIL binary build tags do not include embed: $binary" >&2
  exit 1
fi
echo "PASS binary build tags include embed (${build_info_source}): $binary"

sha256_file() {
  local path=$1

  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$path" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$path" | awk '{print $1}'
  else
    echo "FAIL sha256sum or shasum is required to identify the verified binary" >&2
    exit 1
  fi
}

binary_sha256=$(sha256_file "$binary")
if [[ "$binary_sha256" != "$expected_sha" ]]; then
  echo "FAIL installed binary sha256: expected $expected_sha, got $binary_sha256" >&2
  exit 1
fi
echo "PASS installed binary sha256 matches expected artifact: $binary_sha256"

main_pid=$(systemctl show --property=MainPID "$unit_name" 2>/dev/null \
  | sed -n 's/^MainPID=//p' || true)
if [[ ! "$main_pid" =~ ^[1-9][0-9]*$ ]]; then
  echo "FAIL systemd unit has no running MainPID: $unit_name" >&2
  exit 1
fi
running_exe=/proc/${main_pid}/exe
if [[ ! -r "$running_exe" ]]; then
  echo "FAIL running executable is not readable: $running_exe" >&2
  exit 1
fi
running_sha256=$(sha256_file "$running_exe")
if [[ "$running_sha256" != "$expected_sha" ]]; then
  echo "FAIL running binary sha256: expected $expected_sha, got $running_sha256" >&2
  exit 1
fi
echo "PASS running $unit_name PID $main_pid matches expected artifact: $running_sha256"

curl_args=(
  --silent
  --show-error
  --location
  --noproxy '*'
  --connect-timeout 5
  --max-time 15
)
if [[ "$insecure" == true ]]; then
  curl_args+=(--insecure)
fi

curl_retry() {
  local attempt=1
  local max_attempts=3
  local output rc

  while :; do
    rc=0
    output=$(curl "${curl_args[@]}" "$@") || rc=$?
    if [[ $rc -eq 0 ]]; then
      printf '%s' "$output"
      return 0
    fi
    if [[ $attempt -ge $max_attempts ]]; then
      return "$rc"
    fi
    attempt=$((attempt + 1))
    sleep 1
  done
}

check_route() {
  local surface=$1
  local base_url=$2
  local path=$3
  local expected_status=$4
  local expected_content_type=${5:-}
  local result status content_type

  result=$(curl_retry --output /dev/null \
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

check_metrics() {
  local result metadata body status content_type

  result=$(curl_retry \
    --write-out $'\n%{http_code}\t%{content_type}' "$metrics_url")
  metadata=${result##*$'\n'}
  body=${result%$'\n'*}
  IFS=$'\t' read -r status content_type <<<"$metadata"

  if [[ "$status" != "200" ]]; then
    echo "FAIL metrics: expected HTTP 200, got ${status:-<empty>}" >&2
    exit 1
  fi
  if [[ "$content_type" != "text/plain"* ]]; then
    echo "FAIL metrics: expected content-type text/plain*, got ${content_type:-<empty>}" >&2
    exit 1
  fi
  if ! grep -Eq '^go_goroutines[[:space:]]' <<<"$body"; then
    echo "FAIL metrics: go_goroutines is missing" >&2
    exit 1
  fi

  echo "PASS metrics: HTTP 200 text/plain with go_goroutines"
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

if [[ -n "$metrics_url" ]]; then
  check_metrics
else
  echo "SKIP metrics: --metrics-url was not provided"
fi

if [[ -n "$unit_url" && -n "$public_url" ]]; then
  echo "PASS full release gate: unit and public surfaces verified"
else
  echo "PASS partial release gate: one surface remains unverified"
fi
