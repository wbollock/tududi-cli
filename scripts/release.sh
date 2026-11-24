#!/usr/bin/env bash
# Tags a release and pushes it. The release workflow then builds the binaries.
#
# usage: scripts/release.sh <version> [commit message]
#
# With a commit message, changes to tracked files are committed first.
set -euo pipefail

version=${1:?usage: scripts/release.sh <version> [commit message]}
message=${2:-}

if [[ ! $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
	echo "version must look like v1.2.3, got $version" >&2
	exit 1
fi

cd "$(dirname "$0")/.."

if [[ $(git branch --show-current) != main ]]; then
	echo "release from main, not $(git branch --show-current)" >&2
	exit 1
fi

if git rev-parse -q --verify "refs/tags/$version" >/dev/null; then
	echo "tag $version already exists" >&2
	exit 1
fi

go test ./...

if [[ -n $message ]]; then
	git add -u
	git commit -m "$message" \
		-m "AI-generated content prepared on Will's behalf." \
		-m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
fi

if [[ -n $(git status --porcelain --untracked-files=no) ]]; then
	echo "uncommitted changes, commit them or pass a commit message:" >&2
	git status --short --untracked-files=no >&2
	exit 1
fi

git push origin main
git tag "$version"
git push origin "$version"

echo "pushed $version, watching the release workflow"
sleep 5
GH_HOST=github.com gh run watch --exit-status \
	"$(GH_HOST=github.com gh run list --workflow release.yaml --limit 1 --json databaseId -q '.[0].databaseId')"
