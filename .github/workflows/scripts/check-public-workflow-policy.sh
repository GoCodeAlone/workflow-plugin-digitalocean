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

verify_policytool_file main.go 1c2689e49e877181f06ca596df6601b23bdc5b94042387976a49732762152f32
verify_policytool_file main_test.go a90bb896a957a6afd9d4e6f6dfac3f14caadbec8706fe906942e24c4c86532da
verify_policytool_file go.mod ddbfb09771aa824f859940c0a937f2eeb900cf0786b3cbf4a3ea741a0302b46e
verify_policytool_file go.sum 790ef858e5aeed12269a69e764ac69c02c3877678b0e7d9384ad3728b6e09f6c

cd "${policytool}"
exec env GOWORK=off go run . --repo "${repo_root}" "$@"
