#!/usr/bin/env bash
# Literal workflow fragments intentionally use single quotes.
# shellcheck disable=SC2016
set -euo pipefail

repo_root="${CONFORMANCE_REPO_ROOT:-$(git rev-parse --show-toplevel)}"
cd "${repo_root}"

fail() {
  echo "conformance workflow structure: $*" >&2
  exit 1
}

must_exist() {
  [[ -f "$1" ]] || fail "missing $1"
}

must_contain() {
  local file="$1"
  local text="$2"
  grep -Fq -- "${text}" "${file}" || fail "${file} must contain: ${text}"
}

must_not_contain() {
  local file="$1"
  local text="$2"
  if grep -Fq -- "${text}" "${file}"; then
    fail "${file} must not contain: ${text}"
  fi
}

job_block() {
  local file="$1"
  local job="$2"
  awk -v anchor="  ${job}:" '
    $0 == anchor { found = 1; printing = 1; print; next }
    printing && $0 ~ /^  [a-zA-Z0-9_-]+:/ { exit }
    printing { print }
    END { if (!found) exit 3 }
  ' "${file}"
}

step_block() {
  local file="$1"
  local step="$2"
  awk -v anchor="      - name: ${step}" '
    $0 == anchor { found = 1; printing = 1; print; next }
    printing && $0 ~ /^      - / { exit }
    printing { print }
    END { if (!found) exit 3 }
  ' "${file}"
}

hard_cap_block() {
  local file="$1"
  awk '
    /^          if \[\[ .*HARD_STATE.*at-or-over.* \]\]; then$/ {
      found = 1
      printing = 1
    }
    printing { print }
    printing && $0 == "          fi" { exit }
    END { if (!found) exit 3 }
  ' "${file}"
}

block_must_contain() {
  local label="$1"
  local block="$2"
  local text="$3"
  grep -Fq -- "${text}" <<< "${block}" || fail "${label} must contain: ${text}"
}

block_must_not_contain() {
  local label="$1"
  local block="$2"
  local text="$3"
  if grep -Fq -- "${text}" <<< "${block}"; then
    fail "${label} must not contain: ${text}"
  fi
}

block_must_precede() {
  local label="$1"
  local block="$2"
  local first="$3"
  local second="$4"
  local first_line
  local second_line
  first_line="$(grep -nF -- "${first}" <<< "${block}" | head -n1 | cut -d: -f1 || true)"
  second_line="$(grep -nF -- "${second}" <<< "${block}" | head -n1 | cut -d: -f1 || true)"
  [[ -n "${first_line}" ]] || fail "${label} missing ordered fragment: ${first}"
  [[ -n "${second_line}" ]] || fail "${label} missing ordered fragment: ${second}"
  if [[ "${first_line}" -ge "${second_line}" ]]; then
    fail "${label} must place '${first}' before '${second}'"
  fi
}

assert_trusted_live_gate() {
  local label="$1"
  local block="$2"
  block_must_contain "${label}" "${block}" "github.event_name == 'workflow_dispatch'"
  block_must_contain "${label}" "${block}" "github.event_name == 'push'"
  block_must_contain "${label}" "${block}" "github.ref == 'refs/heads/main'"
  block_must_contain "${label}" "${block}" "github.repository == 'GoCodeAlone/workflow-plugin-digitalocean'"
}

smoke=.github/workflows/conformance-smoke.yml
budget=.github/workflows/conformance-budget-check.yml
scrubber=.github/workflows/conformance-leak-scrubber.yml
cleanup=.github/conformance/cleanup.yaml
helper=.github/workflows/scripts/file-or-comment-leak-issue.sh
safety_helper=.github/workflows/scripts/conformance-safety.sh
safety_test=.github/workflows/scripts/test-conformance-safety-helpers.sh
scrub_safety_test=.github/workflows/scripts/test-conformance-scrub-safety.sh
runbook=docs/conformance-runbook.md

for file in "${smoke}" "${budget}" "${scrubber}" "${cleanup}" "${helper}" "${safety_helper}" "${safety_test}" "${scrub_safety_test}" "${runbook}"; do
  must_exist "${file}"
done

# PRs get only credential-free validation on GitHub-hosted runners. The
# provider-mutating job is restricted to this repository's main push or an
# explicit manual dispatch and uses the self-hosted live-IaC pool.
must_contain "${smoke}" "pull_request:"
must_not_contain "${smoke}" "pull_request_target:"

workflow_preamble="$(awk '$0 == "jobs:" { exit } { print }' "${smoke}")"
block_must_not_contain "workflow preamble" "${workflow_preamble}" "DO_CONFORMANCE_API_TOKEN"
block_must_not_contain "workflow preamble" "${workflow_preamble}" "DIGITALOCEAN_TOKEN"
block_must_not_contain "workflow preamble" "${workflow_preamble}" "RELEASES_TOKEN"
block_must_not_contain "workflow preamble" "${workflow_preamble}" "git config"

