# Shared helpers for scripts/install-mac.sh and scripts/update.sh.
# shellcheck shell=bash

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RELEASE_TAG_PATTERN='v*-fork.*'
DEFAULT_BIN="$HOME/bin/slack-mcp-server"

if [ -t 1 ]; then
	BOLD=$'\033[1m'; RED=$'\033[31m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RESET=$'\033[0m'
else
	BOLD=''; RED=''; GREEN=''; YELLOW=''; RESET=''
fi

# Report unexpected failures instead of exiting silently under set -e.
trap 'status=$?; [ $status -eq 0 ] || printf "%s✗%s Stopped at line %s of %s (exit %s).\n" "$RED" "$RESET" "$LINENO" "$(basename "$0")" "$status" >&2' ERR

info() { printf '%s\n' "$*"; }
ok() { printf '%s✓%s %s\n' "$GREEN" "$RESET" "$*"; }
warn() { printf '%s!%s %s\n' "$YELLOW" "$RESET" "$*" >&2; }
die() { printf '%s✗%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }
step() { printf '\n%s%s%s\n' "$BOLD" "$*" "$RESET"; }

# ask "Question" default(Y|N) -> returns 0 for yes
ask() {
	local prompt=$1 default=${2:-N} answer hint='[y/N]'
	[ "$default" = Y ] && hint='[Y/n]'
	read -r -p "$prompt $hint " answer || answer=''
	answer=${answer:-$default}
	case "$answer" in [Yy]*) return 0 ;; *) return 1 ;; esac
}

# The binary path is remembered per clone, so update.sh rebuilds the same file.
bin_path() {
	git -C "$REPO_DIR" config --local --get slackmcp.bin 2>/dev/null || printf '%s\n' "$DEFAULT_BIN"
}

remember_bin_path() {
	git -C "$REPO_DIR" config --local slackmcp.bin "$1"
}

latest_release_tag() {
	git -C "$REPO_DIR" tag -l "$RELEASE_TAG_PATTERN" --sort=-v:refname | head -n 1
}

worktree_clean() {
	git -C "$REPO_DIR" diff --quiet && git -C "$REPO_DIR" diff --cached --quiet
}

require_go() {
	command -v go >/dev/null 2>&1 || return 1
	local need have need_major need_minor have_major have_minor
	need=$(awk '/^go /{print $2; exit}' "$REPO_DIR/go.mod")
	have=$(go env GOVERSION | sed 's/^go//')
	need_major=${need%%.*}; need_minor=$(echo "$need" | cut -d. -f2)
	have_major=${have%%.*}; have_minor=$(echo "$have" | cut -d. -f2)
	if [ "$have_major" -lt "$need_major" ] || { [ "$have_major" -eq "$need_major" ] && [ "$have_minor" -lt "$need_minor" ]; }; then
		die "Go $need or newer is needed, found $have. Update it, for example with: brew upgrade go"
	fi
	return 0
}

# build_binary PATH: builds the current checkout with the version baked in,
# into a temporary file that is then renamed over PATH, so a running server
# keeps its old binary.
build_binary() {
	local out=$1 tmp version
	mkdir -p "$(dirname "$out")"
	tmp="$out.tmp.$$"
	version=$(git -C "$REPO_DIR" describe --tags --always --dirty --match "$RELEASE_TAG_PATTERN")
	(
		cd "$REPO_DIR" || exit 1
		local pkg
		pkg=$(go list -m)
		go build -ldflags "-s -w \
			-X '$pkg/pkg/version.Version=$version' \
			-X '$pkg/pkg/version.CommitHash=$(git rev-parse HEAD)' \
			-X '$pkg/pkg/version.BuildTime=$(date -u '+%Y-%m-%dT%H:%M:%SZ')'" \
			-o "$tmp" ./cmd/slack-mcp-server
	) || { rm -f "$tmp"; die "Build failed; $out was left unchanged."; }
	mv -f "$tmp" "$out"
	ok "Built $out ($version)"
}

restart_hint() {
	info "Restart your MCP clients so they run the new binary:"
	info "  - Claude Desktop: quit with Cmd+Q and open it again"
	info "  - Codex: start a new session"
}

