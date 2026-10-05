#!/usr/bin/env python3
"""Add or update the slack-mcp-server entry in Claude Desktop and Codex configs.

Used by scripts/install-mac.sh. The token is read from the environment
(SLACK_MCP_INSTALL_TOKEN) and never printed. Other servers in the configs are
left as they are; extra env settings of an existing entry are kept. Each file
is backed up next to itself before it is changed.

  configure_clients.py has-token --claude PATH --codex PATH --name NAME
  configure_clients.py write [--claude PATH] [--codex PATH] --name NAME --bin BIN
"""

import argparse
import json
import os
import re
import shutil
import sys
import time

try:
    import tomllib  # Python 3.11+
except ImportError:  # pragma: no cover
    tomllib = None

TOKEN_KEY = "SLACK_MCP_XOXP_TOKEN"


def backup(path):
    if os.path.exists(path):
        dest = f"{path}.bak-{time.strftime('%Y%m%d-%H%M%S')}"
        shutil.copy2(path, dest)
        return dest
    return None


def write_private(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    mode = os.stat(path).st_mode & 0o777 if os.path.exists(path) else 0o600
    tmp = f"{path}.tmp.{os.getpid()}"
    with open(tmp, "w", encoding="utf-8") as f:
        f.write(text)
    os.chmod(tmp, mode)
    os.replace(tmp, path)


# --- Claude Desktop (JSON) -------------------------------------------------

def claude_load(path):
    if not os.path.exists(path):
        return {}
    with open(path, encoding="utf-8") as f:
        text = f.read()
    return json.loads(text) if text.strip() else {}


def claude_env(path, name):
    server = claude_load(path).get("mcpServers", {}).get(name, {})
    return dict(server.get("env", {}))


def merge_env(old, changes):
    """Apply changes to old; a value of None removes the key."""
    merged = dict(old)
    for key, value in changes.items():
        if value is None:
            merged.pop(key, None)
        else:
            merged[key] = value
    return merged


def claude_write(path, name, bin_path, env):
    data = claude_load(path)
    servers = data.setdefault("mcpServers", {})
    servers[name] = {
        "command": bin_path,
        "args": ["--transport", "stdio"],
        "env": merge_env(servers.get(name, {}).get("env", {}), env),
    }
    saved = backup(path)
    write_private(path, json.dumps(data, indent=2, ensure_ascii=False) + "\n")
    return saved


# --- Codex (TOML) ----------------------------------------------------------

def codex_header_re(name):
    return re.compile(r"^\s*\[\s*mcp_servers\.%s(\.[^\]]+)?\s*\]\s*(#.*)?$" % re.escape(name))


HEADER_RE = re.compile(r"^\s*\[")


def codex_env(path, name):
    if not os.path.exists(path) or tomllib is None:
        return {}
    with open(path, "rb") as f:
        data = tomllib.load(f)
    return dict(data.get("mcp_servers", {}).get(name, {}).get("env", {}))


def toml_str(value):
    return json.dumps(str(value), ensure_ascii=True)


def codex_write(path, name, bin_path, env):
    merged = merge_env(codex_env(path, name), env)
    text = ""
    if os.path.exists(path):
        with open(path, encoding="utf-8") as f:
            text = f.read()

    # Drop the existing [mcp_servers.NAME] and [mcp_servers.NAME.*] tables.
    own = codex_header_re(name)
    kept, skipping = [], False
    for line in text.splitlines():
        if own.match(line):
            skipping = True
            continue
        if skipping and HEADER_RE.match(line):
            skipping = False
        if not skipping:
            kept.append(line)
    body = "\n".join(kept).rstrip()

    block = [
        f"[mcp_servers.{name}]",
        f"command = {toml_str(bin_path)}",
        'args = ["--transport", "stdio"]',
        "",
        f"[mcp_servers.{name}.env]",
    ]
    block += [f"{key} = {toml_str(value)}" for key, value in merged.items()]
    new_text = (body + "\n\n" if body else "") + "\n".join(block) + "\n"

    saved = backup(path)
    write_private(path, new_text)
    return saved


# --- CLI -------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=["has-token", "write"])
    parser.add_argument("--claude")
    parser.add_argument("--codex")
    parser.add_argument("--name", required=True)
    parser.add_argument("--bin")
    args = parser.parse_args()

    if args.command == "has-token":
        # Exit 0 when an existing entry already has a token.
        for path, reader in ((args.claude, claude_env), (args.codex, codex_env)):
            if path and reader(path, args.name).get(TOKEN_KEY):
                return 0
        return 1

    token = os.environ.get("SLACK_MCP_INSTALL_TOKEN", "")
    if not token:
        # Keep the token an existing entry already has.
        for path, reader in ((args.claude, claude_env), (args.codex, codex_env)):
            if path:
                token = reader(path, args.name).get(TOKEN_KEY, "")
                if token:
                    break
    if not token:
        print("No Slack token given and none found in an existing entry.", file=sys.stderr)
        return 2

    # SLACK_MCP_INSTALL_<SETTING>: a value sets SLACK_MCP_<SETTING>, an empty
    # value removes it, an unset variable leaves it as it is.
    env = {TOKEN_KEY: token}
    for setting in ("CHANNEL_TYPES", "READ_ONLY", "ATTACHMENT_TOOL"):
        value = os.environ.get("SLACK_MCP_INSTALL_" + setting)
        if value is not None:
            env["SLACK_MCP_" + setting] = value or None

    for path, writer, label in (
        (args.claude, claude_write, "Claude Desktop"),
        (args.codex, codex_write, "Codex"),
    ):
        if path:
            saved = writer(path, args.name, args.bin, env)
            print(f"{label}: updated {path}" + (f" (backup: {saved})" if saved else ""))
    return 0


if __name__ == "__main__":
    sys.exit(main())
