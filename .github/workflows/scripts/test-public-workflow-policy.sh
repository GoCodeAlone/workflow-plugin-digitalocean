#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
checker_binary="${repo_root}/.github/workflows/scripts/check-public-workflow-policy.sh"
fixtures="${repo_root}/.github/workflows/scripts/fixtures/public-workflow-policy"
tmp_dir="$(mktemp -d "${repo_root}/.workflow-policy-test.XXXXXX")"
trap 'rm -rf "${tmp_dir}"' EXIT
export TMPDIR="${tmp_dir}"
fixture_executables="${tmp_dir}/fixture-executables.json"
printf '[]\n' >"${fixture_executables}"
checker="${tmp_dir}/check-public-workflow-policy.sh"
cat >"${checker}" <<EOF
#!/usr/bin/env bash
exec "${checker_binary}" --executable-allowlist "${fixture_executables}" "\$@"
EOF
chmod +x "${checker}"

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
  "${fixtures}/pass.yml" \
  "${fixtures}/pass-negative-guard.yml"

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
executable_escape="${tmp_dir}/executable-escape.sh"
ln -s /dev/null "${executable_escape}"
executable_escape_rel="${executable_escape#"${repo_root}/"}"
invalid_executables="${tmp_dir}/invalid-executables.json"
cat >"${invalid_executables}" <<EOF
[
  {"path":"scripts/workflow-iac-host-conformance.sh","sha256":"0000000000000000000000000000000000000000000000000000000000000000","rationale":"Hash mismatch and stale-entry mutation fixture."},
  {"path":"../escape.sh","sha256":"0000000000000000000000000000000000000000000000000000000000000000","rationale":"Traversal mutation fixture."},
  {"path":"${executable_escape_rel}","sha256":"0000000000000000000000000000000000000000000000000000000000000000","rationale":"Symlink escape mutation fixture."}
]
EOF
set +e
executable_integrity_output="$("${checker}" \
  --allowlist "${empty_allowlist}" \
  --executable-allowlist "${invalid_executables}" \
  "${fixtures}/pass-negative-guard.yml" 2>&1)"
executable_integrity_status=$?
set -e
for expected in \
  "executable hash mismatch for scripts/workflow-iac-host-conformance.sh" \
  "stale executable allowlist entry scripts/workflow-iac-host-conformance.sh" \
  "executable allowlist path ../escape.sh escapes the repository" \
  "resolves outside repository"; do
  if [[ "${executable_integrity_status}" -eq 0 ]] || ! grep -Fq -- "${expected}" <<<"${executable_integrity_output}"; then
    echo "missing expected executable integrity diagnostic: ${expected}" >&2
    printf '%s\n' "${executable_integrity_output}" >&2
    exit 1
  fi
done

set +e
permissions_output="$(TMPDIR="${tmp_dir}" "${checker}" \
  --allowlist "${empty_allowlist}" \
  "${fixtures}/reject-permissions.yml" 2>&1)"
permissions_status=$?
set -e

if [[ "${permissions_status}" -eq 0 ]]; then
  echo "expected unsafe permission shapes to fail policy" >&2
  exit 1
fi
# The GitHub expression below is an intentionally literal expected diagnostic.
# shellcheck disable=SC2016
for expected in \
  "workflow .github/workflows/scripts/fixtures/public-workflow-policy/reject-permissions.yml uses forbidden permissions: write-all" \
  "job job-write-all uses forbidden permissions: write-all" \
  'job dynamic-permissions uses forbidden dynamic permissions selector ${{ vars.PERMISSIONS }}' \
  "job malformed-permissions uses unsupported permissions shape"; do
  if ! grep -Fq -- "${expected}" <<<"${permissions_output}"; then
    echo "missing expected permissions diagnostic: ${expected}" >&2
    printf '%s\n' "${permissions_output}" >&2
    exit 1
  fi
done

set +e
dynamic_secrets_output="$("${checker}" \
  --allowlist "${empty_allowlist}" \
  "${fixtures}/reject-dynamic-secrets.yml" 2>&1)"
dynamic_secrets_status=$?
set -e

if [[ "${dynamic_secrets_status}" -eq 0 ]]; then
  echo "expected dynamic credential selectors to fail policy" >&2
  exit 1
fi
for expected in \
  "dynamic secret selector secrets[vars.NAME]" \
  "dynamic secret selector secrets[format('{0}_TOKEN', vars.PROVIDER)]" \
  "dynamic secret selector secrets[vars.DIGITALOCEAN_TOKEN]" \
  "dynamic secret selector secrets.*" \
  "whole secrets context" \
  "known cloud credential variable reference DIGITALOCEAN_TOKEN"; do
  if ! grep -Fq -- "${expected}" <<<"${dynamic_secrets_output}"; then
    echo "missing expected dynamic credential diagnostic: ${expected}" >&2
    printf '%s\n' "${dynamic_secrets_output}" >&2
    exit 1
  fi
done

set +e
env_indirection_output="$("${checker}" \
  --allowlist "${empty_allowlist}" \
  "${fixtures}/reject-env-indirection.yml" 2>&1)"
env_indirection_status=$?
set -e

if [[ "${env_indirection_status}" -eq 0 ]]; then
  echo "expected provider environment and dynamic command indirection to fail policy" >&2
  exit 1
fi
for expected in \
  "environment value contains provider CLI doctl" \
  "environment value contains provider API api.digitalocean.com" \
  "environment value contains provider SDK marker" \
  "dynamic command execution" \
  "forbidden eval" \
  "forbidden shell -c"; do
  if ! grep -Fq -- "${expected}" <<<"${env_indirection_output}"; then
    echo "missing expected environment indirection diagnostic: ${expected}" >&2
    printf '%s\n' "${env_indirection_output}" >&2
    exit 1
  fi
