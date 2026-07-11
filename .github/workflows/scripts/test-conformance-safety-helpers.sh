#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
helper="${repo_root}/.github/workflows/scripts/conformance-safety.sh"
seen_file="$(mktemp)"
trap 'rm -f "${seen_file}"' EXIT
failures=0

expect_status() {
  local name="$1"
  local expected="$2"
  shift 2
  local actual

  set +e
  "$@" >/dev/null 2>&1
  actual=$?
  set -e

  if [[ "${actual}" -ne "${expected}" ]]; then
    echo "safety helper ${name}: got exit ${actual}, want ${expected}" >&2
    failures=$((failures + 1))
  else
    echo "safety helper ${name}: ok"
  fi
}

# Budget hard-stop semantics: below is allowed; equality and above abort.
expect_status budget-below 1 "${helper}" budget-at-or-over 24.99 25
expect_status budget-equal 0 "${helper}" budget-at-or-over 25 25
expect_status budget-above 0 "${helper}" budget-at-or-over 25.01 25

# Pagination permits only exact DigitalOcean HTTPS authority and never follows
# the same URL twice in one traversal.
first_page='https://api.digitalocean.com/v2/droplets?per_page=200'
expect_status page-valid 0 "${helper}" validate-do-page-url "${first_page}" "${seen_file}"
expect_status page-repeat 65 "${helper}" validate-do-page-url "${first_page}" "${seen_file}"
expect_status page-http 64 "${helper}" validate-do-page-url 'http://api.digitalocean.com/v2/droplets' "${seen_file}"
expect_status page-host-suffix 64 "${helper}" validate-do-page-url 'https://api.digitalocean.com.evil.example/v2/droplets' "${seen_file}"
expect_status page-userinfo 64 "${helper}" validate-do-page-url 'https://api.digitalocean.com@evil.example/v2/droplets' "${seen_file}"
expect_status page-port 64 "${helper}" validate-do-page-url 'https://api.digitalocean.com:443/v2/droplets' "${seen_file}"

if [[ "${failures}" -ne 0 ]]; then
  exit 1
fi

echo "conformance safety helper tests: ok"
