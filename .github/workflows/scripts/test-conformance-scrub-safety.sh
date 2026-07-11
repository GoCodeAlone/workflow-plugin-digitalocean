#!/usr/bin/env bash
# Literal workflow fragments intentionally use single quotes.
# shellcheck disable=SC2016
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
budget="${repo_root}/.github/workflows/conformance-budget-check.yml"
scrubber="${repo_root}/.github/workflows/conformance-leak-scrubber.yml"
failures=0

require_text() {
  local name="$1"
  local file="$2"
  local text="$3"
  if ! grep -Fq -- "${text}" "${file}"; then
    echo "scrub safety ${name}: missing ${text}" >&2
    failures=$((failures + 1))
  else
    echo "scrub safety ${name}: ok"
  fi
}

require_order() {
  local name="$1"
  local file="$2"
  local first="$3"
  local second="$4"
  local first_line
  local second_line
  first_line="$(grep -nF -- "${first}" "${file}" | head -n1 | cut -d: -f1 || true)"
  second_line="$(grep -nF -- "${second}" "${file}" | head -n1 | cut -d: -f1 || true)"
  if [[ -z "${first_line}" || -z "${second_line}" || "${first_line}" -ge "${second_line}" ]]; then
    echo "scrub safety ${name}: ${first} must precede ${second}" >&2
    failures=$((failures + 1))
  else
    echo "scrub safety ${name}: ok"
  fi
}

check_curl_timeouts() {
  local file="$1"
  local line
  while IFS= read -r line; do
    [[ "${line}" == *"curl "* ]] || continue
    if [[ "${line}" != *"curl --disable"* || "${line}" != *"--connect-timeout 10"* || "${line}" != *"--max-time 30"* ]]; then
      echo "scrub safety curl-timeout: unhardened line in ${file}: ${line}" >&2
      failures=$((failures + 1))
    fi
  done < "${file}"
}

check_curl_timeouts "${budget}"
check_curl_timeouts "${scrubber}"

require_text delete-status-capture "${scrubber}" 'delete_status="$(curl'
require_text delete-status-validation "${scrubber}" 'validate-do-delete-status "${delete_status}"'
require_order delete-before-count "${scrubber}" 'validate-do-delete-status "${delete_status}"' 'scrubbed="$((scrubbed + 1))"'

require_text output-trap "${scrubber}" "trap 'publish_outputs' EXIT"
require_text failure-output "${scrubber}" 'echo "failures=${failure_count}"'
require_text incident-always "${scrubber}" "always() && !cancelled()"
require_text incident-failures "${scrubber}" "steps.scrub.outputs.failures != '0'"
require_text final-failure-step "${scrubber}" "Fail after incomplete cleanup reporting"
require_text final-failure-exit "${scrubber}" "exit 1"
require_order incident-before-budget "${scrubber}" "File or update the cleanup incident" "Escalate spend at or above the hard cap"
require_order budget-before-final "${scrubber}" "Escalate spend at or above the hard cap" "Fail after incomplete cleanup reporting"

if [[ "${failures}" -ne 0 ]]; then
  exit 1
fi

echo "conformance scrub safety tests: ok"