done

set +e
script_execution_output="$("${checker}" \
  --allowlist "${empty_allowlist}" \
  "${fixtures}/reject-script-execution.yml" 2>&1)"
script_execution_status=$?
set -e

if [[ "${script_execution_status}" -eq 0 ]]; then
  echo "expected unreviewed committed script execution to fail policy" >&2
  exit 1
fi
for expected in \
  "unallowlisted executable script ./scripts/unreviewed.sh" \
  "workflow executable path ../escape.sh is outside repository"; do
  if ! grep -Fq -- "${expected}" <<<"${script_execution_output}"; then
    echo "missing expected executable script diagnostic: ${expected}" >&2
    printf '%s\n' "${script_execution_output}" >&2
    exit 1
  fi
done

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
  "unreviewed action digitalocean/action-doctl@v2" \
  "unrecognized reusable workflow digitalocean/platform/.github/workflows/live-deploy.yml@main" \
  "provider authority with secret DEPLOY_AUTH"; do
  if ! grep -Fq -- "${expected}" <<<"${uses_output}"; then
    echo "missing expected provider uses diagnostic: ${expected}" >&2
    printf '%s\n' "${uses_output}" >&2
    exit 1
  fi
done

set +e
unreviewed_uses_output="$("${checker}" \
  --allowlist "${empty_allowlist}" \
  "${fixtures}/reject-unreviewed-uses.yml" 2>&1)"
unreviewed_uses_status=$?
set -e

if [[ "${unreviewed_uses_status}" -eq 0 ]]; then
  echo "expected unreviewed action references to fail policy" >&2
  exit 1
fi
for expected in \
  "unreviewed action octocat/unknown-action@v1" \
  "local action ./.github/actions/not-reviewed is forbidden" \
  "Docker action docker://alpine:3.20 is forbidden" \
  "unreviewed action digitalocean/experimental-deploy@v1" \
  "unrecognized reusable workflow acme/platform/.github/workflows/deploy.yml@main"; do
  if ! grep -Fq -- "${expected}" <<<"${unreviewed_uses_output}"; then
    echo "missing expected unreviewed uses diagnostic: ${expected}" >&2
    printf '%s\n' "${unreviewed_uses_output}" >&2
    exit 1
  fi
done

set +e
guard_suffix_output="$("${checker}" \
  --allowlist "${empty_allowlist}" \
  "${fixtures}/reject-guard-suffix.yml" 2>&1)"
guard_suffix_status=$?
set -e

if [[ "${guard_suffix_status}" -eq 0 ]]; then
  echo "expected executable suffix after negative guard to fail policy" >&2
  exit 1
fi
if ! grep -Fq -- "executable provider CLI doctl" <<<"${guard_suffix_output}"; then
  echo "missing provider CLI diagnostic after negative guard suffix" >&2
  printf '%s\n' "${guard_suffix_output}" >&2
  exit 1
fi
if ! grep -Fq -- "job command-substitution executes forbidden provider authority: executable provider CLI doctl" <<<"${guard_suffix_output}"; then
  echo "command substitution inside a negative guard bypassed provider CLI detection" >&2
  printf '%s\n' "${guard_suffix_output}" >&2
  exit 1
fi

invalid_paths_allowlist="${tmp_dir}/invalid-paths-allowlist.json"
cat >"${invalid_paths_allowlist}" <<'JSON'
[
  {
    "path": "/tmp/absolute-workflow.yml",
    "secret": "PACKAGE_TOKEN",
    "rationale": "Absolute paths must never be accepted as workflow policy exceptions."
  },
  {
    "path": "../traversal-workflow.yml",
    "secret": "RELEASES_TOKEN",
    "rationale": "Parent traversal must never escape exact repository-relative matching."
  }
]
JSON
set +e
invalid_allowlist_output="$("${checker}" \
  --allowlist "${invalid_paths_allowlist}" \
  "${fixtures}/pass-negative-guard.yml" 2>&1)"
invalid_allowlist_status=$?
set -e

if [[ "${invalid_allowlist_status}" -eq 0 ]]; then
  echo "expected unconfined allowlist paths to fail policy" >&2
  exit 1
fi
for expected in \
  "allowlist path /tmp/absolute-workflow.yml must be repository-relative" \
  "allowlist path ../traversal-workflow.yml escapes the repository"; do
  if ! grep -Fq -- "${expected}" <<<"${invalid_allowlist_output}"; then
    echo "missing expected allowlist confinement diagnostic: ${expected}" >&2
    printf '%s\n' "${invalid_allowlist_output}" >&2
    exit 1
  fi
done

escape_workflow="${tmp_dir}/symlink-escape.yml"
ln -s /dev/null "${escape_workflow}"
set +e
outside_output="$("${checker}" \
  --allowlist "${empty_allowlist}" \
  /dev/null \
  "${escape_workflow}" 2>&1)"
outside_status=$?
set -e

if [[ "${outside_status}" -eq 0 ]]; then
  echo "expected outside and symlink-escaping workflow paths to fail policy" >&2
  exit 1
fi
for expected in \
  "workflow path /dev/null is outside repository" \
  "resolves outside repository"; do
  if ! grep -Fq -- "${expected}" <<<"${outside_output}"; then
    echo "missing expected workflow confinement diagnostic: ${expected}" >&2
    printf '%s\n' "${outside_output}" >&2
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
