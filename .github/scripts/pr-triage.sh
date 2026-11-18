#!/usr/bin/env bash
# Keeps one comment on a pull request that lists its failed checks, so the contributor fixes them before review.
#
#   pr-triage.sh SHA [PR]
#
# SHA is the commit a check workflow ran on; PR is found from it when not given. Needs gh with GH_TOKEN, and
# GITHUB_REPOSITORY (owner/name). DRY_RUN=1 prints the comment instead of posting it.
set -euo pipefail

sha=${1:?usage: pr-triage.sh SHA [PR]}
pr=${2:-}
repo=${GITHUB_REPOSITORY:?set GITHUB_REPOSITORY to owner/name}
server=${GITHUB_SERVER_URL:-https://github.com}
marker='<!-- pr-triage -->'

if [ -z "$pr" ]; then
	# Fork PRs aren't listed on a workflow_run event, so look the commit up among open PRs.
	pr=$(gh pr list -R "$repo" --state open --search "$sha" --json number,headRefOid \
		--jq "map(select(.headRefOid == \"$sha\"))[0].number // empty")
fi
if [ -z "$pr" ]; then
	echo "No open pull request has $sha as its head."
	exit 0
fi

head=$(gh pr view -R "$repo" "$pr" --json headRefOid --jq .headRefOid)
if [ "$head" != "$sha" ]; then
	echo "PR #$pr has moved on to $head; its own checks will update the comment."
	exit 0
fi

# A check that ran again (a re-run, or the Commits check after a title edit) keeps only its latest run.
runs=$(gh api --paginate "repos/$repo/commits/$sha/check-runs?per_page=100" --jq '.check_runs[]' |
	jq -s 'group_by(.name) | map(max_by(.id))')
failed=$(jq -r '.[] | select(.conclusion as $c
	| ["failure", "timed_out", "cancelled", "action_required", "startup_failure"] | index($c))
	| "- [\(.name)](\(.html_url))"' <<<"$runs")
pending=$(jq '[.[] | select(.status != "completed")] | length' <<<"$runs")

existing=$(gh api --paginate "repos/$repo/issues/$pr/comments" \
	--jq ".[] | select(.user.login == \"github-actions[bot]\" and (.body | contains(\"$marker\"))) | .id" | head -n1)

if [ -n "$failed" ]; then
	body="$marker
These checks failed on $sha, and the pull request can't be merged until they pass:

$failed

Open each one to see why; the log says how to fix it. See [CONTRIBUTING.md]($server/$repo/blob/HEAD/CONTRIBUTING.md) for the commit and pull request rules. This comment updates when the checks run again."
elif [ "$pending" -gt 0 ]; then
	echo "PR #$pr: no failures yet, $pending checks still running."
	exit 0
elif [ -n "$existing" ]; then
	body="$marker
All checks pass on $sha. Thanks for fixing them."
else
	echo "PR #$pr: all checks pass."
	exit 0
fi

if [ -n "${DRY_RUN:-}" ]; then
	printf 'PR #%s, %s comment:\n%s\n' "$pr" "${existing:+existing}${existing:-new}" "$body"
elif [ -n "$existing" ]; then
	gh api -X PATCH "repos/$repo/issues/comments/$existing" -f body="$body" --silent
	echo "Updated the comment on PR #$pr."
else
	gh api "repos/$repo/issues/$pr/comments" -f body="$body" --silent
	echo "Commented on PR #$pr."
fi
