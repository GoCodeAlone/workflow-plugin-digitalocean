#!/usr/bin/env bash
# GitHub expression literals below are mutation data, never shell expansion.
# shellcheck disable=SC2016
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
checker_binary="${repo_root}/.github/workflows/scripts/check-public-workflow-policy.sh"
fixtures="${repo_root}/.github/workflows/scripts/fixtures/public-workflow-policy"
policytool="${repo_root}/.github/workflows/policytool"

if GOWORK=off go list -m all | grep -Eq '^mvdan\.cc/sh/v3 '; then
  echo "policy parser dependency leaked into the plugin module graph" >&2
  exit 1
fi
if [[ "$(cd "${policytool}" && GOWORK=off GOFLAGS=-mod=readonly go list -m -f '{{.Version}}' mvdan.cc/sh/v3)" != "v3.13.1" ]]; then
  echo "policy tool must pin mvdan.cc/sh/v3 v3.13.1" >&2
  exit 1
fi
(cd "${policytool}" && GOWORK=off GOFLAGS=-mod=readonly go test ./...)

governance_workflow="${repo_root}/.github/workflows/public-workflow-policy.yml"
protection_verifier="${repo_root}/.github/workflows/scripts/verify-public-workflow-branch-protection.sh"
grep -Fq -- "github.event.before" "${governance_workflow}"
grep -Fq -- "0d368a29ba572e050c62cba90ae56908abbd4156" "${governance_workflow}"
if grep -Fq -- "0d368a29ba572e050c62cba90ae56908abbd4155" "${governance_workflow}" || \
  grep -Fq -- "github.event.before != '0000000000000000000000000000000000000000'" "${governance_workflow}"; then
  echo "bootstrap workflow permits a non-exact prior SHA" >&2
  exit 1
fi
grep -Fq -- "branches: [main]" "${governance_workflow}"
grep -Fq -- 'required_check="Public Workflow Policy / policy"' "${protection_verifier}"
grep -Fq -- 'required_approving_review_count >= 1' "${protection_verifier}"
grep -Fq -- 'dismiss_stale_reviews' "${protection_verifier}"
grep -Fq -- 'bypass_pull_request_allowances.users' "${protection_verifier}"
grep -Fq -- 'conditions.ref_name.exclude' "${protection_verifier}"
grep -Fq -- 'required_status_checks.strict == true' "${protection_verifier}"
grep -Fq -- 'strict_required_status_checks_policy == true' "${protection_verifier}"
grep -Fq -- 'Workflow authority changes use three pull requests' "${repo_root}/docs/public-workflow-policy.md"

tmp_dir="$(mktemp -d "${repo_root}/.workflow-policy-test.XXXXXX")"
integrity_extra="${policytool}/extra_linux.go"
integrity_vendor="${policytool}/vendor"
integrity_symlink="${policytool}/extra-link.go"
mutated_workflow=""
mutation_backup="${tmp_dir}/workflow-backup.yml"
cleanup() {
  if [[ -n "${mutated_workflow}" && -f "${mutation_backup}" ]]; then
    cp "${mutation_backup}" "${mutated_workflow}"
  fi
  rm -rf "${tmp_dir}" "${integrity_extra}" "${integrity_vendor}" "${integrity_symlink}"
}
trap cleanup EXIT
export TMPDIR="${tmp_dir}"
fixture_executables="${tmp_dir}/fixture-executables.json"
printf '[]\n' >"${fixture_executables}"
fixture_commands="${tmp_dir}/fixture-commands.json"
printf '[]\n' >"${fixture_commands}"
fixture_actions="${tmp_dir}/fixture-actions.json"
printf '[]\n' >"${fixture_actions}"
empty_allowlist="${tmp_dir}/empty-allowlist.json"
printf '[]\n' >"${empty_allowlist}"

bootstrap_selector="${tmp_dir}/select-policy-root.sh"
awk '
  $0 == "      - name: Select exact trusted policy root" { in_step=1; next }
  in_step && $0 == "        run: |" { in_run=1; next }
  in_run && /^      - name:/ { exit }
  in_run { sub(/^          /, ""); print }
' "${governance_workflow}" >"${bootstrap_selector}"
bootstrap_root="${tmp_dir}/bootstrap"
mkdir -p "${bootstrap_root}/candidate/.github/workflows/scripts"
printf '#!/usr/bin/env bash\n' >"${bootstrap_root}/candidate/.github/workflows/scripts/check-public-workflow-policy.sh"
chmod +x "${bootstrap_root}/candidate/.github/workflows/scripts/check-public-workflow-policy.sh"
bootstrap_output="${tmp_dir}/bootstrap-output"
(cd "${bootstrap_root}" && BEFORE_SHA=0d368a29ba572e050c62cba90ae56908abbd4156 EVENT_NAME=push GITHUB_OUTPUT="${bootstrap_output}" bash "${bootstrap_selector}")
grep -Fxq -- 'root=candidate' "${bootstrap_output}"
for rejected_before in \
  0d368a29ba572e050c62cba90ae56908abbd4155 \
  0000000000000000000000000000000000000000 \
  ffffffffffffffffffffffffffffffffffffffff; do
  set +e
  (cd "${bootstrap_root}" && BEFORE_SHA="${rejected_before}" EVENT_NAME=push GITHUB_OUTPUT="${bootstrap_output}" bash "${bootstrap_selector}") >/dev/null 2>&1
  bootstrap_status=$?
  set -e
  if [[ "${bootstrap_status}" -eq 0 ]]; then
    echo "bootstrap accepted non-exact prior SHA ${rejected_before}" >&2
    exit 1
  fi
done
set +e
(cd "${bootstrap_root}" && BEFORE_SHA=0d368a29ba572e050c62cba90ae56908abbd4156 EVENT_NAME=pull_request_target GITHUB_OUTPUT="${bootstrap_output}" bash "${bootstrap_selector}") >/dev/null 2>&1
bootstrap_pr_status=$?
set -e
if [[ "${bootstrap_pr_status}" -eq 0 ]]; then
  echo "bootstrap accepted exact SHA outside the push event" >&2
  exit 1
fi
mkdir -p "${bootstrap_root}/trusted/.github/workflows/scripts"
printf '#!/usr/bin/env bash\n' >"${bootstrap_root}/trusted/.github/workflows/scripts/check-public-workflow-policy.sh"
chmod +x "${bootstrap_root}/trusted/.github/workflows/scripts/check-public-workflow-policy.sh"
: >"${bootstrap_output}"
(cd "${bootstrap_root}" && BEFORE_SHA=ffffffffffffffffffffffffffffffffffffffff EVENT_NAME=pull_request_target GITHUB_OUTPUT="${bootstrap_output}" bash "${bootstrap_selector}")
grep -Fxq -- 'root=trusted' "${bootstrap_output}"

classic_protection="${tmp_dir}/classic-protection.json"
ruleset_protection="${tmp_dir}/ruleset-protection.json"
invalid_protection="${tmp_dir}/invalid-protection.json"
repository_metadata="${tmp_dir}/repository-metadata.json"
printf '{}\n' >"${invalid_protection}"
printf '{"default_branch":"main"}\n' >"${repository_metadata}"
cat >"${classic_protection}" <<'JSON'
{
  "enforce_admins":{"enabled":true},
  "required_status_checks":{
    "strict":true,
    "contexts":["Public Workflow Policy / policy"],
    "checks":[{"context":"Public Workflow Policy / policy","app_id":15368}]
  },
  "required_pull_request_reviews":{
    "required_approving_review_count":1,
    "dismiss_stale_reviews":true,
    "bypass_pull_request_allowances":{"users":[],"teams":[],"apps":[]}
  },
  "restrictions":null,
  "allow_force_pushes":{"enabled":false},
  "allow_deletions":{"enabled":false}
}
JSON
cat >"${ruleset_protection}" <<'JSON'
{
  "enforcement":"active",
  "bypass_actors":[],
  "conditions":{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}},
  "rules":[
    {"type":"pull_request","parameters":{"required_approving_review_count":1,"dismiss_stale_reviews_on_push":true}},
    {"type":"required_status_checks","parameters":{"strict_required_status_checks_policy":true,"required_status_checks":[{"context":"Public Workflow Policy / policy","integration_id":15368}]}},
    {"type":"non_fast_forward"},
    {"type":"deletion"}
  ]
}
JSON
PUBLIC_WORKFLOW_PROTECTION_FIXTURE_MODE=1 PUBLIC_WORKFLOW_CLASSIC_JSON_FILE="${classic_protection}" PUBLIC_WORKFLOW_RULESET_JSON_FILE="${invalid_protection}" PUBLIC_WORKFLOW_REPOSITORY_JSON_FILE="${repository_metadata}" \
  "${protection_verifier}" example/repo main >/dev/null
PUBLIC_WORKFLOW_PROTECTION_FIXTURE_MODE=1 PUBLIC_WORKFLOW_CLASSIC_JSON_FILE="${invalid_protection}" PUBLIC_WORKFLOW_RULESET_JSON_FILE="${ruleset_protection}" PUBLIC_WORKFLOW_REPOSITORY_JSON_FILE="${repository_metadata}" \
  "${protection_verifier}" example/repo main >/dev/null
ruleset_explicit="${ruleset_protection}.explicit"
jq '.conditions.ref_name.include=["refs/heads/release"]' "${ruleset_protection}" >"${ruleset_explicit}"
PUBLIC_WORKFLOW_PROTECTION_FIXTURE_MODE=1 PUBLIC_WORKFLOW_CLASSIC_JSON_FILE="${invalid_protection}" PUBLIC_WORKFLOW_RULESET_JSON_FILE="${ruleset_explicit}" PUBLIC_WORKFLOW_REPOSITORY_JSON_FILE="${repository_metadata}" \
  "${protection_verifier}" example/repo release >/dev/null

