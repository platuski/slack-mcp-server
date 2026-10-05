#!/usr/bin/env bash
# Install slack-mcp-server (fork) on macOS and add it to Claude Desktop and Codex.
#
#   ./scripts/install-mac.sh                install the newest release
#   ./scripts/install-mac.sh --current      build the current checkout instead
#   ./scripts/install-mac.sh --bin PATH     binary location (default ~/bin/slack-mcp-server)
#   ./scripts/install-mac.sh --name NAME    server name in the client configs (default slack-channels)
#   ./scripts/install-mac.sh --no-verify    skip the online token check
#
# Run it again to change the token or settings; it updates the existing entries.
set -euo pipefail

# shellcheck source=lib/common.sh
source "$(dirname "$0")/lib/common.sh"

use_current=0
verify=1
name=slack-channels
bin=$(bin_path)
while [ $# -gt 0 ]; do
	case "$1" in
		--current) use_current=1 ;;
		--no-verify) verify=0 ;;
		--bin) bin=${2:?--bin needs a path}; shift ;;
		--name) name=${2:?--name needs a name}; shift ;;
		-h|--help) sed -n '2,11p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) die "Unknown option: $1" ;;
	esac
	shift
done
case "$bin" in "~/"*) bin="$HOME/${bin#\~/}" ;; esac

CLAUDE_CONFIG="$HOME/Library/Application Support/Claude/claude_desktop_config.json"
CODEX_CONFIG="$HOME/.codex/config.toml"
CONFIGURE="$REPO_DIR/scripts/lib/configure_clients.py"

# Scopes, kept in line with pkg/provider/scopes_log.go and the README.
REQUIRED_SCOPES="channels:read channels:history groups:read groups:history users:read search:read.public search:read.private"
OPTIONAL_SCOPES="users:read.email files:read"
DM_SCOPES="im:history im:read mpim:history mpim:read search:read search:read.im search:read.mpim reactions:read"

[ "$(uname -s)" = Darwin ] || die "This script is for macOS. On other systems, follow the manual steps in the README."
cd "$REPO_DIR"

step "1. Checking tools"
command -v git >/dev/null || die "git is missing. Install the Xcode command line tools: xcode-select --install"
command -v curl >/dev/null || die "curl is missing."
python3 -c 'import json' 2>/dev/null || die "python3 is missing. Install the Xcode command line tools: xcode-select --install"
if ! require_go; then
	if command -v brew >/dev/null && ask "Go is not installed. Install it with Homebrew now?" Y; then
		brew install go
		require_go || die "Go still not found after installing; open a new terminal and run this script again."
	else
		die "Go 1.25 or newer is needed: https://go.dev/dl/ or brew install go"
	fi
fi
ok "git, curl, python3 and $(go env GOVERSION)"

step "2. Choosing the version"
if [ "$use_current" -eq 1 ]; then
	ok "Building the current checkout ($(git describe --tags --always --dirty --match "$RELEASE_TAG_PATTERN"))"
else
	git fetch --quiet --tags origin || warn "Could not fetch releases; using the local copy."
	latest=$(latest_release_tag)
	if [ -z "$latest" ]; then
		warn "No releases found; building the current checkout."
	elif [ "$(git rev-parse HEAD)" = "$(git rev-parse "$latest^{commit}")" ]; then
		ok "Already on the newest release, $latest"
	else
		worktree_clean || die "This clone has uncommitted changes. Commit or stash them, or run with --current."
		branch=$(git symbolic-ref --short -q HEAD || true)
		if [ -n "$branch" ]; then
			ask "Switch this clone from branch '$branch' to release $latest?" Y \
				|| die "Stopped. Run with --current to build the branch instead."
		fi
		git checkout --quiet --detach "$latest"
		ok "Using release $latest"
	fi
fi

step "3. Building"
build_binary "$bin"
remember_bin_path "$bin"

step "4. Slack token"
info "Use a User OAuth Token (xoxp-...) from a Slack app with these User Token Scopes:"
info "  required: $REQUIRED_SCOPES"
info "  optional: users:read.email (find people by email), files:read (read attachments)"
info "  never:    im:*, mpim:*, search:read, search:read.im, search:read.mpim, reactions:read"
existing_args=()
[ -f "$CLAUDE_CONFIG" ] && existing_args+=(--claude "$CLAUDE_CONFIG")
[ -f "$CODEX_CONFIG" ] && existing_args+=(--codex "$CODEX_CONFIG")
have_existing=0
if [ ${#existing_args[@]} -gt 0 ] && python3 "$CONFIGURE" has-token "${existing_args[@]}" --name "$name"; then
	have_existing=1
fi
if [ "$have_existing" -eq 0 ]; then
	info ""
	info "No Slack app yet? Create one from the manifest in this repo, which has exactly these scopes:"
	info "  Create New App -> From a manifest -> pick the workspace -> paste the manifest -> Create,"
	info "  then Install to Workspace and copy the User OAuth Token from OAuth & Permissions."
	if ask "Copy the manifest to your clipboard and open Slack's app page?" Y; then
		pbcopy < "$REPO_DIR/slack-app-manifest.json" && ok "Manifest copied to the clipboard"
		open "https://api.slack.com/apps?new_app=1" || warn "Could not open the browser; go to https://api.slack.com/apps"
	fi
	info ""
fi
token=''
while :; do
	if [ "$have_existing" -eq 1 ]; then
		read -r -s -p "Paste the token, or press Enter to keep the current one: " token || token=''
	else
		read -r -s -p "Paste the token (input is hidden): " token || token=''
	fi
	printf '\n'
	if [ -z "$token" ] && [ "$have_existing" -eq 1 ]; then
		ok "Keeping the current token"
		break
	fi
	case "$token" in
		xoxp-*|xoxe.xoxp-*) break ;;
		xoxb-*|xoxe.xoxb-*) warn "That is a bot token. Use the User OAuth Token (xoxp-...)." ;;
		*) warn "That does not look like a user token (xoxp-...). Try again." ;;
	esac
