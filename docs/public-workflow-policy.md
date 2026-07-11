# Public workflow policy

Public workflow changes are checked by `.github/workflows/public-workflow-policy.yml`.
The `pull_request_target` job executes only SHA-pinned actions and the analyzer,
wrapper, module, and trust manifests from the trusted base checkout. The pull
request checkout is stored separately and read only as policy input. Candidate
actions, scripts, Go files, modules, and trust manifests are never executed.
The job has only `contents: read`, uses GitHub-hosted runners, and receives no
cloud credentials or OIDC authority.

Workflow authority changes use three pull requests:

1. Add one `staged` trust context group for the future complete workflow digest
   while retaining the current `active` group. The current workflow selects the
   active group; the staged group is tolerated only as transition data.
2. After that trust-only pull request merges, submit the workflow change. The
   trusted base selects the staged group matching the candidate workflow while
   tolerating the old active group.
3. After the workflow merges, submit a trust-only cleanup that removes the old
   group and promotes the new group from `staged` to `active`.

The base manifests intentionally reject workflow additions, removals, or edits
made in the same pull request as their trust changes. Candidate changes to the
checker, analyzer, tests, or manifests cannot weaken enforcement for that pull
request: even a candidate checker replaced with a no-op is never executed or
consulted by the trusted-base job, and a same-pull-request live workflow remains
rejected by the base analyzer.

The one-time pull request introducing this guard cannot itself be protected by
a workflow absent from its base branch. After bootstrap merges, subsequent
`.github/workflows` changes are checked by the trusted base implementation.

The same stable `Public Workflow Policy / policy` check also runs on pushes to
`main`, comparing `github.event.before` as trusted policy authority with the new
commit as candidate data. Task completion remains contingent on repository
branch protection: changes must use pull requests, require at least one
approval with stale approvals dismissed, require this exact status check,
block direct pushes, enforce administrators, and permit no bypass actors. After
the guard is merged and the required check is provisioned, an administrator
must run:

```bash
./.github/workflows/scripts/verify-public-workflow-branch-protection.sh GoCodeAlone/workflow-plugin-digitalocean main
```

This script is read-only. It verifies classic branch protection or an active
ruleset and never changes repository settings.