assert_protection_rejected() {
  local kind="$1"
  local fixture="$2"
  local branch="${3:-main}"
  local classic_file="${invalid_protection}"
  local ruleset_file="${invalid_protection}"
  if [[ "${kind}" == classic ]]; then
    classic_file="${fixture}"
  else
    ruleset_file="${fixture}"
  fi
  set +e
  PUBLIC_WORKFLOW_PROTECTION_FIXTURE_MODE=1 PUBLIC_WORKFLOW_CLASSIC_JSON_FILE="${classic_file}" PUBLIC_WORKFLOW_RULESET_JSON_FILE="${ruleset_file}" PUBLIC_WORKFLOW_REPOSITORY_JSON_FILE="${repository_metadata}" \
    "${protection_verifier}" example/repo "${branch}" >/dev/null 2>&1
  local status=$?
  set -e
  if [[ "${status}" -eq 0 ]]; then
    echo "${kind} protection accepted an invalid producer/freshness fixture: ${fixture}" >&2
    exit 1
  fi
}
jq 'del(.required_status_checks.strict)' "${classic_protection}" >"${classic_protection}.missing"
jq '.required_status_checks.strict=false' "${classic_protection}" >"${classic_protection}.false"
assert_protection_rejected classic "${classic_protection}.missing"
assert_protection_rejected classic "${classic_protection}.false"
jq 'del(.required_status_checks.checks[0].app_id)' "${classic_protection}" >"${classic_protection}.producer-missing"
jq '.required_status_checks.checks[0].app_id=99999' "${classic_protection}" >"${classic_protection}.producer-wrong"
jq '.required_status_checks.checks[0].app_id=null' "${classic_protection}" >"${classic_protection}.producer-null"
jq 'del(.required_status_checks.checks)' "${classic_protection}" >"${classic_protection}.legacy-context-only"
assert_protection_rejected classic "${classic_protection}.producer-missing"
assert_protection_rejected classic "${classic_protection}.producer-wrong"
assert_protection_rejected classic "${classic_protection}.producer-null"
assert_protection_rejected classic "${classic_protection}.legacy-context-only"
jq 'del(.rules[1].parameters.strict_required_status_checks_policy)' "${ruleset_protection}" >"${ruleset_protection}.missing"
jq '.rules[1].parameters.strict_required_status_checks_policy=false' "${ruleset_protection}" >"${ruleset_protection}.false"
assert_protection_rejected ruleset "${ruleset_protection}.missing"
assert_protection_rejected ruleset "${ruleset_protection}.false"
jq 'del(.rules[1].parameters.required_status_checks[0].integration_id)' "${ruleset_protection}" >"${ruleset_protection}.producer-missing"
jq '.rules[1].parameters.required_status_checks[0].integration_id=99999' "${ruleset_protection}" >"${ruleset_protection}.producer-wrong"
jq '.rules[1].parameters.required_status_checks[0].integration_id=null' "${ruleset_protection}" >"${ruleset_protection}.producer-null"
jq '.rules[1].parameters.required_status_checks=[] | .rules[1].parameters.contexts=["Public Workflow Policy / policy"]' "${ruleset_protection}" >"${ruleset_protection}.legacy-context-only"
assert_protection_rejected ruleset "${ruleset_protection}.producer-missing"
assert_protection_rejected ruleset "${ruleset_protection}.producer-wrong"
assert_protection_rejected ruleset "${ruleset_protection}.producer-null"
assert_protection_rejected ruleset "${ruleset_protection}.legacy-context-only"
jq '.rules |= map(select(.type != "non_fast_forward"))' "${ruleset_protection}" >"${ruleset_protection}.missing-non-fast-forward"
jq '.rules |= map(select(.type != "deletion"))' "${ruleset_protection}" >"${ruleset_protection}.missing-deletion"
assert_protection_rejected ruleset "${ruleset_protection}.missing-non-fast-forward"
assert_protection_rejected ruleset "${ruleset_protection}.missing-deletion"
assert_protection_rejected ruleset "${ruleset_protection}" release

lifecycle_root="${tmp_dir}/lifecycle"
mkdir -p "${lifecycle_root}/.github/workflows"
mkdir -p "${lifecycle_root}/scripts"
lifecycle_workflow="${lifecycle_root}/.github/workflows/lifecycle.yml"
lifecycle_transition="${tmp_dir}/lifecycle-transition.json"
lifecycle_presence="${tmp_dir}/lifecycle-presence.json"
cat >"${lifecycle_presence}" <<'JSON'
[
  {"path":".github/workflows/lifecycle.yml","contextSHA256":"462dc1ee56aa917ca0ca80bce78a0ec4744efa7b805480c09617df432ada0c61","state":"active","presence":"present"},
  {"path":".github/workflows/lifecycle.yml","contextSHA256":"69c5280913b6dbe34e7f59977b889b60411da8308dd01e6e1c3997391a44581d","state":"staged","presence":"present"}
]
JSON
cat >"${lifecycle_transition}" <<'JSON'
[
  {"path":".github/workflows/lifecycle.yml","command":"echo","statementSHA256":"819b561be4b01d042acf9c152963504db679c1f35863be463a27d0b1f829fce2","contextSHA256":"462dc1ee56aa917ca0ca80bce78a0ec4744efa7b805480c09617df432ada0c61","state":"active","rationale":"Current lifecycle context."},
  {"path":".github/workflows/lifecycle.yml","command":"lifecycle.sh","statementSHA256":"dba5e9682987ecf0db39babf5824d3bdea717b091db48e154c082bced59f6b79","contextSHA256":"462dc1ee56aa917ca0ca80bce78a0ec4744efa7b805480c09617df432ada0c61","state":"active","rationale":"Current lifecycle executable."},
  {"path":".github/workflows/lifecycle.yml","command":"echo","statementSHA256":"fe696343d9c54236742da9a5f73af7180c94578dca254d9099440c71775da76a","contextSHA256":"69c5280913b6dbe34e7f59977b889b60411da8308dd01e6e1c3997391a44581d","state":"staged","rationale":"Future lifecycle context."},
  {"path":".github/workflows/lifecycle.yml","command":"lifecycle.sh","statementSHA256":"dba5e9682987ecf0db39babf5824d3bdea717b091db48e154c082bced59f6b79","contextSHA256":"69c5280913b6dbe34e7f59977b889b60411da8308dd01e6e1c3997391a44581d","state":"staged","rationale":"Future lifecycle executable."}
]
JSON
lifecycle_executables="${tmp_dir}/lifecycle-executables.json"
cat >"${lifecycle_executables}" <<'JSON'
[
  {"path":"scripts/lifecycle.sh","workflowPath":".github/workflows/lifecycle.yml","contextSHA256":"462dc1ee56aa917ca0ca80bce78a0ec4744efa7b805480c09617df432ada0c61","state":"active","sha256":"2e1f5a51dcffcd76df111e383338b6d7e68d8b01ab088636ea87758a05b0e084","rationale":"Current script hash."},
  {"path":"scripts/lifecycle.sh","workflowPath":".github/workflows/lifecycle.yml","contextSHA256":"69c5280913b6dbe34e7f59977b889b60411da8308dd01e6e1c3997391a44581d","state":"staged","sha256":"cacb7804eaa7158147c9216632414e3ec06c3bd463d7a72f2a9aa7b6b06290e0","rationale":"Future script hash."}
]
JSON
printf '#!/usr/bin/env bash\necho old\n' >"${lifecycle_root}/scripts/lifecycle.sh"
cp "${fixtures}/lifecycle-old.yml" "${lifecycle_workflow}"
"${checker_binary}" --scan-root "${lifecycle_root}" --presence-allowlist "${lifecycle_presence}" --allowlist "${empty_allowlist}" --executable-allowlist "${lifecycle_executables}" --command-allowlist "${lifecycle_transition}" --action-allowlist "${empty_allowlist}"
printf '#!/usr/bin/env bash\necho future\n' >"${lifecycle_root}/scripts/lifecycle.sh"
cp "${fixtures}/lifecycle-future.yml" "${lifecycle_workflow}"
"${checker_binary}" --scan-root "${lifecycle_root}" --presence-allowlist "${lifecycle_presence}" --allowlist "${empty_allowlist}" --executable-allowlist "${lifecycle_executables}" --command-allowlist "${lifecycle_transition}" --action-allowlist "${empty_allowlist}"
lifecycle_cleanup="${tmp_dir}/lifecycle-cleanup.json"
jq 'map(select(.state=="staged") | .state="active")' "${lifecycle_transition}" >"${lifecycle_cleanup}"
jq '[.[1] | .state="active"]' "${lifecycle_presence}" >"${lifecycle_presence}.cleanup"
jq '[.[1] | .state="active"]' "${lifecycle_executables}" >"${lifecycle_executables}.cleanup"
"${checker_binary}" --scan-root "${lifecycle_root}" --presence-allowlist "${lifecycle_presence}.cleanup" --allowlist "${empty_allowlist}" --executable-allowlist "${lifecycle_executables}.cleanup" --command-allowlist "${lifecycle_cleanup}" --action-allowlist "${empty_allowlist}"

# Workflow additions and deletions use the same trusted three-phase lifecycle.
# An absent tombstone authorizes zero matching workflow files without weakening
# the default failure for an undeclared empty workflow set.
lifecycle_add_presence="${tmp_dir}/lifecycle-add-presence.json"
cat >"${lifecycle_add_presence}" <<'JSON'
[
  {"path":".github/workflows/lifecycle.yml","state":"active","presence":"absent"},
  {"path":".github/workflows/lifecycle.yml","contextSHA256":"69c5280913b6dbe34e7f59977b889b60411da8308dd01e6e1c3997391a44581d","state":"staged","presence":"present"}
]
JSON
jq '[.[] | select(.state=="staged")]' "${lifecycle_transition}" >"${lifecycle_transition}.add"
jq '[.[] | select(.state=="staged")]' "${lifecycle_executables}" >"${lifecycle_executables}.add"
rm -f "${lifecycle_workflow}" "${lifecycle_root}/scripts/lifecycle.sh"
"${checker_binary}" --scan-root "${lifecycle_root}" --presence-allowlist "${lifecycle_add_presence}" --allowlist "${empty_allowlist}" --executable-allowlist "${lifecycle_executables}.add" --command-allowlist "${lifecycle_transition}.add" --action-allowlist "${empty_allowlist}"
printf '#!/usr/bin/env bash\necho future\n' >"${lifecycle_root}/scripts/lifecycle.sh"
cp "${fixtures}/lifecycle-future.yml" "${lifecycle_workflow}"
"${checker_binary}" --scan-root "${lifecycle_root}" --presence-allowlist "${lifecycle_add_presence}" --allowlist "${empty_allowlist}" --executable-allowlist "${lifecycle_executables}.add" --command-allowlist "${lifecycle_transition}.add" --action-allowlist "${empty_allowlist}"
jq '[.[1] | .state="active"]' "${lifecycle_add_presence}" >"${lifecycle_add_presence}.cleanup"
jq 'map(.state="active")' "${lifecycle_transition}.add" >"${lifecycle_transition}.add-cleanup"
jq 'map(.state="active")' "${lifecycle_executables}.add" >"${lifecycle_executables}.add-cleanup"
"${checker_binary}" --scan-root "${lifecycle_root}" --presence-allowlist "${lifecycle_add_presence}.cleanup" --allowlist "${empty_allowlist}" --executable-allowlist "${lifecycle_executables}.add-cleanup" --command-allowlist "${lifecycle_transition}.add-cleanup" --action-allowlist "${empty_allowlist}"

