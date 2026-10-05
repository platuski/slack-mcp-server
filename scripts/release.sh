#!/usr/bin/env bash
# Tag and push the next fork release (maintainers).
#
#   ./scripts/release.sh            test, tag and push the next release of master,
#                                   then create a GitHub Release for it
#   ./scripts/release.sh --dry-run  show the next tag and changes, change nothing
#
# Releases are named after the upstream release the fork is based on plus a
# fork number, for example v1.3.0-fork.2. install-mac.sh and update.sh only
# use these tags.
set -euo pipefail

# shellcheck source=lib/common.sh
source "$(dirname "$0")/lib/common.sh"

dry_run=0
for arg in "$@"; do
	case "$arg" in
		--dry-run|-n) dry_run=1 ;;
		-h|--help) sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) die "Unknown option: $arg" ;;
	esac
done

cd "$REPO_DIR"

step "Checking master"
[ "$(git symbolic-ref --short -q HEAD || true)" = master ] || die "Check out master first."
worktree_clean || die "master has uncommitted changes."
git fetch --quiet --tags origin
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/master)" ] \
	|| die "Local master differs from origin/master. Push or pull first, so the tag points at a published commit."
if git remote get-url upstream >/dev/null 2>&1; then
	git fetch --quiet --tags upstream || warn "Could not fetch upstream tags; using the local ones."
fi

base=$(git describe --tags --abbrev=0 --match 'v[0-9]*' --exclude '*-fork.*' HEAD 2>/dev/null || true)
[ -n "$base" ] || die "No upstream release tag found below master. Add the upstream remote: git remote add upstream https://github.com/korotovsky/slack-mcp-server.git"

previous=$(latest_release_tag)
if [ -n "$previous" ] && [ "$(git rev-parse "$previous^{commit}")" = "$(git rev-parse HEAD)" ]; then
	die "master is already released as $previous."
fi
last_n=$(git tag -l "$base-fork.*" | sed "s/^$base-fork\.//" | { grep -E '^[0-9]+$' || true; } | sort -n | tail -n 1)
tag="$base-fork.$(( ${last_n:-0} + 1 ))"
ok "Next release: $tag (upstream $base${previous:+, previous $previous})"

# List fork commits only: leave out commits that are part of upstream, and
# after an upstream sync, rebased commits that the previous release had.
exclude=()
if git rev-parse -q --verify refs/remotes/upstream/master >/dev/null; then
	exclude=(^refs/remotes/upstream/master)
fi
if [ -n "$previous" ] && git merge-base --is-ancestor "$previous" HEAD; then
	changes=$(git log --no-merges --format='• %s' "$previous..HEAD" ${exclude[@]+"${exclude[@]}"})
elif [ -n "$previous" ]; then
	changes=$(git log --no-merges --cherry-pick --right-only --format='• %s' "$previous...HEAD" ${exclude[@]+"${exclude[@]}"})
else
	changes=$(git log --no-merges --format='• %s' "$base..HEAD" ${exclude[@]+"${exclude[@]}"})
fi
if [ -n "$previous" ] && [ "${previous%-fork.*}" != "$base" ]; then
	changes="• Synced with upstream $base${changes:+
$changes}"
fi
[ -n "$changes" ] || die "No commits since $previous."
info ""
info "Changes:"
printf '%s\n' "$changes" | sed 's/^/  /'

if [ "$dry_run" -eq 1 ]; then
	info ""
	info "Dry run: nothing tagged or pushed."
	exit 0
fi

step "Running checks"
require_go || die "Go is not installed."
go vet ./... || die "go vet failed; nothing was tagged."
go test ./... -run '.*Unit.*' -count=1 >/dev/null || die "Unit tests failed (go test ./... -run '.*Unit.*'); nothing was tagged."
ok "go vet and unit tests pass"

ask "Tag $tag and push it to origin?" N || die "Stopped. Nothing was tagged."
git tag -a "$tag" -m "$tag" -m "$changes"
git push --quiet origin "$tag"
ok "Pushed $tag"

step "GitHub Release"
# The fork's repo, from origin; gh would default to upstream in a fork.
repo=$(git remote get-url origin | sed -E 's#^https://([^@/]+@)?github\.com/##; s#^(ssh://)?git@github\.com[:/]##; s#\.git$##; s#/$##')
notes=$(cat <<EOF
## Changes

$(printf '%s\n' "$changes" | sed 's/^• /- /')

## Install

\`\`\`bash
git clone https://github.com/$repo.git
cd $(basename "$repo")
./scripts/install-mac.sh
\`\`\`

## Update

\`\`\`bash
./scripts/update.sh
\`\`\`

Then restart Claude Desktop (Cmd+Q) and start a new Codex session. Setup details are in the [README](https://github.com/$repo#installing-this-fork).
EOF
)
if ! printf '%s' "$repo" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$'; then
	warn "origin is not a GitHub repository; no GitHub Release created."
	repo=''
elif ! command -v gh >/dev/null || ! gh auth status >/dev/null 2>&1; then
	warn "gh is not installed or not logged in; create the release with:"
	info "  gh release create $tag --repo $repo --title $tag --notes-file <notes> --verify-tag"
elif printf '%s\n' "$notes" | gh release create "$tag" --repo "$repo" --title "$tag" --notes-file - --verify-tag >/dev/null; then
	ok "Created https://github.com/$repo/releases/tag/$tag"
	info "  People who watch the repo for releases (Watch -> Custom -> Releases) are notified."
else
	warn "Creating the GitHub Release failed; the tag is pushed. Retry with: gh release create $tag --repo $repo"
fi

message=$(cat <<EOF
*slack-mcp-server $tag is out*
$changes

Update with \`./scripts/update.sh\` in your clone, then restart Claude (Cmd+Q) and start a new Codex session.${repo:+
Release notes: https://github.com/$repo/releases/tag/$tag}
EOF
)
step "Announcement for Slack"
printf '%s\n' "$message"
if command -v pbcopy >/dev/null && ask "Copy it to the clipboard?" Y; then
	printf '%s\n' "$message" | pbcopy && ok "Copied"
fi
