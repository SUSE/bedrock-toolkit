#!/usr/bin/env bash
# Checks that each commit in BASE..HEAD has a Conventional Commit subject and a Signed-off-by line
# for its author (bots exempt from the sign-off), and that PR_TITLE, when set, is a Conventional
# Commit subject too: squash merges use it as the commit on main.
#
#   .github/scripts/check-commits.sh origin/main HEAD
set -euo pipefail

base=${1:?usage: check-commits.sh BASE [HEAD]}
head=${2:-HEAD}
types='build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test'
pattern="^($types)(\([a-z0-9._/-]+\))?!?: [^ ].*"
failed=0

fail() {
	echo "::error::$1"
	failed=1
}

if [[ -n "${PR_TITLE:-}" ]] && ! [[ "$PR_TITLE" =~ $pattern ]]; then
	fail "PR title \"$PR_TITLE\" is not a Conventional Commit subject: type(scope)!: summary, type one of ${types//|/, }"
fi

for sha in $(git rev-list --no-merges "$base..$head"); do
	subject=$(git log -1 --format=%s "$sha")
	author=$(git log -1 --format='%an <%ae>' "$sha")
	short=${sha:0:7}
	if ! [[ "$subject" =~ $pattern ]]; then
		fail "$short \"$subject\" is not a Conventional Commit subject: type(scope)!: summary, type one of ${types//|/, }"
	fi
	if [[ "$author" == *"[bot]"* ]]; then
		continue
	fi
	if ! git log -1 --format=%B "$sha" | grep -qxF "Signed-off-by: $author"; then
		fail "$short has no \"Signed-off-by: $author\": amend it with git commit --amend -s, or sign off every commit with git rebase --signoff $base"
	fi
done

if [[ $failed == 0 ]]; then
	echo "Every commit in $base..$head is a signed-off Conventional Commit."
fi
exit $failed