credential_job="$(job_block "${smoke}" credential-free)" || fail "missing credential-free job"
block_must_contain "credential-free job" "${credential_job}" "runs-on: ubuntu-latest"
block_must_not_contain "credential-free job" "${credential_job}" "self-hosted"
block_must_not_contain "credential-free job" "${credential_job}" "DO_CONFORMANCE_API_TOKEN"
block_must_not_contain "credential-free job" "${credential_job}" "DIGITALOCEAN_TOKEN"
block_must_not_contain "credential-free job" "${credential_job}" "RELEASES_TOKEN"
block_must_not_contain "credential-free job" "${credential_job}" "git config"
block_must_not_contain "credential-free job" "${credential_job}" '${{ secrets.'
block_must_contain "credential-free job" "${credential_job}" "GOWORK=off go test -tags=conformance"

budget_call_job="$(job_block "${smoke}" budget-check)" || fail "missing budget-check call job"
assert_trusted_live_gate "budget-check call job" "${budget_call_job}"
block_must_contain "budget-check call job" "${budget_call_job}" "uses: ./.github/workflows/conformance-budget-check.yml"

live_job="$(job_block "${smoke}" live-smoke)" || fail "missing live-smoke job"
assert_trusted_live_gate "live-smoke job" "${live_job}"
block_must_contain "live-smoke job" "${live_job}" "runs-on: [self-hosted, linux]"
block_must_contain "live-smoke job" "${live_job}" "needs: [budget-check]"
block_must_not_contain "live-smoke job" "${live_job}" "RELEASES_TOKEN"
block_must_not_contain "live-smoke job" "${live_job}" "git config --global"

# W0 consumes a released Workflow CLI and the plugin's existing conformance
# entrypoint. It must not reach forward to Task 2 contracts or Workflow source.
must_contain "${smoke}" "WFCTL_VERSION: v0.85.4"
must_not_contain "${smoke}" "WFCTL_CONFORMANCE_REF"
must_not_contain "${smoke}" "repository: GoCodeAlone/workflow"

# Exercise the host-accepted <root>/<name>/<name> + plugin.json loader shape.
must_contain "${smoke}" 'PLUGIN_ROOT=".conformance/plugins"'
must_contain "${smoke}" 'PLUGIN_NAME="workflow-plugin-digitalocean"'
must_contain "${smoke}" 'PLUGIN_DIR="${PLUGIN_ROOT}/${PLUGIN_NAME}"'
must_contain "${smoke}" 'go build -o "${PLUGIN_DIR}/${PLUGIN_NAME}" ./cmd/plugin'
must_contain "${smoke}" 'cp plugin.json "${PLUGIN_DIR}/plugin.json"'
must_contain "${smoke}" 'wfctl infra apply'
must_contain "${smoke}" '--plugin-dir "${PLUGIN_ROOT}"'

# Trusted live work fails before mutation without a token or over budget, and
# every attempt reaches provider-local cleanup.
live_preflight="$(step_block "${smoke}" "Fail before mutation when the token is missing")" || fail "missing live token preflight"
block_must_contain "live token preflight" "${live_preflight}" 'test -n "${DO_CONFORMANCE_API_TOKEN:-}"'

live_create="$(step_block "${smoke}" "Create tagged smoke Droplet through wfctl")" || fail "missing live create step"
block_must_contain "live create step" "${live_create}" "wfctl infra apply"
block_must_contain "live create step" "${live_create}" ".github/conformance/cleanup.yaml"

live_cleanup="$(step_block "${smoke}" "Force-delete tagged resources")" || fail "missing live cleanup step"
block_must_contain "live cleanup step" "${live_cleanup}" "if: always()"
block_must_contain "live cleanup step" "${live_cleanup}" "wfctl infra cleanup"
block_must_contain "live cleanup step" "${live_cleanup}" ".github/conformance/cleanup.yaml"

budget_job="$(job_block "${budget}" check-budget)" || fail "missing check-budget job"
block_must_contain "check-budget job" "${budget_job}" "runs-on: [self-hosted, linux]"
budget_preflight="$(step_block "${budget}" "Fail before mutation when the token is missing")" || fail "missing budget token preflight"
block_must_contain "budget token preflight" "${budget_preflight}" 'test -n "${DO_CONFORMANCE_API_TOKEN:-}"'
must_contain "${budget}" 'month_to_date_usage'
must_contain "${budget}" 'BUDGET_HARD_CAP_USD: 25'
hard_cap_branch="$(hard_cap_block "${budget}")" || fail "missing hard-cap branch"
block_must_contain "hard-cap branch" "${hard_cap_branch}" 'exit 1'
budget_caps="$(step_block "${budget}" "Enforce budget caps")" || fail "missing budget cap step"
block_must_contain "budget cap step" "${budget_caps}" "conformance-safety.sh budget-state"
block_must_contain "budget cap step" "${budget_caps}" 'HARD_STATE="$(.github/workflows/scripts/conformance-safety.sh budget-state'
block_must_precede "budget cap step" "${budget_caps}" "conformance-safety.sh budget-state" 'if [[ "${HARD_STATE}" == "at-or-over" ]]'