lifecycle_delete_presence="${tmp_dir}/lifecycle-delete-presence.json"
cat >"${lifecycle_delete_presence}" <<'JSON'
[
  {"path":".github/workflows/lifecycle.yml","contextSHA256":"69c5280913b6dbe34e7f59977b889b60411da8308dd01e6e1c3997391a44581d","state":"active","presence":"present"},
  {"path":".github/workflows/lifecycle.yml","state":"staged","presence":"absent"}
]
JSON
"${checker_binary}" --scan-root "${lifecycle_root}" --presence-allowlist "${lifecycle_delete_presence}" --allowlist "${empty_allowlist}" --executable-allowlist "${lifecycle_executables}.add-cleanup" --command-allowlist "${lifecycle_transition}.add-cleanup" --action-allowlist "${empty_allowlist}"
rm -f "${lifecycle_workflow}" "${lifecycle_root}/scripts/lifecycle.sh"
"${checker_binary}" --scan-root "${lifecycle_root}" --presence-allowlist "${lifecycle_delete_presence}" --allowlist "${empty_allowlist}" --executable-allowlist "${lifecycle_executables}.add-cleanup" --command-allowlist "${lifecycle_transition}.add-cleanup" --action-allowlist "${empty_allowlist}"
jq '[.[1] | .state="active"]' "${lifecycle_delete_presence}" >"${lifecycle_delete_presence}.cleanup"
"${checker_binary}" --scan-root "${lifecycle_root}" --presence-allowlist "${lifecycle_delete_presence}.cleanup" --allowlist "${empty_allowlist}" --executable-allowlist "${empty_allowlist}" --command-allowlist "${empty_allowlist}" --action-allowlist "${empty_allowlist}"

set +e
undeclared_empty_output="$("${checker_binary}" --scan-root "${lifecycle_root}" --presence-allowlist "${empty_allowlist}" --allowlist "${empty_allowlist}" --executable-allowlist "${empty_allowlist}" --command-allowlist "${empty_allowlist}" --action-allowlist "${empty_allowlist}" 2>&1)"
undeclared_empty_status=$?
set -e
if [[ "${undeclared_empty_status}" -eq 0 ]] || ! grep -Fq -- "no public workflow files found and no trusted absence is declared" <<<"${undeclared_empty_output}"; then
  echo "empty workflow set passed without a trusted absence declaration" >&2
  printf '%s\n' "${undeclared_empty_output}" >&2
  exit 1
fi
invalid_tombstone="${tmp_dir}/invalid-tombstone.json"
printf '[{"path":"../../outside.yml","state":"active","presence":"absent"}]\n' >"${invalid_tombstone}"
set +e
invalid_tombstone_output="$("${checker_binary}" --scan-root "${lifecycle_root}" --presence-allowlist "${invalid_tombstone}" --allowlist "${empty_allowlist}" --executable-allowlist "${empty_allowlist}" --command-allowlist "${empty_allowlist}" --action-allowlist "${empty_allowlist}" 2>&1)"
invalid_tombstone_status=$?
set -e
if [[ "${invalid_tombstone_status}" -eq 0 ]] || ! grep -Fq -- "invalid trust group for ../../outside.yml" <<<"${invalid_tombstone_output}"; then
  echo "malformed tombstone authorized an empty workflow inventory" >&2
  printf '%s\n' "${invalid_tombstone_output}" >&2
  exit 1
fi

assert_lifecycle_invalid() {
  local manifest="$1"
  local expected="$2"
  set +e
  local output
  output="$("${checker_binary}" --scan-root "${lifecycle_root}" --presence-allowlist "${manifest}" --allowlist "${empty_allowlist}" --executable-allowlist "${lifecycle_executables}" --command-allowlist "${lifecycle_transition}" --action-allowlist "${empty_allowlist}" 2>&1)"
  local status=$?
  set -e
  if [[ "${status}" -eq 0 ]] || ! grep -Fq -- "${expected}" <<<"${output}"; then
    echo "invalid lifecycle manifest was accepted: ${expected}" >&2
    printf '%s\n' "${output}" >&2
    exit 1
  fi
}
lifecycle_invalid="${tmp_dir}/lifecycle-invalid.json"
# Restore the future fixture used by the invalid-manifest cases below.
printf '#!/usr/bin/env bash\necho future\n' >"${lifecycle_root}/scripts/lifecycle.sh"
cp "${fixtures}/lifecycle-future.yml" "${lifecycle_workflow}"
jq '.[0].state="pending" | [.[0]]' "${lifecycle_presence}" >"${lifecycle_invalid}"
assert_lifecycle_invalid "${lifecycle_invalid}" "invalid trust group"
jq '.[0].contextSHA256="cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" | [.[0]]' "${lifecycle_presence}" >"${lifecycle_invalid}"
assert_lifecycle_invalid "${lifecycle_invalid}" "no trust group matches workflow"
jq '.[0] as $active | .[1] as $staged | [$active, $staged, ($staged | .contextSHA256="cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")]' "${lifecycle_presence}" >"${lifecycle_invalid}"
assert_lifecycle_invalid "${lifecycle_invalid}" "multiple staged trust groups"
jq '.[0] as $active | [$active, ($active | .state="staged")]' "${lifecycle_presence}" >"${lifecycle_invalid}"
assert_lifecycle_invalid "${lifecycle_invalid}" "mixed trust group state"
checker="${tmp_dir}/check-public-workflow-policy.sh"
cat >"${checker}" <<EOF
#!/usr/bin/env bash
exec "${checker_binary}" --presence-allowlist "${empty_allowlist}" --executable-allowlist "${fixture_executables}" --command-allowlist "${fixture_commands}" --action-allowlist "${fixture_actions}" "\$@"
EOF
chmod +x "${checker}"

printf 'package policytool\n' >"${integrity_extra}"
set +e
extra_go_output="$("${checker_binary}" 2>&1)"
extra_go_status=$?
set -e
rm -f "${integrity_extra}"
if [[ "${extra_go_status}" -eq 0 ]] || ! grep -Fq -- \
  "unexpected policytool path: extra_linux.go" <<<"${extra_go_output}"; then
  echo "extra Go source bypassed policytool integrity" >&2
  printf '%s\n' "${extra_go_output}" >&2
  exit 1
fi

mkdir -p "${integrity_vendor}/mvdan.cc/sh/v3/syntax"
printf 'package syntax\n' >"${integrity_vendor}/mvdan.cc/sh/v3/syntax/override.go"
set +e
vendor_output="$("${checker_binary}" 2>&1)"
vendor_status=$?
set -e
rm -rf "${integrity_vendor}"
if [[ "${vendor_status}" -eq 0 ]] || ! grep -Fq -- \
  "unexpected policytool path: vendor" <<<"${vendor_output}"; then
  echo "vendor override bypassed policytool integrity" >&2
  printf '%s\n' "${vendor_output}" >&2
  exit 1
fi

ln -s main.go "${integrity_symlink}"
set +e
symlink_output="$("${checker_binary}" 2>&1)"
symlink_status=$?
set -e
rm -f "${integrity_symlink}"
if [[ "${symlink_status}" -eq 0 ]] || ! grep -Fq -- \
  "unexpected policytool path: extra-link.go" <<<"${symlink_output}"; then
  echo "policytool symlink bypassed integrity" >&2
  printf '%s\n' "${symlink_output}" >&2
  exit 1
fi

if ! grep -Fq -- 'exec env GOWORK=off GOFLAGS=-mod=readonly go run ./main.go' "${checker_binary}"; then
  echo "policytool wrapper does not execute the fixed source file" >&2
  exit 1
fi
if ! grep -Fq -- 'env GOWORK=off GOFLAGS=-mod=readonly go mod download' "${checker_binary}"; then
  echo "policytool wrapper does not prepare dependencies read-only" >&2
  exit 1
fi
if grep -Fq -- '-mod=mod' "${checker_binary}"; then
  echo "policytool wrapper permits module mutation" >&2
  exit 1
fi
policytool_hashes_before="$(git hash-object \
  "${policytool}/main.go" \
  "${policytool}/main_test.go" \
  "${policytool}/go.mod" \
  "${policytool}/go.sum" | tr '\n' ' ')"
"${checker_binary}"
policytool_hashes_after="$(git hash-object \
  "${policytool}/main.go" \
  "${policytool}/main_test.go" \
  "${policytool}/go.mod" \
  "${policytool}/go.sum" | tr '\n' ' ')"
if [[ "${policytool_hashes_before}" != "${policytool_hashes_after}" ]]; then
  echo "read-only policytool preparation changed trusted files" >&2
  exit 1
fi