done

attachments=''
if [ -n "$token" ] && [ "$verify" -eq 1 ]; then
	tmpdir=$(mktemp -d)
	trap 'rm -rf "$tmpdir"' EXIT
	# The token goes to curl on stdin, so it never appears in the process list.
	printf 'header = "Authorization: Bearer %s"\n' "$token" \
		| curl -sS -K - -D "$tmpdir/headers" -o "$tmpdir/body" -X POST https://slack.com/api/auth.test \
		|| die "Could not reach Slack to check the token. Run with --no-verify to skip the check."
	result=$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print("ok" if d.get("ok") else d.get("error","unknown_error")); print(d.get("team","")); print(d.get("user",""))' "$tmpdir/body")
	status=$(sed -n 1p <<<"$result")
	[ "$status" = ok ] || die "Slack rejected the token: $status"
	ok "Token works for $(sed -n 3p <<<"$result") in $(sed -n 2p <<<"$result")"

	scopes=$(grep -i '^x-oauth-scopes:' "$tmpdir/headers" | cut -d: -f2- | tr -d ' \r' | tr ',' ' ')
	missing=''
	for s in $REQUIRED_SCOPES; do
		case " $scopes " in *" $s "*) ;; *) missing="$missing $s" ;; esac
	done
	[ -z "$missing" ] || die "The token is missing required scopes:$missing. Add them to the Slack app, reinstall it and run this script again."
	dm=''
	for s in $DM_SCOPES; do
		case " $scopes " in *" $s "*) dm="$dm $s" ;; esac
	done
	if [ -n "$dm" ]; then
		warn "The token has scopes that can read DMs or group DMs:$dm"
		warn "The server still blocks DMs, but Slack no longer does. Remove them from the Slack app, revoke its tokens and reinstall."
		ask "Continue anyway?" N || die "Stopped. Nothing was written to your client configs."
	fi
	case " $scopes " in *" files:read "*) attachments=true ;; esac
	case " $scopes " in *" users:read.email "*) ;; *) warn "Without users:read.email, users_search cannot look people up by email." ;; esac
	[ -n "$attachments" ] || info "  (no files:read: attachment reading stays off)"
elif [ -n "$token" ]; then
	warn "Skipping the token check (--no-verify)."
	ask "Does the token have files:read (read attachments)?" N && attachments=true
fi

step "5. Adding the server to your clients"
write_args=()
if [ -d "$(dirname "$CLAUDE_CONFIG")" ] && ask "Add '$name' to Claude Desktop?" Y; then
	write_args+=(--claude "$CLAUDE_CONFIG")
fi
if [ -d "$HOME/.codex" ] && ask "Add '$name' to Codex?" Y; then
	write_args+=(--codex "$CODEX_CONFIG")
fi
if [ ${#write_args[@]} -eq 0 ]; then
	warn "No client selected. Add the server by hand, see the README."
else
	(
		# Exported, not passed as arguments, so the token stays out of the process list.
		export SLACK_MCP_INSTALL_TOKEN="$token"
		export SLACK_MCP_INSTALL_CHANNEL_TYPES=public_channel,private_channel
		export SLACK_MCP_INSTALL_READ_ONLY=true
		if [ -n "$token" ]; then
			# The attachment setting follows the new token's scopes; with the
			# current token kept, it stays as it is.
			export SLACK_MCP_INSTALL_ATTACHMENT_TOOL="$attachments"
		fi
		python3 "$CONFIGURE" write "${write_args[@]}" --name "$name" --bin "$bin"
	)
	if [ -n "$token" ]; then
		ok "Settings: public and private channels only, read-only, attachments ${attachments:+on}${attachments:-off}"
	else
		ok "Settings: public and private channels only, read-only; attachment setting unchanged"
	fi
fi
unset token

step "Done"
claude_running() { [ "$(osascript -e 'application "Claude" is running' 2>/dev/null)" = true ]; }
if [ ${#write_args[@]} -gt 0 ] && claude_running && ask "Restart Claude Desktop now? It closes all its windows." N; then
	osascript -e 'quit app "Claude"' >/dev/null 2>&1 || true
	for _ in $(seq 1 30); do claude_running || break; sleep 1; done
	if claude_running; then
		warn "Claude is still running; quit it with Cmd+Q and open it again."
	else
		open -a Claude && ok "Claude Desktop restarted"
	fi
	info "Codex: start a new session to use the new server."
else
	restart_hint
fi
info ""
info "After the restart, the server log should list the token's scopes with no DM warning:"
info "  grep -a 'OAuth token scopes' ~/Library/Logs/Claude/mcp-server-$name.log | tail -1"
info ""
info "Update later with: ./scripts/update.sh"
