# Public workflow policy

Public workflow changes are checked by `.github/workflows/public-workflow-policy.yml`.
The `pull_request_target` job executes only SHA-pinned actions and the analyzer,
wrapper, module, and trust manifests from the trusted base checkout. The pull
request checkout is stored separately and read only as policy input. Candidate
actions, scripts, Go files, modules, and trust manifests are never executed.
The job has only `contents: read`, uses GitHub-hosted runners, and receives no
cloud credentials or OIDC authority.

Workflow authority changes use two pull requests:

1. Update only the policy implementation, policy tests, and relevant trust
   manifests. Keep workflow YAML and non-policy executable data unchanged.
   The trusted-base job deliberately ignores candidate policy authority while
   ordinary candidate CI validates the candidate policy and manifests.
2. After the trust-policy pull request merges, submit the workflow YAML or
   non-policy executable change against that newly trusted base.

The base manifests intentionally reject workflow additions, removals, or edits
made in the same pull request as their trust changes. Candidate changes to the
checker, analyzer, tests, or manifests cannot weaken enforcement for that pull
request: even a candidate checker replaced with a no-op is never executed or
consulted by the trusted-base job, and a same-pull-request live workflow remains
rejected by the base analyzer.

The one-time pull request introducing this guard cannot itself be protected by
a workflow absent from its base branch. After bootstrap merges, subsequent
`.github/workflows` changes are checked by the trusted base implementation.
