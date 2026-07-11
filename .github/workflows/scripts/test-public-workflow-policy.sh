#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
checker="${repo_root}/.github/workflows/scripts/check-public-workflow-policy.sh"
fixtures="${repo_root}/.github/workflows/scripts/fixtures/public-workflow-policy"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

pass_allowlist="${tmp_dir}/pass-allowlist.json"
cat >"${pass_allowlist}" <<'JSON'
[
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/pass.yml",
    "secret": "RELEASES_TOKEN",
    "rationale": "Read-only access to private Go module and release metadata dependencies."
  },
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/pass.yml",
    "secret": "GITHUB_TOKEN",
    "rationale": "GitHub-provided token publishes release assets to this repository."
  }
]
JSON

"${checker}" \
  --allowlist "${pass_allowlist}" \
  "${fixtures}/pass.yml"

reject_allowlist="${tmp_dir}/reject-allowlist.json"
cat >"${reject_allowlist}" <<'JSON'
[
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/reject.yml",
    "secret": "DIGITALOCEAN_TOKEN",
    "rationale": "A rationale must never make a known cloud credential acceptable."
  },
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/reject.yml",
    "secret": "DEPLOY_AUTH",
    "rationale": "An alias must never hide provider authority from the policy."
  },
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/reject.yml",
    "secret": "STALE_TOKEN",
    "rationale": "This deliberately stale exception proves fail-closed validation."
  }
]
JSON

set +e
reject_output="$("${checker}" \
  --allowlist "${reject_allowlist}" \
  "${fixtures}/reject.yml" 2>&1)"
reject_status=$?
set -e

if [[ "${reject_status}" -eq 0 ]]; then
  echo "expected forbidden workflow fixture to fail policy" >&2
  exit 1
fi

for expected in \
  "self-hosted runner" \
  "id-token: write" \
  "known cloud secret DIGITALOCEAN_TOKEN" \
  "provider authority with secret DEPLOY_AUTH" \
  "executable provider CLI doctl" \
  "fixed provider API api.digitalocean.com" \
  "provider SDK marker with provider authority" \
  "integration tag with provider authority" \
  "named live test" \
  "manual provider-authority job" \
  "scheduled provider-authority job" \
  "secret UNREVIEWED_TOKEN is not allowlisted" \
  "stale allowlist entry STALE_TOKEN"; do
  if ! grep -Fq -- "${expected}" <<<"${reject_output}"; then
    echo "missing expected policy diagnostic: ${expected}" >&2
    printf '%s\n' "${reject_output}" >&2
    exit 1
  fi
done

empty_allowlist="${tmp_dir}/empty-allowlist.json"
printf '[]\n' >"${empty_allowlist}"

uses_allowlist="${tmp_dir}/uses-allowlist.json"
cat >"${uses_allowlist}" <<'JSON'
[
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/reject-provider-uses.yml",
    "secret": "DEPLOY_AUTH",
    "rationale": "An opaque alias must not hide authority granted to a provider action."
  }
]
JSON
set +e
uses_output="$("${checker}" \
  --allowlist "${uses_allowlist}" \
  "${fixtures}/reject-provider-uses.yml" 2>&1)"
uses_status=$?
set -e

if [[ "${uses_status}" -eq 0 ]]; then
  echo "expected provider action and reusable workflow references to fail policy" >&2
  exit 1
fi
for expected in \
  "forbidden provider action digitalocean/action-doctl@v2" \
  "forbidden provider reusable workflow digitalocean/platform/.github/workflows/live-deploy.yml@main" \
  "provider authority with secret DEPLOY_AUTH"; do
  if ! grep -Fq -- "${expected}" <<<"${uses_output}"; then
    echo "missing expected provider uses diagnostic: ${expected}" >&2
    printf '%s\n' "${uses_output}" >&2
    exit 1
  fi
done

secret_syntax_allowlist="${tmp_dir}/secret-syntax-allowlist.json"
cat >"${secret_syntax_allowlist}" <<'JSON'
[
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/reject-secret-syntax.yml",
    "secret": "DEPLOY_AUTH",
    "rationale": "A global opaque alias must remain visible to provider-authority analysis."
  },
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/reject-secret-syntax.yml",
    "secret": "DIGITALOCEAN_TOKEN",
    "rationale": "Known provider credentials remain forbidden regardless of syntax or rationale."
  }
]
JSON
set +e
secret_syntax_output="$("${checker}" \
  --allowlist "${secret_syntax_allowlist}" \
  "${fixtures}/reject-secret-syntax.yml" 2>&1)"
secret_syntax_status=$?
set -e

if [[ "${secret_syntax_status}" -eq 0 ]]; then
  echo "expected bracket and global secret references to fail policy" >&2
  exit 1
fi
for expected in \
  "workflow .github/workflows/scripts/fixtures/public-workflow-policy/reject-secret-syntax.yml references known cloud secret DIGITALOCEAN_TOKEN" \
  "provider authority with secret DEPLOY_AUTH" \
  "secret UNREVIEWED_TOKEN is not allowlisted"; do
  if ! grep -Fq -- "${expected}" <<<"${secret_syntax_output}"; then
    echo "missing expected secret syntax diagnostic: ${expected}" >&2
    printf '%s\n' "${secret_syntax_output}" >&2
    exit 1
  fi
done
if grep -Fq -- "stale allowlist entry" <<<"${secret_syntax_output}"; then
  echo "bracket secret references were not matched to their exact allowlist entries" >&2
  printf '%s\n' "${secret_syntax_output}" >&2
  exit 1
fi

set +e
runner_output="$("${checker}" \
  --allowlist "${empty_allowlist}" \
  "${fixtures}/reject-runners.yml" 2>&1)"
runner_status=$?
set -e

if [[ "${runner_status}" -eq 0 ]]; then
  echo "expected non-GitHub-hosted runner selectors to fail policy" >&2
  exit 1
fi
# The GitHub expression below is an intentionally literal expected diagnostic.
# shellcheck disable=SC2016
for expected in \
  "runner selector private-linux is not recognized as GitHub-hosted" \
  "runner selector linux is not recognized as GitHub-hosted" \
  'forbidden dynamic runner selector ${{ vars.RUNNER_LABEL }}'; do
  if ! grep -Fq -- "${expected}" <<<"${runner_output}"; then
    echo "missing expected runner policy diagnostic: ${expected}" >&2
    printf '%s\n' "${runner_output}" >&2
    exit 1
  fi
done

set +e
global_output="$("${checker}" \
  --allowlist "${empty_allowlist}" \
  "${fixtures}/reject-global.yml" 2>&1)"
global_status=$?
set -e

if [[ "${global_status}" -eq 0 ]]; then
  echo "expected global policy markers to fail even when job permissions override them" >&2
  exit 1
fi
for expected in \
  "id-token: write" \
  "known cloud credential variable AWS_ACCESS_KEY_ID"; do
  if ! grep -Fq -- "${expected}" <<<"${global_output}"; then
    echo "missing expected global policy diagnostic: ${expected}" >&2
    printf '%s\n' "${global_output}" >&2
    exit 1
  fi
done

echo "public workflow policy fixtures passed"