# The scheduled owner-local scrubber is independently token-gated and uses
# only this repository's helper and cleanup prefix.
must_contain "${scrubber}" "schedule:"
must_contain "${scrubber}" "workflow_dispatch:"
must_contain "${scrubber}" 'wf-do-conformance-'
must_contain "${scrubber}" "${helper}"
must_contain "${budget}" "${helper}"
must_contain "${helper}" "${runbook}"

scrubber_job="$(job_block "${scrubber}" scrub)" || fail "missing scrub job"
block_must_contain "scrub job" "${scrubber_job}" "runs-on: [self-hosted, linux]"
scrubber_preflight="$(step_block "${scrubber}" "Fail when the scrubber token is missing")" || fail "missing scrubber token preflight"
block_must_contain "scrubber token preflight" "${scrubber_preflight}" 'test -n "${DO_CONFORMANCE_API_TOKEN:-}"'
scrubber_pages="$(step_block "${scrubber}" "List and delete expired tagged Droplets")" || fail "missing scrubber pagination step"
block_must_contain "scrubber pagination step" "${scrubber_pages}" "conformance-safety.sh"
block_must_contain "scrubber pagination step" "${scrubber_pages}" "validate-do-page-url"
block_must_contain "scrubber pagination step" "${scrubber_pages}" '"${seen_pages}"'
block_must_contain "scrubber pagination step" "${scrubber_pages}" "curl --disable"
block_must_contain "scrubber pagination step" "${scrubber_pages}" "--proto '=https'"
block_must_precede "scrubber pagination step" "${scrubber_pages}" \
  "validate-do-page-url" 'list_status="$(curl'

scrubber_budget="$(step_block "${scrubber}" "Escalate spend at or above the hard cap")" || fail "missing scrubber budget step"
block_must_contain "scrubber budget step" "${scrubber_budget}" "conformance-safety.sh budget-state"
block_must_contain "scrubber budget step" "${scrubber_budget}" "if: always() && !cancelled()"

cleanup_incident="$(step_block "${scrubber}" "File or update the cleanup incident")" || fail "missing cleanup incident step"
block_must_contain "cleanup incident step" "${cleanup_incident}" "always() && !cancelled()"
block_must_contain "cleanup incident step" "${cleanup_incident}" "steps.scrub.outcome == 'failure'"
block_must_contain "cleanup incident step" "${cleanup_incident}" "steps.scrub.outputs.failures != '0'"

cleanup_final="$(step_block "${scrubber}" "Fail after incomplete cleanup reporting")" || fail "missing final incomplete-cleanup step"
block_must_contain "final incomplete-cleanup step" "${cleanup_final}" "always() && !cancelled()"
block_must_contain "final incomplete-cleanup step" "${cleanup_final}" "steps.scrub.outcome == 'failure'"
block_must_contain "final incomplete-cleanup step" "${cleanup_final}" "steps.scrub.outputs.failures != '0'"
block_must_contain "final incomplete-cleanup step" "${cleanup_final}" "exit 1"
block_must_precede "scrub job" "${scrubber_job}" "File or update the cleanup incident" "Escalate spend at or above the hard cap"
block_must_precede "scrub job" "${scrubber_job}" "Escalate spend at or above the hard cap" "Fail after incomplete cleanup reporting"

# The apply and cleanup commands share one provider-local config; it creates a
# tagged smoke Droplet and supplies the provider loader for cleanup.
must_contain "${cleanup}" "provider: digitalocean"
must_contain "${cleanup}" 'token: ${DIGITALOCEAN_TOKEN}'
must_contain "${cleanup}" "type: infra.droplet"
must_contain "${cleanup}" '      - ${CONFORMANCE_TAG}'

# Main CI keeps the structure gate and released wfctl pin durable.
must_contain .github/workflows/ci.yml "WFCTL_CONFORMANCE_VERSION: v0.85.4"
must_not_contain .github/workflows/ci.yml "WFCTL_CONFORMANCE_REF"
must_contain .github/workflows/ci.yml "./.github/workflows/scripts/test-conformance-workflows.sh"
must_contain .github/workflows/ci.yml "./.github/workflows/scripts/test-conformance-workflow-mutations.sh"
must_contain .github/workflows/ci.yml "./.github/workflows/scripts/test-conformance-safety-helpers.sh"
must_contain .github/workflows/ci.yml "./.github/workflows/scripts/test-conformance-scrub-safety.sh"

echo "conformance workflow structure: ok"
