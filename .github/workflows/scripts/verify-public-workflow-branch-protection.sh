#!/usr/bin/env bash
set -euo pipefail

repo="${1:?usage: verify-public-workflow-branch-protection.sh OWNER/REPO [BRANCH]}"
branch="${2:-main}"
required_check="Public Workflow Policy / policy"

classic="$(gh api "repos/${repo}/branches/${branch}/protection" 2>/dev/null || true)"
if jq -e --arg check "${required_check}" '
  .enforce_admins.enabled == true
  and .required_pull_request_reviews.required_approving_review_count >= 1
  and .required_pull_request_reviews.dismiss_stale_reviews == true
  and ((.required_pull_request_reviews.bypass_pull_request_allowances.users // []) | length == 0)
  and ((.required_pull_request_reviews.bypass_pull_request_allowances.teams // []) | length == 0)
  and ((.required_pull_request_reviews.bypass_pull_request_allowances.apps // []) | length == 0)
  and (.required_status_checks.contexts // [] | index($check) != null)
  and (.restrictions == null or ((.restrictions.users // []) | length == 0)
       and ((.restrictions.teams // []) | length == 0)
       and ((.restrictions.apps // []) | length == 0))
  and (.allow_force_pushes.enabled // false) == false
  and (.allow_deletions.enabled // false) == false
' <<<"${classic}" >/dev/null 2>&1; then
  echo "branch protection verified for ${repo}:${branch} (${required_check})"
  exit 0
fi

rulesets="$(gh api --paginate "repos/${repo}/rulesets" 2>/dev/null | jq -s 'add // []')"
while IFS= read -r ruleset_id; do
  ruleset="$(gh api "repos/${repo}/rulesets/${ruleset_id}")"
  if jq -e --arg branch "refs/heads/${branch}" --arg check "${required_check}" '
    .enforcement == "active"
    and ((.bypass_actors // []) | length == 0)
    and ((.conditions.ref_name.include // []) | any(. == $branch or . == "~DEFAULT_BRANCH"))
    and ((.conditions.ref_name.exclude // []) | length == 0)
    and (any(.rules[]?; .type == "pull_request"
      and .parameters.required_approving_review_count >= 1
      and .parameters.dismiss_stale_reviews_on_push == true))
    and (any(.rules[]?; .type == "required_status_checks"
      and any(.parameters.required_status_checks[]?; .context == $check)))
  ' <<<"${ruleset}" >/dev/null; then
    echo "ruleset protection verified for ${repo}:${branch} (${required_check})"
    exit 0
  fi
done < <(jq -r '.[] | select(.enforcement == "active") | .id' <<<"${rulesets}")

echo "branch protection is incomplete for ${repo}:${branch}; require PRs, one approval, stale dismissal, '${required_check}', administrator enforcement, and no bypass" >&2
exit 1