trailing_secret="${tmp_dir}/trailing-secret.json"
trailing_executable="${tmp_dir}/trailing-executable.json"
trailing_command="${tmp_dir}/trailing-command.json"
trailing_action="${tmp_dir}/trailing-action.json"
trailing_presence="${tmp_dir}/trailing-presence.json"
printf '[] {}\n' >"${trailing_secret}"
printf '[] garbage\n' >"${trailing_executable}"
printf '[] {}\n' >"${trailing_command}"
printf '[] garbage\n' >"${trailing_action}"
printf '[] {}\n' >"${trailing_presence}"
for trust_input in secret executable command action presence; do
  args=()
  case "${trust_input}" in
    secret) args=(--allowlist "${trailing_secret}") ;;
    executable) args=(--executable-allowlist "${trailing_executable}") ;;
    command) args=(--command-allowlist "${trailing_command}") ;;
    action) args=(--action-allowlist "${trailing_action}") ;;
    presence) args=(--presence-allowlist "${trailing_presence}") ;;
  esac
  set +e
  trailing_output="$("${checker_binary}" "${args[@]}" 2>&1)"
  trailing_status=$?
  set -e
  if [[ "${trailing_status}" -eq 0 ]] || ! grep -Fq -- "unexpected trailing JSON" <<<"${trailing_output}"; then
    echo "${trust_input} allowlist accepted trailing JSON" >&2
    printf '%s\n' "${trailing_output}" >&2
    exit 1
  fi
done

candidate_root="${tmp_dir}/candidate"
mkdir -p "${candidate_root}/scripts"
cp -R "${repo_root}/.github" "${candidate_root}/.github"
cp "${repo_root}/scripts/workflow-iac-host-conformance.sh" "${candidate_root}/scripts/"

# Candidate trust manifests and analyzer sources are untrusted data. Replacing
# them must not affect the trusted base wrapper's decision.
for manifest in \
  public-workflow-secret-allowlist.json \
  public-workflow-executable-allowlist.json \
  public-workflow-command-allowlist.json \
  public-workflow-action-allowlist.json \
  public-workflow-presence-allowlist.json; do
  printf '[]\n' >"${candidate_root}/.github/${manifest}"
done
printf 'this is not Go source\n' >"${candidate_root}/.github/workflows/policytool/main.go"
printf 'not a module\n' >"${candidate_root}/.github/workflows/policytool/go.mod"
"${checker_binary}" --scan-root "${candidate_root}"

candidate_checker="${candidate_root}/.github/workflows/scripts/check-public-workflow-policy.sh"
printf '#!/usr/bin/env bash\nexit 0\n' >"${candidate_checker}"
set +e
candidate_checker_output="$("${checker_binary}" --scan-root "${candidate_root}" 2>&1)"
candidate_checker_status=$?
set -e
if [[ "${candidate_checker_status}" -eq 0 ]] || ! grep -Fq -- \
  "executable hash mismatch for .github/workflows/scripts/check-public-workflow-policy.sh" <<<"${candidate_checker_output}"; then
  echo "candidate CI checker mutation was validated against the trusted copy" >&2
  printf '%s\n' "${candidate_checker_output}" >&2
  exit 1
fi
cp "${repo_root}/.github/workflows/scripts/check-public-workflow-policy.sh" "${candidate_checker}"

candidate_policy_test="${candidate_root}/.github/workflows/scripts/test-public-workflow-policy.sh"
printf '#!/usr/bin/env bash\nexit 0\n' >"${candidate_policy_test}"
set +e
candidate_test_output="$("${checker_binary}" --scan-root "${candidate_root}" 2>&1)"
candidate_test_status=$?
set -e
if [[ "${candidate_test_status}" -eq 0 ]] || ! grep -Fq -- \
  "executable hash mismatch for .github/workflows/scripts/test-public-workflow-policy.sh" <<<"${candidate_test_output}"; then
  echo "candidate CI policy-test mutation was validated against the trusted copy" >&2
  printf '%s\n' "${candidate_test_output}" >&2
  exit 1
fi
cp "${repo_root}/.github/workflows/scripts/test-public-workflow-policy.sh" "${candidate_policy_test}"

cat >"${candidate_root}/.github/workflows/candidate-live.yml" <<'YAML'
name: Candidate live cloud workflow
on:
  pull_request:
defaults:
  run:
    shell: bash
jobs:
  live:
    runs-on: ubuntu-latest
    steps:
      - run: doctl compute droplet list
YAML
set +e
candidate_live_output="$("${checker_binary}" --scan-root "${candidate_root}" 2>&1)"
candidate_live_status=$?
set -e
if [[ "${candidate_live_status}" -eq 0 ]] || ! grep -Fq -- \
  "executable provider CLI doctl" <<<"${candidate_live_output}"; then
  echo "same-PR policy and live workflow weakening bypassed trusted base enforcement" >&2
  printf '%s\n' "${candidate_live_output}" >&2
  exit 1
fi
rm "${candidate_root}/.github/workflows/candidate-live.yml"

cat >"${candidate_root}/.github/workflows/candidate-service.yml" <<'YAML'
name: Candidate service execution
on: pull_request_target
permissions:
  contents: write
defaults:
  run:
    shell: bash
jobs:
  service:
    runs-on: ubuntu-latest
    services:
      attacker:
        image: ghcr.io/example/attacker:latest
    steps:
      - run: '(( 1 ))'
YAML
set +e
candidate_service_output="$("${checker_binary}" --scan-root "${candidate_root}" 2>&1)"
candidate_service_status=$?
set -e
if [[ "${candidate_service_status}" -eq 0 ]] || ! grep -Fq -- \
  "declares forbidden job services" <<<"${candidate_service_output}"; then
  echo "candidate service workflow bypassed trusted base enforcement" >&2
  printf '%s\n' "${candidate_service_output}" >&2
  exit 1
fi
rm "${candidate_root}/.github/workflows/candidate-service.yml"

cat >"${candidate_root}/.github/workflows/candidate-arithmetic.yml" <<'YAML'
name: Candidate arithmetic execution
on: pull_request_target
permissions:
  contents: write
defaults:
  run:
    shell: bash
jobs:
  arithmetic:
    runs-on: ubuntu-latest
    env:
      X: ${{ github.event.pull_request.title }}
    steps:
      - run: '(( X ))'
YAML
set +e
candidate_arithmetic_output="$("${checker_binary}" --scan-root "${candidate_root}" 2>&1)"
candidate_arithmetic_status=$?
set -e
if [[ "${candidate_arithmetic_status}" -eq 0 ]] || ! grep -Fq -- \
  "executes unreviewed exact shell statement" <<<"${candidate_arithmetic_output}"; then
  echo "candidate arithmetic workflow bypassed trusted base enforcement" >&2
  printf '%s\n' "${candidate_arithmetic_output}" >&2
  exit 1
fi
rm "${candidate_root}/.github/workflows/candidate-arithmetic.yml"

for inherited_shape in scalar mapping; do
  candidate_inherit="${candidate_root}/.github/workflows/candidate-inherit.yml"
  if [[ "${inherited_shape}" == scalar ]]; then
    inherited_yaml='    secrets: inherit'
    inherited_expected='inherits all job secrets'
  else
    inherited_yaml=$'    secrets:\n      token: inherit'
    inherited_expected='maps inherited secret token'
  fi
  cat >"${candidate_inherit}" <<YAML
name: Candidate inherited secrets
on: pull_request_target
jobs:
  inherited:
    uses: acme/platform/.github/workflows/reuse.yml@0123456789012345678901234567890123456789
${inherited_yaml}
YAML
  set +e
  candidate_inherit_output="$("${checker_binary}" --scan-root "${candidate_root}" 2>&1)"
  candidate_inherit_status=$?
  set -e
  if [[ "${candidate_inherit_status}" -eq 0 ]] || ! grep -Fq -- "${inherited_expected}" <<<"${candidate_inherit_output}"; then
    echo "candidate ${inherited_shape} inherited-secret shape bypassed policy" >&2
    printf '%s\n' "${candidate_inherit_output}" >&2
    exit 1
  fi
  rm "${candidate_inherit}"
done

candidate_workflow_call="${candidate_root}/.github/workflows/candidate-workflow-call.yml"
cat >"${candidate_workflow_call}" <<'YAML'
name: Candidate reusable environment secret
on:
  workflow_call:
jobs:
  deploy:
    runs-on: ubuntu-latest
    environment: production
    env:
      DEPLOY_TOKEN: ${{ secrets.RELEASES_TOKEN }}
    steps:
      - run: echo reusable
YAML
set +e
candidate_workflow_call_output="$("${checker_binary}" --scan-root "${candidate_root}" 2>&1)"
candidate_workflow_call_status=$?
set -e
if [[ "${candidate_workflow_call_status}" -eq 0 ]] || ! grep -Fq -- \
  "public workflow references forbidden repository secret RELEASES_TOKEN" <<<"${candidate_workflow_call_output}"; then
  echo "workflow_call environment secret bypassed public workflow policy" >&2
  printf '%s\n' "${candidate_workflow_call_output}" >&2
  exit 1
fi
rm "${candidate_workflow_call}"

assert_exact_mutation_rejected() {
  local label="$1"
  local workflow="$2"
  local sed_expression="$3"
  local expected="$4"
  mutated_workflow="${workflow}"
  cp "${workflow}" "${mutation_backup}"
  sed -i.bak -e "${sed_expression}" "${workflow}"
  rm -f "${workflow}.bak"
  set +e
  local output
  output="$("${checker_binary}" 2>&1)"
  local status=$?
  set -e
  cp "${mutation_backup}" "${workflow}"
  mutated_workflow=""
  if [[ "${status}" -eq 0 ]] || ! grep -Fq -- "${expected}" <<<"${output}"; then
    echo "exact workflow mutation was accepted: ${label}" >&2
    printf '%s\n' "${output}" >&2
    exit 1
  fi
}

if rg -n 'secrets\.|RELEASES_TOKEN|GOPRIVATE|x-access-token' \
  "${repo_root}/.github/workflows/ci.yml" \
  "${repo_root}/.github/workflows/iac-host-conformance.yml" \
  "${repo_root}/.github/workflows/grpc-version-sync.yml"; then
  echo "pull_request-capable workflow retains repository-secret authority" >&2
  exit 1
