#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "usage: $0 <count-or-spend> <details>" >&2
  exit 64
fi

count="$1"
details="$2"
primary_label="${PRIMARY_LABEL:-conformance-leak-incident}"
helper_label="${HELPER_LABEL:-auto-filed-leak}"
issue_title="${ISSUE_TITLE:-DigitalOcean conformance leak: ${count} resources scrubbed}"
now="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"

existing="$(gh issue list \
  --label "${primary_label}" \
  --label "${helper_label}" \
  --state open \
  --json number \
  --jq '.[0].number // empty')"

if [[ -n "${existing}" ]]; then
  gh issue comment "${existing}" \
    --body "Automated conformance check at ${now}: value=${count}. Details: ${details}"
else
  gh issue create \
    --label "${primary_label}" \
    --label "${helper_label}" \
    --title "${issue_title}" \
    --body "Automated conformance check at ${now}: value=${count}. Details: ${details}

Triage and dedup-label rules: https://github.com/${GITHUB_REPOSITORY}/blob/main/docs/conformance-runbook.md"
fi
