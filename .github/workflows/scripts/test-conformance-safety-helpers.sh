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

expect_output_status() {
  local name="$1"
  local expected_status="$2"
  local expected_output="$3"
  shift 3
  local actual_status
  local actual_output

  set +e
  actual_output="$("$@" 2>/dev/null)"
  actual_status=$?
  set -e

  if [[ "${actual_status}" -ne "${expected_status}" || "${actual_output}" != "${expected_output}" ]]; then
    echo "safety helper ${name}: got exit=${actual_status} output=[${actual_output}], want exit=${expected_status} output=[${expected_output}]" >&2
    failures=$((failures + 1))
  else
    echo "safety helper ${name}: ok"
  fi
}

# Every valid budget comparison returns a state with status 0. Invalid values
# fail closed, so callers cannot confuse "below" with a helper error.
expect_output_status budget-decimal-below 0 below "${helper}" budget-state 24.99 25
expect_output_status budget-decimal-equal 0 at-or-over "${helper}" budget-state 25 25
expect_output_status budget-decimal-above 0 at-or-over "${helper}" budget-state 25.01 25
expect_output_status budget-scientific-below 0 below "${helper}" budget-state 2.499e1 25
expect_output_status budget-scientific-equal 0 at-or-over "${helper}" budget-state 2.5e1 25
expect_output_status budget-scientific-above 0 at-or-over "${helper}" budget-state 2.501e1 25
expect_status budget-negative 64 "${helper}" budget-state -1 25
expect_status budget-scientific-negative 64 "${helper}" budget-state -1e1 25
expect_status budget-malformed 64 "${helper}" budget-state not-a-number 25

# DigitalOcean Droplet DELETE succeeds only on the documented 204 response.
expect_status delete-204 0 "${helper}" validate-do-delete-status 204
expect_status delete-200 66 "${helper}" validate-do-delete-status 200
expect_status delete-299 66 "${helper}" validate-do-delete-status 299
expect_status delete-300 66 "${helper}" validate-do-delete-status 300
expect_status delete-399 66 "${helper}" validate-do-delete-status 399
expect_status delete-400 66 "${helper}" validate-do-delete-status 400

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