fi
assert_exact_mutation_rejected \
  "wfctl trailing target" \
  "${repo_root}/.github/workflows/ci.yml" \
  's/--strict-contracts/--strict-contracts ./g' \
  "unreviewed exact statement containing wfctl"
assert_exact_mutation_rejected \
  "GitHub release arguments" \
  "${repo_root}/.github/workflows/release.yml" \
  's/--draft=false/--draft=true/' \
  "unreviewed exact statement containing gh"
assert_exact_mutation_rejected \
  "sed target" \
  "${repo_root}/.github/workflows/ci.yml" \
  's/\$tmp_dir\/plugin.json/\$tmp_dir\/plugin.contracts.json/g' \
  "unreviewed exact statement containing sed"
assert_exact_mutation_rejected \
  "dynamic trailing expression" \
  "${repo_root}/.github/workflows/ci.yml" \
  's|GOWORK=off go vet ./...|GOWORK=off go vet ./... "${{ vars.EXTRA_TARGET }}"|' \
  "unreviewed exact statement containing go"
assert_exact_mutation_rejected \
  "workflow inherited safe environment" \
  "${repo_root}/.github/workflows/ci.yml" \
  's/WFCTL_VALIDATE_VERSION: v0.51.2/WFCTL_VALIDATE_VERSION: v0.51.3/' \
  "uses unreviewed exact action actions/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5"
assert_exact_mutation_rejected \
  "job inherited safe environment" \
  "${repo_root}/.github/workflows/ci.yml" \
  's/SAFE_JOB_MODE: strict/SAFE_JOB_MODE: changed/' \
  "uses unreviewed exact action actions/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5"
assert_exact_mutation_rejected \
  "approved local script working-directory" \
  "${repo_root}/.github/workflows/ci.yml" \
  $'s/        run: |/        working-directory: \/tmp\\\n        run: |/g' \
  "declares forbidden working-directory"
assert_exact_mutation_rejected \
  "release trigger" \
  "${repo_root}/.github/workflows/release.yml" \
  's/^  push:/  pull_request_target:/' \
  "uses unreviewed exact action actions/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5"
assert_exact_mutation_rejected \
  "job container" \
  "${repo_root}/.github/workflows/ci.yml" \
  $'s/    runs-on: ubuntu-latest/    runs-on: ubuntu-latest\\\n    container: ghcr.io\/example\/attacker:latest/g' \
  "uses unreviewed exact action actions/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5"
assert_exact_mutation_rejected \
  "standalone builtin" \
  "${repo_root}/.github/workflows/ci.yml" \
  $'s/        run: |/        run: |\\\n          set -x/g' \
  "unreviewed exact statement containing set"
assert_exact_mutation_rejected \
  "pull request repository secret" \
  "${repo_root}/.github/workflows/ci.yml" \
  's/SAFE_JOB_MODE: strict/SAFE_JOB_MODE: strict\n      PRIVATE_TOKEN: ${{ secrets.RELEASES_TOKEN }}/' \
  "public workflow references forbidden repository secret RELEASES_TOKEN"
assert_exact_mutation_rejected \
  "pull request target repository secret" \
  "${repo_root}/.github/workflows/ci.yml" \
  's/pull_request:/pull_request_target:/; s/SAFE_JOB_MODE: strict/SAFE_JOB_MODE: strict\n      PRIVATE_TOKEN: ${{ secrets.RELEASES_TOKEN }}/' \
  "public workflow references forbidden repository secret RELEASES_TOKEN"
assert_exact_mutation_rejected \
  "push repository secret" \
  "${repo_root}/.github/workflows/release.yml" \
  's/REF_NAME: \${{ github.ref_name }}/REF_NAME: ${{ github.ref_name }}\n          PRIVATE_TOKEN: ${{ secrets.RELEASES_TOKEN }}/' \
  "secret RELEASES_TOKEN is not allowlisted"
assert_exact_mutation_rejected \
  "push cloud secret" \
  "${repo_root}/.github/workflows/release.yml" \
  's/REF_NAME: \${{ github.ref_name }}/REF_NAME: ${{ github.ref_name }}\n          CLOUD_TOKEN: ${{ secrets.DIGITALOCEAN_TOKEN }}/' \
  "references known cloud secret DIGITALOCEAN_TOKEN"
assert_exact_mutation_rejected \
  "release tag shell interpolation" \
  "${repo_root}/.github/workflows/release.yml" \
  's/gh release edit "\$REF_NAME"/gh release edit ${{ github.ref_name }}/' \
  "executes unreviewed exact statement containing gh"

for action_mutation in \
  '${{ vars.ACTION_REF }}' \
  'actions/checkout@main' \
  'actions/checkout@v4' \
  'actions/checkout@0123456789012345678901234567890123456789'; do
  expected="uses unreviewed exact action ${action_mutation}"
  if [[ "${action_mutation}" == '${{ vars.ACTION_REF }}' ]]; then
    expected="uses forbidden dynamic action ${action_mutation}"
  fi
  assert_exact_mutation_rejected \
    "action reference ${action_mutation}" \
    "${repo_root}/.github/workflows/ci.yml" \
    "s/actions\\/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5/${action_mutation//\//\\/}/g" \
    "${expected}"
done

assert_exact_mutation_rejected \
  "upload-artifact path" \
  "${repo_root}/.github/workflows/ci.yml" \
  's/path: conformance-evidence.json/path: other-evidence.json/' \
  "uses unreviewed exact action actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02"
assert_exact_mutation_rejected \
  "repository-dispatch repository" \
  "${repo_root}/.github/workflows/release.yml" \
  's|repository: GoCodeAlone/workflow-registry|repository: GoCodeAlone/workflow|' \
  "uses unreviewed exact action peter-evans/repository-dispatch@28959ce8df70de7be546dd1250a005dd32156697"
assert_exact_mutation_rejected \
  "repository-dispatch payload" \
  "${repo_root}/.github/workflows/release.yml" \
  's/"plugin": "digitalocean"/"plugin": "other"/' \
  "uses unreviewed exact action peter-evans/repository-dispatch@28959ce8df70de7be546dd1250a005dd32156697"
assert_exact_mutation_rejected \
  "repository-dispatch token" \
  "${repo_root}/.github/workflows/release.yml" \
  's/secrets.repo_dispatch_token/secrets.GITHUB_TOKEN/' \
  "uses unreviewed exact action peter-evans/repository-dispatch@28959ce8df70de7be546dd1250a005dd32156697"
assert_exact_mutation_rejected \
  "GoReleaser args" \
  "${repo_root}/.github/workflows/release.yml" \
  's/args: release --clean/args: release --clean --skip=publish/' \
  "uses unreviewed exact action goreleaser/goreleaser-action@f06c13b6b1a9625abc9e6e439d9c05a8f2190e94"
assert_exact_mutation_rejected \
  "GoReleaser environment" \
  "${repo_root}/.github/workflows/release.yml" \
  's/GITHUB_TOKEN: \${{ github.token }}/GITHUB_TOKEN: ${{ secrets.RELEASES_TOKEN }}/' \
  "secret RELEASES_TOKEN is not allowlisted"

pass_allowlist="${tmp_dir}/pass-allowlist.json"
pass_presence="${tmp_dir}/pass-presence.json"
cat >"${pass_presence}" <<'JSON'
[
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass.yml","contextSHA256":"f006c139dc0278d382b21e64d016d890c6f5971d5b7648f6a7c0fd67f1988972","state":"active","presence":"present"},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass-negative-guard.yml","contextSHA256":"f887c1b61a92298b060a9d7edd6ffc6335d1fece34111136ffdebbb855ec29b6","state":"active","presence":"present"},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass-expression-and-deny-guard.yml","contextSHA256":"3d6477a56ad4fbd1112035e552bb7306716cf3462620c6a9fe96fd832cbd79e3","state":"active","presence":"present"}
]
JSON
cat >"${pass_allowlist}" <<'JSON'
[
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/pass.yml",
    "secret": "GITHUB_TOKEN",
    "contextSHA256": "f006c139dc0278d382b21e64d016d890c6f5971d5b7648f6a7c0fd67f1988972",
    "state": "active", "rationale": "GitHub-provided token publishes release assets to this repository."
  }
]
JSON

