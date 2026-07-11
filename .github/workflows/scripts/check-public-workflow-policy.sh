#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
policytool="${repo_root}/.github/workflows/policytool"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    LC_ALL=C shasum -a 256 "$1" | awk '{print $1}'
  else
    echo "no SHA-256 implementation available" >&2
    exit 1
  fi
}

verify_policytool_layout() {
  while IFS= read -r policytool_path; do
    relative_path="${policytool_path#"${policytool}/"}"
    case "${relative_path}" in
      main.go|main_test.go|go.mod|go.sum) ;;
      *)
        echo "unexpected policytool path: ${relative_path}" >&2
        exit 1
        ;;
    esac
    if [[ ! -f "${policytool_path}" || -L "${policytool_path}" ]]; then
      echo "policytool path must be a regular non-symlink file: ${relative_path}" >&2
      exit 1
    fi
  done < <(find "${policytool}" -mindepth 1 -print | LC_ALL=C sort)
}

verify_policytool_file() {
  local relative_path="$1"
  local expected="$2"
  local actual
  actual="$(sha256_file "${policytool}/${relative_path}")"
  if [[ "${actual}" != "${expected}" ]]; then
    echo "policytool hash mismatch for ${relative_path}" >&2
    exit 1
  fi
}

verify_policytool() {
  verify_policytool_layout
  verify_policytool_file main.go 1f320c90993f084a064e16126e68044421738681a4d13ba7049ad10abf0e16e2
  verify_policytool_file main_test.go 073fae3d120ffb730ada98dd0dbb6f2da03766feae95c89b716ed6f6a932a999
  verify_policytool_file go.mod ddbfb09771aa824f859940c0a937f2eeb900cf0786b3cbf4a3ea741a0302b46e
  verify_policytool_file go.sum 790ef858e5aeed12269a69e764ac69c02c3877678b0e7d9384ad3728b6e09f6c
}

cd "${policytool}"
verify_policytool
env GOWORK=off GOFLAGS=-mod=readonly go mod download
verify_policytool
exec env GOWORK=off GOFLAGS=-mod=readonly go run ./main.go --repo "${repo_root}" "$@"
