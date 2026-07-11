# DigitalOcean Conformance Runbook

This repository owns DigitalOcean tokens, live-cloud checks, budget controls,
cleanup, and leak response. Workflow core supplies provider-neutral contracts;
it does not operate this account or these jobs.

## Execution boundary

- Pull requests, including forks, run only credential-free conformance on
  GitHub-hosted `ubuntu-latest` runners.
- Live mutation runs only after a trusted push to this repository's `main` or
  an explicit `workflow_dispatch`. Live jobs use `[self-hosted, linux]`.
- The hourly leak scrubber is the only scheduled live path.
- All live paths fail before an API mutation when
  `DO_CONFORMANCE_API_TOKEN` is unavailable.
- The jobs use released `wfctl` `v0.85.4`; W0 does not consume unreleased
  provider contracts.

The smoke job builds the accepted external-loader layout at
`.conformance/plugins/workflow-plugin-digitalocean/`: the directory contains a
matching `workflow-plugin-digitalocean` executable, `plugin.json`, and
`plugin.contracts.json`. It then creates one tagged Droplet through `wfctl`,
runs the plugin's `TestConformance` entrypoint, and invokes tag cleanup under
`always()`.

## Budget approval

| Field | Value |
|---|---|
| Approver | `jon@langevin.me` |
| Approval date | 2026-05-03 |
| Hard stop | **$25 month-to-date** |
| Soft alert | **$15 month-to-date** |
| Workload | one smallest-class Droplet per trusted smoke run |

Change a threshold only by appending a dated approval entry here and updating
`.github/workflows/conformance-budget-check.yml` in the same PR. The reusable
budget job queries `/v2/customers/my/balance` and aborts before the smoke job can
start when usage meets or exceeds the hard stop. Both the budget job and hourly
scrubber use the same executable predicate, including the exact `$25` boundary.

## Token rotation

`DO_CONFORMANCE_API_TOKEN` must be a read/write Personal Access Token for a
dedicated conformance account with no production resources. Never print it,
upload it, or put it in a step summary.

1. Create the replacement token in the dedicated account.
2. Replace the repository Actions secret.
3. Manually dispatch `conformance-smoke.yml` and verify the budget preflight,
   tagged creation, conformance entrypoint, and cleanup.
4. Revoke the old token only after that run finishes.

| Last rotated | By | Notes |
|---|---|---|
| 2026-05-03 | `jon@langevin.me` | Initial dedicated-account token |

## Cleanup and incidents

Cleanup has two layers:

1. `conformance-smoke.yml` always runs `wfctl infra cleanup --fix` with the
   unique `wf-do-conformance-<run>-<attempt>` tag.
2. `conformance-leak-scrubber.yml` hourly deletes tagged Droplets older than
   one hour and files or updates an incident. Every pagination URL is checked
   before authenticated use: only exact `https://api.digitalocean.com`
   authority is accepted, and repeated pages abort the traversal.

The helper deduplicates on two labels. Automated leak issues use
`conformance-leak-incident` plus `auto-filed-leak`; automated budget issues use
`conformance-budget-incident` plus `auto-filed-budget`. Do not remove the helper
label from an open automated issue. Close the issue to start a new dedup chain;
use only the primary label on a separate human postmortem issue.

When cleanup fails, immediately run the scrubber manually, inspect the tagged
resources in the dedicated account, and delete any survivors. If the installed
plugin or released `wfctl` is the cause, disable the plugin schedules/manual
live jobs and restore the last known cleanup helper while fixing forward. The
legacy Workflow-core jobs remain the rollback path until their separately
gated removal.

## Local validation

```sh
./.github/workflows/scripts/test-conformance-workflows.sh
./.github/workflows/scripts/test-conformance-workflow-mutations.sh
./.github/workflows/scripts/test-conformance-safety-helpers.sh
actionlint .github/workflows/*.yml
GOWORK=off go test -tags=conformance ./internal/... -run '^TestConformance$' -count=1
```