pass_commands="${tmp_dir}/pass-commands.json"
cat >"${pass_commands}" <<'JSON'
[
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/pass.yml",
    "command": "go",
    "statementSHA256": "5384574a39b2103666734bbe92565841174832d0b8865a6d5f521eb663438c51",
    "contextSHA256": "f006c139dc0278d382b21e64d016d890c6f5971d5b7648f6a7c0fd67f1988972",
    "state": "active", "rationale": "Run the exact credential-free integration test fixture."
  },
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/pass.yml",
    "command": "go",
    "statementSHA256": "1bb497e3e13a1105cf24e3359fa3ef75de08b66ff8a2839cd7f9ea97824d9eb3",
    "contextSHA256": "f006c139dc0278d382b21e64d016d890c6f5971d5b7648f6a7c0fd67f1988972",
    "state": "active", "rationale": "Run the exact credential-free default Go test fixture."
  },
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/pass.yml",
    "command": "gh",
    "statementSHA256": "0a111d913d8601e23bc6e43fa1b4b6a5fa65c44c342a26c23f10bf8fa119827a",
    "contextSHA256": "f006c139dc0278d382b21e64d016d890c6f5971d5b7648f6a7c0fd67f1988972",
    "state": "active", "rationale": "Exercise the exact GitHub release upload fixture."
  },
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass.yml","command":"echo","statementSHA256":"552ab348c73a453fe78c6df7a1b2cf0c8381dc11a20908a96a643105c8abfdc7","contextSHA256":"f006c139dc0278d382b21e64d016d890c6f5971d5b7648f6a7c0fd67f1988972","state": "active", "rationale":"Exact rejection-guard echo statement."},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass.yml","command":"exit","statementSHA256":"552ab348c73a453fe78c6df7a1b2cf0c8381dc11a20908a96a643105c8abfdc7","contextSHA256":"f006c139dc0278d382b21e64d016d890c6f5971d5b7648f6a7c0fd67f1988972","state": "active", "rationale":"Exact rejection-guard exit statement."},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass.yml","command":"rg","statementSHA256":"552ab348c73a453fe78c6df7a1b2cf0c8381dc11a20908a96a643105c8abfdc7","contextSHA256":"f006c139dc0278d382b21e64d016d890c6f5971d5b7648f6a7c0fd67f1988972","state": "active", "rationale":"Exact rejection-guard search statement."},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass.yml","command":"$assignment","statementSHA256":"df3893e5269970fcf8bb076be5b5f849eec4a7237b311ab4129af31b78271a09","contextSHA256":"f006c139dc0278d382b21e64d016d890c6f5971d5b7648f6a7c0fd67f1988972","state": "active", "rationale":"Exact safe standalone assignment fixture."},
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/pass-expression-and-deny-guard.yml",
    "command": "go",
    "statementSHA256": "1bb497e3e13a1105cf24e3359fa3ef75de08b66ff8a2839cd7f9ea97824d9eb3",
    "contextSHA256": "3d6477a56ad4fbd1112035e552bb7306716cf3462620c6a9fe96fd832cbd79e3",
    "state": "active", "rationale": "Run the exact credential-free Go test fixture."
  },
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass-expression-and-deny-guard.yml","command":"echo","statementSHA256":"0d3a22321340ce7d2928c4f9b228a94c4ae2e00f5cd9482b48ed0d7b0f2aceab","contextSHA256":"3d6477a56ad4fbd1112035e552bb7306716cf3462620c6a9fe96fd832cbd79e3","state": "active", "rationale":"Exact expression-guard echo statement."},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass-expression-and-deny-guard.yml","command":"exit","statementSHA256":"0d3a22321340ce7d2928c4f9b228a94c4ae2e00f5cd9482b48ed0d7b0f2aceab","contextSHA256":"3d6477a56ad4fbd1112035e552bb7306716cf3462620c6a9fe96fd832cbd79e3","state": "active", "rationale":"Exact expression-guard exit statement."},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass-expression-and-deny-guard.yml","command":"rg","statementSHA256":"0d3a22321340ce7d2928c4f9b228a94c4ae2e00f5cd9482b48ed0d7b0f2aceab","contextSHA256":"3d6477a56ad4fbd1112035e552bb7306716cf3462620c6a9fe96fd832cbd79e3","state": "active", "rationale":"Exact expression-guard search statement."},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass-negative-guard.yml","command":"echo","statementSHA256":"16e6dfba0f3777c91b890a6aa03e083595250d63a6cc015886c4092caab0b07a","contextSHA256":"f887c1b61a92298b060a9d7edd6ffc6335d1fece34111136ffdebbb855ec29b6","state": "active", "rationale":"Exact negative-guard echo statement."},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass-negative-guard.yml","command":"exit","statementSHA256":"16e6dfba0f3777c91b890a6aa03e083595250d63a6cc015886c4092caab0b07a","contextSHA256":"f887c1b61a92298b060a9d7edd6ffc6335d1fece34111136ffdebbb855ec29b6","state": "active", "rationale":"Exact negative-guard exit statement."},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass-negative-guard.yml","command":"rg","statementSHA256":"16e6dfba0f3777c91b890a6aa03e083595250d63a6cc015886c4092caab0b07a","contextSHA256":"f887c1b61a92298b060a9d7edd6ffc6335d1fece34111136ffdebbb855ec29b6","state": "active", "rationale":"Exact negative-guard search statement."}
]
JSON

pass_actions="${tmp_dir}/pass-actions.json"
cat >"${pass_actions}" <<'JSON'
[
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass.yml","uses":"actions/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5","nodeSHA256":"72a9f885834e7e7cfc170d24954ed15b9c11a222760a6b919510993561321f03","contextSHA256":"f006c139dc0278d382b21e64d016d890c6f5971d5b7648f6a7c0fd67f1988972","state": "active", "rationale":"Exact immutable fixture action node."},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass.yml","uses":"actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff","nodeSHA256":"4097668e631432c1244a4dd4d9557c50491bf42112c9ba85e62d3fcf637047d1","contextSHA256":"f006c139dc0278d382b21e64d016d890c6f5971d5b7648f6a7c0fd67f1988972","state": "active", "rationale":"Exact immutable fixture action node."},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass.yml","uses":"actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02","nodeSHA256":"0d8a6b42c70a0f3275850fe9d63cfb646c0a9e0d7f6ea1c8b45171a1076060b7","contextSHA256":"f006c139dc0278d382b21e64d016d890c6f5971d5b7648f6a7c0fd67f1988972","state": "active", "rationale":"Exact immutable fixture action node."},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass.yml","uses":"GoCodeAlone/setup-wfctl@bcd880980f5bbe8d192d0c20ff6279d25331f956","nodeSHA256":"93dce32c457545dd0624d77c36ec255298b321308974a9cf046e67691e5dd745","contextSHA256":"f006c139dc0278d382b21e64d016d890c6f5971d5b7648f6a7c0fd67f1988972","state": "active", "rationale":"Exact immutable fixture action node."},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass.yml","uses":"goreleaser/goreleaser-action@f06c13b6b1a9625abc9e6e439d9c05a8f2190e94","nodeSHA256":"e755472a8b992588d44f5bed60d0ebdf304a0854661bd6b598ca4f6bceafa4b9","contextSHA256":"f006c139dc0278d382b21e64d016d890c6f5971d5b7648f6a7c0fd67f1988972","state": "active", "rationale":"Exact immutable fixture action node."},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass.yml","uses":"peter-evans/repository-dispatch@28959ce8df70de7be546dd1250a005dd32156697","nodeSHA256":"cab2cf5943a542ede48aea46c28a4b26d392cede1cd1685eed568c01fa4f0c39","contextSHA256":"f006c139dc0278d382b21e64d016d890c6f5971d5b7648f6a7c0fd67f1988972","state": "active", "rationale":"Exact immutable fixture action node."}
]
JSON

"${checker}" \
  --presence-allowlist "${pass_presence}" \
  --allowlist "${pass_allowlist}" \
  --command-allowlist "${pass_commands}" \
  --action-allowlist "${pass_actions}" \
  "${fixtures}/pass.yml" \
  "${fixtures}/pass-negative-guard.yml" \
  "${fixtures}/pass-expression-and-deny-guard.yml"

mutated_workflow="${fixtures}/pass.yml"
cp "${mutated_workflow}" "${mutation_backup}"
sed -i.bak 's/SAFE_VALUE=one/SAFE_VALUE=two/' "${mutated_workflow}"
rm -f "${mutated_workflow}.bak"
set +e
assignment_mutation_output="$("${checker}" \
  --presence-allowlist "${pass_presence}" \
  --allowlist "${pass_allowlist}" \
  --command-allowlist "${pass_commands}" \
  --action-allowlist "${pass_actions}" \
  "${fixtures}/pass.yml" \
  "${fixtures}/pass-negative-guard.yml" \
  "${fixtures}/pass-expression-and-deny-guard.yml" 2>&1)"
assignment_mutation_status=$?
set -e
cp "${mutation_backup}" "${mutated_workflow}"
mutated_workflow=""
if [[ "${assignment_mutation_status}" -eq 0 ]] || ! grep -Fq -- \
  "executes unreviewed standalone assignment" <<<"${assignment_mutation_output}"; then
  echo "safe assignment digest mutation was accepted" >&2
  printf '%s\n' "${assignment_mutation_output}" >&2
  exit 1
fi

set +e
yaml_structure_output="$("${checker}" \
  --allowlist "${empty_allowlist}" \
  "${fixtures}/reject-yaml-structure.yml" 2>&1)"
yaml_structure_status=$?
set -e
if [[ "${yaml_structure_status}" -eq 0 ]]; then
  echo "expected YAML aliases and duplicate keys to fail policy" >&2
  exit 1
fi
for expected in \
  "contains forbidden YAML alias" \
  "contains duplicate mapping key runs-on" \
  "contains duplicate mapping key run" \
  "contains duplicate mapping key uses" \
  "contains duplicate mapping key VALUE"; do
  if ! grep -Fq -- "${expected}" <<<"${yaml_structure_output}"; then
    echo "missing YAML structure diagnostic: ${expected}" >&2
    printf '%s\n' "${yaml_structure_output}" >&2
    exit 1
  fi
done

statement_secret_allowlist="${tmp_dir}/statement-secret-allowlist.json"
cat >"${statement_secret_allowlist}" <<'JSON'
[
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/reject-statement-authority.yml",
    "secret": "RELEASES_TOKEN",
    "state": "active", "rationale": "Mutation fixture proves exact statements reject secret output independently of secret-name review."
  }
]
JSON
set +e
statement_output="$("${checker}" \
  --allowlist "${statement_secret_allowlist}" \
  "${fixtures}/reject-statement-authority.yml" 2>&1)"
statement_status=$?
set -e
if [[ "${statement_status}" -eq 0 ]]; then
  echo "expected statement authority mutations to fail policy" >&2
  exit 1
fi
for expected in \
  "unreviewed exact statement containing go" \
  "assigns forbidden execution environment variable PATH" \
  "assigns forbidden execution environment variable BASH_ENV" \
  "assigns forbidden execution environment variable ENV" \
  "assigns forbidden execution environment variable SHELLOPTS" \
  "assigns forbidden execution environment variable LD_PRELOAD" \
  "assigns forbidden execution environment variable DYLD_INSERT_LIBRARIES" \
  "assigns forbidden execution environment variable BASH_FUNC_wrapper%% through env" \
  "redirects to forbidden GitHub command file GITHUB_ENV" \
  "redirects to forbidden GitHub command file GITHUB_PATH" \
  "unreviewed exact statement containing echo" \
  "unreviewed exact statement containing printf" \
  "uses forbidden dynamic command execution"; do
  if ! grep -Fq -- "${expected}" <<<"${statement_output}"; then
    echo "missing statement authority diagnostic: ${expected}" >&2
    printf '%s\n' "${statement_output}" >&2
    exit 1
  fi
done

set +e
execution_context_output="$("${checker}" \
  --allowlist "${empty_allowlist}" \
  "${fixtures}/reject-execution-context.yml" 2>&1)"
