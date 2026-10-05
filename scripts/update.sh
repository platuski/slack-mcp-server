#!/usr/bin/env bash
# Update this clone to the newest fork release and rebuild the binary.
#
#   ./scripts/update.sh            switch to the newest release tag and rebuild
#   ./scripts/update.sh --rebuild  rebuild even when already up to date
#   ./scripts/update.sh --yes      do not ask before leaving a branch
set -euo pipefail

# shellcheck source=lib/common.sh
source "$(dirname "$0")/lib/common.sh"

rebuild=0
assume_yes=0
for arg in "$@"; do
	case "$arg" in
		--rebuild) rebuild=1 ;;
		--yes|-y) assume_yes=1 ;;
		-h|--help) sed -n '2,7p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) die "Unknown option: $arg" ;;
	esac
done

cd "$REPO_DIR"
require_go || die "Go is not installed. Run ./scripts/install-mac.sh first."
worktree_clean || die "This clone has uncommitted changes. Commit or stash them first; update.sh does not overwrite them."

step "Checking for a new release"
git fetch --quiet --tags origin || die "Could not reach the repository. Check your network and try again."
latest=$(latest_release_tag)
[ -n "$latest" ] || die "No releases found (no $RELEASE_TAG_PATTERN tags on origin)."

current=$(git describe --tags --exact-match --match "$RELEASE_TAG_PATTERN" HEAD 2>/dev/null || git rev-parse --short HEAD)
branch=$(git symbolic-ref --short -q HEAD || true)
bin=$(bin_path)

if [ "$(git rev-parse HEAD)" = "$(git rev-parse "$latest^{commit}")" ]; then
	ok "Already on the newest release, $latest."
	if [ "$rebuild" -eq 0 ] && [ -x "$bin" ]; then
		exit 0
	fi
else
	info "Current: $current${branch:+ (branch $branch)}"
	info "Newest:  $latest"
	if git merge-base --is-ancestor HEAD "$latest" 2>/dev/null; then
		info ""
		info "Changes:"
		git log --oneline --no-decorate HEAD.."$latest" | head -n 30 | sed 's/^/  /'
	fi
	if [ -n "$branch" ] && [ "$assume_yes" -eq 0 ]; then
		warn "You are on branch '$branch'. Updating switches this clone to the release tag $latest."
		ask "Continue?" N || die "Stopped. Nothing was changed."
	fi
	git checkout --quiet --detach "$latest"
	ok "Switched to $latest"
fi

step "Building"
require_go
build_binary "$bin"

step "Done"
restart_hint
