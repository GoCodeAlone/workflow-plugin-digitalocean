#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
checker="${repo_root}/.github/workflows/scripts/test-conformance-workflows.sh"

new_fixture() {
  local fixture
  fixture="$(mktemp -d)"
  mkdir -p "${fixture}/.github/workflows" "${fixture}/docs"
  cp -R "${repo_root}/.github/conformance" "${fixture}/.github/"
  cp -R "${repo_root}/.github/workflows/." "${fixture}/.github/workflows/"
  cp "${repo_root}/docs/conformance-runbook.md" "${fixture}/docs/"
  printf '%s\n' "${fixture}"
}

expect_rejected() {
  local name="$1"
  local fixture="$2"
  local expected="$3"
  local output
  local exit_code

  set +e
  output="$(CONFORMANCE_REPO_ROOT="${fixture}" "${checker}" 2>&1)"
  exit_code=$?
  set -e

  if [[ "${exit_code}" -eq 0 ]]; then
    echo "mutation test ${name}: unsafe mutation escaped the structure gate" >&2
    return 1
  fi
  if ! grep -Fq -- "${expected}" <<< "${output}"; then
    echo "mutation test ${name}: wrong rejection" >&2
    echo "wanted: ${expected}" >&2
    echo "actual: ${output}" >&2
    return 1
  fi
  echo "mutation test ${name}: rejected"
}

mutate_credential_free_runner() {
  local file="$1"
  local output="${file}.mutated"
  awk '
    $0 == "  credential-free:" { in_job = 1 }
    in_job && $0 ~ /^  [a-zA-Z0-9_-]+:/ && $0 != "  credential-free:" { in_job = 0 }
    in_job && $0 == "    runs-on: ubuntu-latest" {
      print "    runs-on: [self-hosted, linux]"
      changed = 1
      next
    }
    { print }
    END { if (!changed) exit 42 }
  ' "${file}" > "${output}"
  mv "${output}" "${file}"
}

mutate_hard_cap_abort() {
  local file="$1"
  local output="${file}.mutated"
  awk '
    $0 == "      - name: Enforce budget caps" { in_step = 1 }
    in_step && $0 ~ /^      - name:/ && $0 != "      - name: Enforce budget caps" { in_step = 0 }
    in_step && $0 == "            exit 1" {
      print "            echo \"::warning::hard cap mutation did not abort\""
      changed = 1
      next
    }
    { print }
    END { if (!changed) exit 42 }
  ' "${file}" > "${output}"
  mv "${output}" "${file}"
}

mutate_top_level_do_token() {
  local file="$1"
  local output="${file}.mutated"
  awk '
    $0 == "env:" {
      print
      print "  DO_CONFORMANCE_API_TOKEN: ${{ secrets.DO_CONFORMANCE_API_TOKEN }}"
      changed = 1
      next
    }
    { print }
    END { if (!changed) exit 42 }
  ' "${file}" > "${output}"
  mv "${output}" "${file}"
}

mutate_credential_free_secret() {
  local file="$1"
  local output="${file}.mutated"
  awk '
    $0 == "  credential-free:" { in_job = 1 }
    in_job && $0 == "    runs-on: ubuntu-latest" {
      print
      print "    env:"
      print "      RELEASES_TOKEN: ${{ secrets.RELEASES_TOKEN }}"
      changed = 1
      next
    }
    { print }
    END { if (!changed) exit 42 }
  ' "${file}" > "${output}"
  mv "${output}" "${file}"
}

mutate_live_global_git_config() {
  local file="$1"
  local output="${file}.mutated"
  awk '
    $0 == "      - name: Build accepted plugin loader layout" {
      print "      - name: Unsafe persistent credential rewrite"
      print "        run: git config --global credential.helper store"
      changed = 1
    }
    { print }
    END { if (!changed) exit 42 }
  ' "${file}" > "${output}"
  mv "${output}" "${file}"
}

runner_fixture="$(new_fixture)"
budget_fixture="$(new_fixture)"
token_fixture="$(new_fixture)"
secret_fixture="$(new_fixture)"
live_git_fixture="$(new_fixture)"
trap 'rm -rf "${runner_fixture}" "${budget_fixture}" "${token_fixture}" "${secret_fixture}" "${live_git_fixture}"' EXIT

mutate_credential_free_runner "${runner_fixture}/.github/workflows/conformance-smoke.yml"
failures=0
expect_rejected \
  credential-free-self-hosted \
  "${runner_fixture}" \
  "credential-free job must contain: runs-on: ubuntu-latest" || failures=$((failures + 1))

mutate_hard_cap_abort "${budget_fixture}/.github/workflows/conformance-budget-check.yml"
expect_rejected \
  hard-cap-no-abort \
  "${budget_fixture}" \
  "hard-cap branch must contain: exit 1" || failures=$((failures + 1))

mutate_top_level_do_token "${token_fixture}/.github/workflows/conformance-smoke.yml"
expect_rejected \
  credential-free-inherited-do-token \
  "${token_fixture}" \
  "workflow preamble must not contain: DO_CONFORMANCE_API_TOKEN" || failures=$((failures + 1))

mutate_credential_free_secret "${secret_fixture}/.github/workflows/conformance-smoke.yml"
expect_rejected \
  credential-free-repository-secret \
  "${secret_fixture}" \
  "credential-free job must not contain: RELEASES_TOKEN" || failures=$((failures + 1))

mutate_live_global_git_config "${live_git_fixture}/.github/workflows/conformance-smoke.yml"
expect_rejected \
  live-persistent-git-config \
  "${live_git_fixture}" \
  "live-smoke job must not contain: git config --global" || failures=$((failures + 1))

if [[ "${failures}" -ne 0 ]]; then
  exit 1
fi

echo "conformance workflow mutation tests: ok"
