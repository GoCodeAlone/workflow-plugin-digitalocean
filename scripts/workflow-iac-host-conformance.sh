#!/usr/bin/env bash
set -euo pipefail

engine_version="${1:?usage: workflow-iac-host-conformance.sh <workflow-engine-version> [label]}"
label="${2:-${engine_version}}"

case "${engine_version}" in
  v*) ;;
  *) engine_version="v${engine_version}" ;;
esac

repo_root="$(git rev-parse --show-toplevel)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

work_dir="${tmp_dir}/repo"
mkdir -p "${work_dir}"

rsync -a \
  --exclude '.git' \
  --exclude '.worktrees' \
  --exclude '_worktrees' \
  --exclude 'data' \
  "${repo_root}/" "${work_dir}/"

cd "${work_dir}"

echo "==> workflow IaC host conformance (${label}): github.com/GoCodeAlone/workflow@${engine_version}"
go mod edit -require "github.com/GoCodeAlone/workflow@${engine_version}"
GOWORK=off go mod tidy
WORKFLOW_IAC_HOST_CONFORMANCE=1 GOWORK=off go test ./internal -run TestWorkflowHostConformance_LoadsTypedIaCPlugin -count=1 -v