execution_context_status=$?
set -e
if [[ "${execution_context_status}" -eq 0 ]]; then
  echo "expected working-directory and inherited environment overrides to fail policy" >&2
  exit 1
fi
for expected in \
  "declares forbidden defaults.run.working-directory" \
  "declares forbidden working-directory" \
  "execution-affecting environment variable CC" \
  "execution-affecting environment variable GIT_CONFIG_GLOBAL" \
  "execution-affecting environment variable HOME" \
  "execution-affecting environment variable IFS" \
  "execution-affecting environment variable GOFLAGS" \
  "execution-affecting environment variable BASH_FUNC_WORKFLOW%%" \
  "execution-affecting environment variable BASH_FUNC_JOB%%" \
  "execution-affecting environment variable BASH_FUNC_STEP%%"; do
  if ! grep -Fq -- "${expected}" <<<"${execution_context_output}"; then
    echo "missing execution context diagnostic: ${expected}" >&2
    printf '%s\n' "${execution_context_output}" >&2
    exit 1
  fi
done

reject_allowlist="${tmp_dir}/reject-allowlist.json"
reject_presence="${tmp_dir}/reject-presence.json"
printf '[{"path":".github/workflows/scripts/fixtures/public-workflow-policy/reject.yml","contextSHA256":"e604ecf5a5ac14b51f40aa220e0aa14aeef76f9ddf297b6b69002d155a1f1715","state":"active","presence":"present"}]\n' >"${reject_presence}"
cat >"${reject_allowlist}" <<'JSON'
[
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/reject.yml",
    "contextSHA256": "e604ecf5a5ac14b51f40aa220e0aa14aeef76f9ddf297b6b69002d155a1f1715",
    "secret": "DIGITALOCEAN_TOKEN",
    "state": "active", "rationale": "A rationale must never make a known cloud credential acceptable."
  },
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/reject.yml",
    "contextSHA256": "e604ecf5a5ac14b51f40aa220e0aa14aeef76f9ddf297b6b69002d155a1f1715",
    "secret": "DEPLOY_AUTH",
    "state": "active", "rationale": "An alias must never hide provider authority from the policy."
  },
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/reject.yml",
    "contextSHA256": "e604ecf5a5ac14b51f40aa220e0aa14aeef76f9ddf297b6b69002d155a1f1715",
    "secret": "STALE_TOKEN",
    "state": "active", "rationale": "This deliberately stale exception proves fail-closed validation."
  }
]
JSON

set +e
reject_output="$("${checker}" \
  --presence-allowlist "${reject_presence}" \
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

executable_escape="${tmp_dir}/executable-escape.sh"
ln -s /dev/null "${executable_escape}"
executable_escape_rel="${executable_escape#"${repo_root}/"}"
invalid_executables="${tmp_dir}/invalid-executables.json"
negative_presence="${tmp_dir}/negative-presence.json"
printf '[{"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass-negative-guard.yml","contextSHA256":"f887c1b61a92298b060a9d7edd6ffc6335d1fece34111136ffdebbb855ec29b6","state":"active","presence":"present"}]\n' >"${negative_presence}"
cat >"${invalid_executables}" <<EOF
[
  {"path":"scripts/workflow-iac-host-conformance.sh","workflowPath":".github/workflows/scripts/fixtures/public-workflow-policy/pass-negative-guard.yml","contextSHA256":"f887c1b61a92298b060a9d7edd6ffc6335d1fece34111136ffdebbb855ec29b6","sha256":"0000000000000000000000000000000000000000000000000000000000000000","state":"active","rationale":"Hash mismatch and stale-entry mutation fixture."},
  {"path":"../escape.sh","workflowPath":".github/workflows/scripts/fixtures/public-workflow-policy/pass-negative-guard.yml","contextSHA256":"f887c1b61a92298b060a9d7edd6ffc6335d1fece34111136ffdebbb855ec29b6","sha256":"0000000000000000000000000000000000000000000000000000000000000000","state":"active","rationale":"Traversal mutation fixture."},
  {"path":"${executable_escape_rel}","workflowPath":".github/workflows/scripts/fixtures/public-workflow-policy/pass-negative-guard.yml","contextSHA256":"f887c1b61a92298b060a9d7edd6ffc6335d1fece34111136ffdebbb855ec29b6","sha256":"0000000000000000000000000000000000000000000000000000000000000000","state":"active","rationale":"Symlink escape mutation fixture."}
]
EOF
set +e
executable_integrity_output="$("${checker}" \
  --presence-allowlist "${negative_presence}" \
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
  "executes unreviewed exact statement containing eval"; do
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
    "state": "active", "rationale": "An opaque alias must not hide authority granted to a provider action."
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
  "uses unreviewed exact action digitalocean/action-doctl@v2" \
  "uses unreviewed exact reusable workflow digitalocean/platform/.github/workflows/live-deploy.yml@main" \
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
  "uses unreviewed exact action octocat/unknown-action@v1" \
  "uses unreviewed exact action ./.github/actions/not-reviewed" \
  "uses unreviewed exact action docker://alpine:3.20" \
  "uses unreviewed exact action digitalocean/experimental-deploy@v1" \
  'uses forbidden dynamic action ${{ vars.ACTION_REF }}' \
  "uses unreviewed exact action actions/checkout@v5" \
  "uses unreviewed exact action actions/setup-go@main" \
  "uses unreviewed exact action actions/upload-artifact@0123456789012345678901234567890123456789" \
  "uses unreviewed exact reusable workflow acme/platform/.github/workflows/deploy.yml@main"; do
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

set +e
command_analysis_output="$("${checker}" \
  --allowlist "${empty_allowlist}" \
  "${fixtures}/reject-command-analysis.yml" 2>&1)"
command_analysis_status=$?
set -e

if [[ "${command_analysis_status}" -eq 0 ]]; then
  echo "expected shell command analysis bypasses to fail policy" >&2
  exit 1
fi
for job in \
  assignment-prefix \
  command-wrapper \
  exec-wrapper \
  env-wrapper \
  subshell \
  group \
  command-substitution; do
  if ! grep -Fq -- "job ${job} invokes unallowlisted executable script ./scripts/live.sh" <<<"${command_analysis_output}"; then
    echo "missing AST command diagnostic for ${job}" >&2
    printf '%s\n' "${command_analysis_output}" >&2
    exit 1
  fi
done
for job in dynamic-path wrapped-dynamic-command sudo-wrapper; do
  if ! grep -Fq -- "job ${job} uses forbidden dynamic command execution" <<<"${command_analysis_output}"; then
    echo "missing dynamic command diagnostic for ${job}" >&2
    printf '%s\n' "${command_analysis_output}" >&2
    exit 1
  fi
done

set +e
shell_output="$("${checker}" \
  --allowlist "${empty_allowlist}" \
  "${fixtures}/reject-shell.yml" 2>&1)"
shell_status=$?
set -e

if [[ "${shell_status}" -eq 0 ]]; then
  echo "expected custom shells and shell parse failures to fail policy" >&2
  exit 1
fi
# The GitHub expression below is an intentionally literal expected diagnostic.
# shellcheck disable=SC2016
for expected in \
  "job custom-shell uses forbidden custom shell python" \
  'job dynamic-shell uses forbidden dynamic shell ${{ vars.SHELL }}' \
  "job invalid-shell-program shell parse failed"; do
  if ! grep -Fq -- "${expected}" <<<"${shell_output}"; then
    echo "missing shell policy diagnostic: ${expected}" >&2
    printf '%s\n' "${shell_output}" >&2
    exit 1
  fi
done

set +e
deny_execution_output="$("${checker}" \
  --allowlist "${empty_allowlist}" \
  "${fixtures}/reject-deny-pattern-execution.yml" 2>&1)"
deny_execution_status=$?
set -e

if [[ "${deny_execution_status}" -eq 0 ]] || ! grep -Fq -- \
  "job execute-deny-value uses provider deny pattern outside a pure rejection guard" \
  <<<"${deny_execution_output}"; then
  echo "provider deny pattern escaped its parser-proven guard" >&2
  printf '%s\n' "${deny_execution_output}" >&2
  exit 1
fi

set +e
shell_inheritance_output="$("${checker}" \
  --allowlist "${empty_allowlist}" \
  "${fixtures}/reject-shell-inheritance.yml" 2>&1)"
shell_inheritance_status=$?
missing_shell_output="$("${checker}" \
  --allowlist "${empty_allowlist}" \
  "${fixtures}/reject-missing-shell.yml" 2>&1)"
missing_shell_status=$?
unsafe_commands="${tmp_dir}/unsafe-commands.json"
cat >"${unsafe_commands}" <<'JSON'
[
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/reject-unsafe-programs.yml",
    "command": "go",
    "statementSHA256": "0000000000000000000000000000000000000000000000000000000000000000",
    "contextSHA256": "0000000000000000000000000000000000000000000000000000000000000000",
    "state": "active", "rationale": "Mutation: prove an allowlisted command with the wrong subcommand remains rejected."
  }
]
JSON
unsafe_program_output="$("${checker}" \
  --allowlist "${empty_allowlist}" \
  --command-allowlist "${unsafe_commands}" \
  "${fixtures}/reject-unsafe-programs.yml" 2>&1)"
unsafe_program_status=$?
set -e

if [[ "${shell_inheritance_status}" -eq 0 || "${missing_shell_status}" -eq 0 ]]; then
  echo "expected inherited, overridden, and missing shells to fail policy" >&2
  exit 1
fi
# The GitHub expression below is an intentionally literal expected diagnostic.
# shellcheck disable=SC2016
for expected in \
  'workflow .github/workflows/scripts/fixtures/public-workflow-policy/reject-shell-inheritance.yml uses forbidden dynamic shell ${{ vars.DEFAULT_SHELL }}' \
  "job job-python uses forbidden custom shell python" \
  "job job-pwsh uses forbidden custom shell pwsh" \
  "job job-pwsh executes unreviewed exact statement containing invoke-restmethod" \
  "job job-custom uses forbidden custom shell fish" \
  "job step-override uses forbidden custom shell powershell"; do
  if ! grep -Fq -- "${expected}" <<<"${shell_inheritance_output}"; then
    echo "missing effective shell diagnostic: ${expected}" >&2
    printf '%s\n' "${shell_inheritance_output}" >&2
    exit 1
  fi
