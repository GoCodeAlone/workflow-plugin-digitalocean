#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
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

smoke=.github/workflows/conformance-smoke.yml
budget=.github/workflows/conformance-budget-check.yml
scrubber=.github/workflows/conformance-leak-scrubber.yml
cleanup=.github/conformance/cleanup.yaml
helper=.github/workflows/scripts/file-or-comment-leak-issue.sh
runbook=docs/conformance-runbook.md

for file in "${smoke}" "${budget}" "${scrubber}" "${cleanup}" "${helper}" "${runbook}"; do
  must_exist "${file}"
done

# PRs get only credential-free validation on GitHub-hosted runners. The
# provider-mutating job is restricted to this repository's main push or an
# explicit manual dispatch and uses the self-hosted live-IaC pool.
must_contain "${smoke}" "pull_request:"
must_contain "${smoke}" "runs-on: ubuntu-latest"
must_contain "${smoke}" "runs-on: [self-hosted, linux]"
must_contain "${smoke}" "github.event_name == 'workflow_dispatch'"
must_contain "${smoke}" "github.event_name == 'push'"
must_contain "${smoke}" "github.ref == 'refs/heads/main'"
must_contain "${smoke}" "github.repository == 'GoCodeAlone/workflow-plugin-digitalocean'"
must_not_contain "${smoke}" "pull_request_target:"

# W0 consumes a released Workflow CLI and the plugin's existing conformance
# entrypoint. It must not reach forward to Task 2 contracts or Workflow source.
must_contain "${smoke}" "WFCTL_VERSION: v0.85.4"
must_contain "${smoke}" "GOWORK=off go test -tags=conformance"
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
must_contain "${smoke}" 'test -n "${DO_CONFORMANCE_API_TOKEN:-}"'
must_contain "${smoke}" "needs: [budget-check]"
must_contain "${smoke}" "if: always()"
must_contain "${smoke}" "wfctl infra cleanup"
must_contain "${smoke}" ".github/conformance/cleanup.yaml"
must_contain "${budget}" "runs-on: [self-hosted, linux]"
must_contain "${budget}" 'test -n "${DO_CONFORMANCE_API_TOKEN:-}"'
must_contain "${budget}" 'month_to_date_usage'
must_contain "${budget}" 'BUDGET_HARD_CAP_USD: 25'
must_contain "${budget}" 'exit 1'

# The scheduled owner-local scrubber is independently token-gated and uses
# only this repository's helper and cleanup prefix.
must_contain "${scrubber}" "schedule:"
must_contain "${scrubber}" "workflow_dispatch:"
must_contain "${scrubber}" "runs-on: [self-hosted, linux]"
must_contain "${scrubber}" 'test -n "${DO_CONFORMANCE_API_TOKEN:-}"'
must_contain "${scrubber}" 'wf-do-conformance-'
must_contain "${scrubber}" "${helper}"
must_contain "${budget}" "${helper}"
must_contain "${helper}" "${runbook}"

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

echo "conformance workflow structure: ok"