done
if ! grep -Fq -- "job implicit-platform-default does not declare an explicit Bash shell" <<<"${missing_shell_output}"; then
  echo "missing implicit platform shell diagnostic" >&2
  printf '%s\n' "${missing_shell_output}" >&2
  exit 1
fi

if [[ "${unsafe_program_status}" -eq 0 ]]; then
  echo "expected provider-capable and dynamic programs to fail policy" >&2
  exit 1
fi
for expected in \
  "job curl-endpoint executes categorically forbidden command curl" \
  "job wget-endpoint executes categorically forbidden command wget" \
  "job http-client executes categorically forbidden command http" \
  "job python-code executes categorically forbidden command python" \
  "job node-code executes categorically forbidden command node" \
  "job dynamic-interpreter-script executes categorically forbidden command python" \
  "job powershell-endpoint executes categorically forbidden command pwsh" \
  "job go-run executes categorically forbidden command go with argv [\"run\"" \
  "job npx-exec executes categorically forbidden command npx" \
  "job npm-exec executes categorically forbidden command npm" \
  "job docker-run executes categorically forbidden command docker" \
  "job docker-login executes categorically forbidden command docker" \
  "job docker-push executes categorically forbidden command docker" \
  "job terraform-plan executes categorically forbidden command terraform" \
  "job tofu-plan executes categorically forbidden command tofu" \
  "job pulumi-preview executes categorically forbidden command pulumi" \
  "job kubectl-get executes categorically forbidden command kubectl" \
  "job helm-list executes categorically forbidden command helm" \
  "job ansible-playbook executes categorically forbidden command ansible" \
  "job rclone-list executes categorically forbidden command rclone" \
  "job s3cmd-list executes categorically forbidden command s3cmd" \
  "job mc-list executes categorically forbidden command mc" \
  "job unknown-executable executes unreviewed exact statement containing mystery-tool" \
  "job sudo-wrapper uses forbidden dynamic command execution"; do
  if ! grep -Fq -- "${expected}" <<<"${unsafe_program_output}"; then
    echo "missing unsafe program diagnostic: ${expected}" >&2
    printf '%s\n' "${unsafe_program_output}" >&2
    exit 1
  fi
done

invalid_commands="${tmp_dir}/invalid-commands.json"
cat >"${invalid_commands}" <<'JSON'
[
  {
    "path": "/tmp/absolute.yml",
    "command": "go",
    "statementSHA256": "1111111111111111111111111111111111111111111111111111111111111111",
    "contextSHA256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "state": "active", "rationale": "Absolute paths must not grant command authority."
  },
  {
    "path": "../traversal.yml",
    "command": "go",
    "statementSHA256": "2222222222222222222222222222222222222222222222222222222222222222",
    "contextSHA256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "state": "active", "rationale": "Traversal must not grant command authority."
  },
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/pass-negative-guard.yml",
    "command": "terraform",
    "statementSHA256": "3333333333333333333333333333333333333333333333333333333333333333",
    "contextSHA256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "state": "active", "rationale": "Known provider commands are forbidden even with a rationale."
  },
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/pass-negative-guard.yml",
    "command": "go",
    "statementSHA256": "4444444444444444444444444444444444444444444444444444444444444444",
    "contextSHA256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "state": "active", "rationale": "Deliberately stale command capability mutation."
  },
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/pass-negative-guard.yml",
    "command": "go",
    "statementSHA256": "4444444444444444444444444444444444444444444444444444444444444444",
    "contextSHA256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "state": "active", "rationale": "Deliberate duplicate command capability mutation."
  }
]
JSON
set +e
invalid_commands_output="$("${checker}" \
  --allowlist "${empty_allowlist}" \
  --command-allowlist "${invalid_commands}" \
  "${fixtures}/pass-negative-guard.yml" 2>&1)"
invalid_commands_status=$?
set -e
if [[ "${invalid_commands_status}" -eq 0 ]]; then
  echo "expected invalid command allowlist entries to fail policy" >&2
  exit 1
fi
for expected in \
  "command allowlist path /tmp/absolute.yml must be repository-relative" \
  "command allowlist path ../traversal.yml escapes the repository" \
  "provider-capable command terraform is categorically unallowlistable" \
  "duplicate command allowlist entry go sha256:4444444444444444444444444444444444444444444444444444444444444444" \
  "no trust group matches workflow"; do
  if ! grep -Fq -- "${expected}" <<<"${invalid_commands_output}"; then
    echo "missing invalid command allowlist diagnostic: ${expected}" >&2
    printf '%s\n' "${invalid_commands_output}" >&2
    exit 1
  fi
done

invalid_actions="${tmp_dir}/invalid-actions.json"
cat >"${invalid_actions}" <<'JSON'
[
  {"path":"/tmp/absolute.yml","uses":"actions/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5","nodeSHA256":"1111111111111111111111111111111111111111111111111111111111111111","contextSHA256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state": "active", "rationale":"Absolute paths must not grant action authority."},
  {"path":"../traversal.yml","uses":"actions/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5","nodeSHA256":"2222222222222222222222222222222222222222222222222222222222222222","contextSHA256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state": "active", "rationale":"Traversal must not grant action authority."},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass-negative-guard.yml","uses":"digitalocean/action-doctl@0123456789012345678901234567890123456789","nodeSHA256":"3333333333333333333333333333333333333333333333333333333333333333","contextSHA256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state": "active", "rationale":"Provider actions remain categorically forbidden."},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass-negative-guard.yml","uses":"${{ vars.ACTION_REF }}","nodeSHA256":"4444444444444444444444444444444444444444444444444444444444444444","contextSHA256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state": "active", "rationale":"Dynamic action references remain forbidden."},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass-negative-guard.yml","uses":"actions/checkout@v4","nodeSHA256":"6666666666666666666666666666666666666666666666666666666666666666","contextSHA256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state": "active", "rationale":"Mutable action tags remain forbidden in trust policy."},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass-negative-guard.yml","uses":"actions/checkout@main","nodeSHA256":"7777777777777777777777777777777777777777777777777777777777777777","contextSHA256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state": "active", "rationale":"Mutable action branches remain forbidden in trust policy."},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass-negative-guard.yml","uses":"actions/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5","nodeSHA256":"5555555555555555555555555555555555555555555555555555555555555555","contextSHA256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state": "active", "rationale":"Deliberately stale exact action mutation."},
  {"path":".github/workflows/scripts/fixtures/public-workflow-policy/pass-negative-guard.yml","uses":"actions/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5","nodeSHA256":"5555555555555555555555555555555555555555555555555555555555555555","contextSHA256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state": "active", "rationale":"Deliberate duplicate exact action mutation."}
]
JSON
set +e
invalid_actions_output="$("${checker}" \
  --allowlist "${empty_allowlist}" \
  --action-allowlist "${invalid_actions}" \
  "${fixtures}/pass-negative-guard.yml" 2>&1)"
invalid_actions_status=$?
set -e
if [[ "${invalid_actions_status}" -eq 0 ]]; then
  echo "expected invalid action allowlist entries to fail policy" >&2
  exit 1
fi
if [[ "$(grep -Fc -- "invalid action allowlist entry" <<<"${invalid_actions_output}")" -lt 3 ]]; then
  echo "dynamic, tag, and branch action allowlist entries were not all rejected" >&2
  printf '%s\n' "${invalid_actions_output}" >&2
  exit 1
fi
for expected in \
  "action allowlist path /tmp/absolute.yml must be repository-relative" \
  "action allowlist path ../traversal.yml escapes the repository" \
  "provider action digitalocean/action-doctl@0123456789012345678901234567890123456789 is categorically unallowlistable" \
  "invalid action allowlist entry" \
  "duplicate action allowlist entry actions/checkout@34e114876b0b11c390a56381ad16ebd13914f8d5" \
  "no trust group matches workflow"; do
  if ! grep -Fq -- "${expected}" <<<"${invalid_actions_output}"; then
    echo "missing invalid action allowlist diagnostic: ${expected}" >&2
    printf '%s\n' "${invalid_actions_output}" >&2
    exit 1
  fi
done

invalid_paths_allowlist="${tmp_dir}/invalid-paths-allowlist.json"
cat >"${invalid_paths_allowlist}" <<'JSON'
[
  {
    "path": "/tmp/absolute-workflow.yml",
    "secret": "PACKAGE_TOKEN",
    "state": "active", "rationale": "Absolute paths must never be accepted as workflow policy exceptions."
  },
  {
    "path": "../traversal-workflow.yml",
    "secret": "RELEASES_TOKEN",
    "state": "active", "rationale": "Parent traversal must never escape exact repository-relative matching."
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
    "state": "active", "rationale": "A global opaque alias must remain visible to provider-authority analysis."
  },
  {
    "path": ".github/workflows/scripts/fixtures/public-workflow-policy/reject-secret-syntax.yml",
    "secret": "DIGITALOCEAN_TOKEN",
    "state": "active", "rationale": "Known provider credentials remain forbidden regardless of syntax or rationale."
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
  "known cloud credential variable AWS_ACCESS_KEY_ID" \
  "known cloud secret SPACES_ACCESS_KEY_ID" \
  "known cloud secret SPACES_SECRET_ACCESS_KEY" \
  "known cloud secret DIGITALOCEAN_SPACES_ACCESS_KEY_ID" \
  "known cloud secret DIGITALOCEAN_SPACES_SECRET_ACCESS_KEY" \
  "known cloud secret DO_SPACES_ACCESS_KEY_ID" \
  "known cloud secret DO_SPACES_SECRET_ACCESS_KEY"; do
  if ! grep -Fq -- "${expected}" <<<"${global_output}"; then
    echo "missing expected global policy diagnostic: ${expected}" >&2
    printf '%s\n' "${global_output}" >&2
    exit 1
  fi
done

echo "public workflow policy fixtures passed"
