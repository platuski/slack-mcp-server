# Slack MCP Server (platuski fork)

> [!NOTE]
> **This is a fork of [korotovsky/slack-mcp-server](https://github.com/korotovsky/slack-mcp-server).**
> It adds the features described in [Fork changes](#fork-changes) below. The [Upstream README](#upstream-readme) section after it is the original documentation, unchanged apart from environment variable rows marked *(fork)*.

## Fork changes

Based on upstream commit [`b88c0de`](https://github.com/korotovsky/slack-mcp-server/commit/b88c0de).

### `SLACK_MCP_CHANNEL_TYPES`: restrict which conversation types the server exposes

Upstream always exposes public channels, private channels, DMs and group DMs. This fork adds a comma separated allowlist:

| Value             | Conversation type |
|-------------------|-------------------|
| `public_channel`  | Public channels   |
| `private_channel` | Private channels  |
| `im`              | Direct messages   |
| `mpim`            | Group DMs         |

Unset or empty keeps upstream behavior (all four types). An unknown value stops the server at startup.

Example: channels only, no DM or group DM access:

```json
"env": {
  "SLACK_MCP_CHANNEL_TYPES": "public_channel,private_channel"
}
```

Excluded types are:

- dropped from the channels cache, including entries loaded from a cache file written by an earlier unrestricted run;
- filtered out of `channels_list`, `channels_me`, `conversations_unreads` and `conversations_search_messages`;
- rejected by `conversations_history`, `conversations_replies`, `conversations_mark`, `conversations_join`, `conversations_leave`, `conversations_add_message`, `reactions_add` and `reactions_remove`. The check uses the cache first and falls back to `conversations.info`; if the type cannot be determined, the request is rejected.
- rejected by `attachment_get_data` unless the file is also shared in a conversation of an enabled type (see [Attachments](#attachments-with-filesread)).

For the `channel_types` parameter of `channels_list` and `channels_me`, excluded types are dropped and logged as a warning when at least one allowed type is also requested. A request for excluded types only returns a `conversation type is not allowed` error naming the requested and the enabled types. When no usable type is given, the default `public_channel,private_channel` is limited to the enabled types.

On startup the server logs `Conversation types restricted` with the allowed types.

Known limits:

- With `search.messages`, search filtering happens after Slack returns a page, so a page can hold fewer results than `limit`.
- The `channels_list` and `channels_me` tool descriptions still list `im` and `mpim`.

### DM-safe message search with granular Slack scopes

Upstream's `conversations_search_messages` calls `search.messages`, which needs the `search:read` scope. That scope covers every conversation the user can see, including DMs and group DMs, and `SLACK_MCP_CHANNEL_TYPES` can only filter the results afterwards.

This fork can search through Slack's [Real-time Search API](https://docs.slack.dev/apis/web-api/real-time-search-api) (`assistant.search.context`) instead. It uses one scope per conversation type, so Slack itself refuses DM and group DM search when those scopes are not granted.

#### Setup for channel search without DM access

1. In your Slack app ([api.slack.com/apps](https://api.slack.com/apps)), open **OAuth & Permissions** and add these **User Token Scopes**:
   - `search:read.public`
   - `search:read.private`

   Do not add `search:read`, `search:read.im` or `search:read.mpim`. With `search:read` the server keeps using `search.messages`.
2. Reinstall the app to the workspace (Slack shows a banner after a scope change). The token only gets the new scopes after the reinstall. If the User OAuth Token value on that page changed, update it in your MCP client config.
3. Restrict the server to channels:

   ```json
   "env": {
     "SLACK_MCP_XOXP_TOKEN": "xoxp-...",
     "SLACK_MCP_CHANNEL_TYPES": "public_channel,private_channel"
   }
   ```
4. Restart the MCP client. On startup the server logs `Message search uses Real-time Search (assistant.search.context)` with the searchable conversation types.

The scopes used by the rest of the server stay the same, for example `channels:read`, `groups:read`, `channels:history`, `groups:history` and `users:read`.

#### Optional read scopes

These can be added without opening DM access:

- `users:read.email`: `users_search` with an email address returns the user with exactly that email (from the users cache, or `users.lookupByEmail` when the cache does not have it yet). Without the scope, Slack omits email addresses and email queries return an error naming the scope. Slack Connect users from other organizations are not found by email.
- `files:read`: `attachment_get_data` returns files (also set `SLACK_MCP_ATTACHMENT_TOOL=true`); without the scope it returns an error naming it. Slack does not limit this scope by conversation type: a file shared only in a DM can be fetched by its ID. The server guards it, see [Attachments](#attachments-with-filesread).

#### Scopes to never add for DM-safe use

- `im:read`, `im:history`, `mpim:read`, `mpim:history` and other `im:*` / `mpim:*` scopes
- `search:read`, `search:read.im`, `search:read.mpim`
- `reactions:read`: `reactions.list` returns DM messages you reacted to
- `canvases:read`, `lists:read`

#### How the search API is chosen

At startup the server reads the token's scopes from the `X-OAuth-Scopes` header of `auth.test`.

| Token | Search API |
|-------|------------|
| User OAuth token (`xoxp`) with `search:read.public`, `.private`, `.mpim` or `.im` and without `search:read` | `assistant.search.context` |
| User OAuth token with `search:read`, without search scopes, or whose scopes cannot be read | `search.messages` (upstream) |
| Browser session tokens (`xoxc`/`xoxd`) | `search.messages` (upstream) |
| Bot token (`xoxb`) | no search tool (upstream) |

#### Behavior with Real-time Search

- Slack is asked only for conversation types that are both enabled by `SLACK_MCP_CHANNEL_TYPES` and covered by a granted search scope.
- Every result is checked again: results from DMs, from group DMs or from a conversation of a type that is not enabled are dropped. The type comes from the channels cache, or from `conversations.info` for channels the cache does not hold (archived channels, channels created later, `--no-cache`); if it cannot be determined, the result is dropped. DM IDs are never looked up.
- `search_query` must contain search text. The `in:`, `from:`, `before:`, `after:`, `on:`, `during:` and `is:thread` modifiers are taken out of the query and applied as API parameters or local filters. Dates are whole UTC days, and `after:` excludes the given day as in Slack's own modifiers.
- `filter_users_with` and `with:` are rejected: the API has no participant filter. `filter_in_im_or_mpim` is rejected unless `im` or `mpim` is both enabled and covered by a scope. Filters that cannot be used are left out of the tool schema.
- Slack returns at most 20 results per page. The server fetches up to 10 pages to fill `limit` (at most 100) and always uses keyword search (`disable_semantic_search`). The author, channel and threads-only filters are also checked locally, so a page can hold fewer results than `limit`; if all fetched results were filtered out, the response holds only a cursor to continue.
- Errors name the fix: `missing_scope` names the scope to add, and `not_allowed_token_type`, `feature_not_enabled` and `assistant_search_context_disabled` get their own messages.

With `search.messages`, `filter_in_im_or_mpim` and `filter_users_with` are also rejected and hidden when neither `im` nor `mpim` is enabled. Unrestricted configurations keep the upstream schema and behavior.

#### Limits

Checked against a live workspace with `public_channel,private_channel` and the two scopes above: a phrase that exists only in a DM returned no results, and Slack returned no DM result that the server had to drop.

- Public channel results do not require channel membership; search also covers public channels the user has not joined (as Slack documents).
- Slack Connect channels: a shared channel the user is a member of was searchable, with and without `filter_in_channel`. Other Slack Connect setups are untested.
- Keyword search works without a Slack AI Search plan. Semantic search is never requested. Very common words (for example `the`) return no results on their own.
- Real-time Search must be available to the app: Slack offers it to internal apps and to apps published in the Slack Marketplace.
- Every tool call's arguments, including `search_query`, are logged at info level by the upstream request logger.

The request client and parts of the result handling are based on [korotovsky/slack-mcp-server#328](https://github.com/korotovsky/slack-mcp-server/pull/328) by Jason George.

### Attachments with `files:read`

When `SLACK_MCP_CHANNEL_TYPES` excludes any type, `attachment_get_data` checks where a file is shared before downloading it. `files.info` lists the conversations (`channels`, `groups`, `ims` and the `shares.public` / `shares.private` maps); the file is returned only when at least one of them is in the channels cache and of an enabled type. A file shared only in DMs or group DMs, a file without shares, and a file whose conversations are not in the cache (for example archived channels) are rejected with a generic `conversation type is not allowed` error that does not say where the file is shared. Without a restriction, files are returned as upstream.

Downloads only go to `https://files.slack.com` (`files.slack-gov.com` with `SLACK_MCP_GOVSLACK=true`). A redirect to any other host is refused instead of followed, so the token is never sent elsewhere, and downloads are capped at 5 MB.

### `SLACK_MCP_READ_ONLY`: register no write tools

With `SLACK_MCP_READ_ONLY=true` the server does not register tools that change Slack state, even when `SLACK_MCP_ENABLED_TOOLS` or a tool's own variable enables them: `conversations_add_message`, `reactions_add`, `reactions_remove`, `conversations_mark`, `conversations_join`, `conversations_leave`, `usergroups_create`, `usergroups_update`, `usergroups_users_update`, `saved_update` and `saved_clear_completed`. `conversations_unreads` stays available; it does not mark messages as read. Unset or `false` keeps upstream behavior.

### Installing this fork

The upstream install options (the `npx @korotovsky/slack-mcp-server` package, the `ghcr.io/korotovsky/slack-mcp-server` Docker image and upstream release binaries) install **upstream's build without these features**. Build from this repository instead (Go 1.25 or newer):

```bash
git clone https://github.com/platuski/slack-mcp-server.git
cd slack-mcp-server
go build -o slack-mcp-server ./cmd/slack-mcp-server
```

Point your MCP client's `command` at the resulting binary. Authentication and all other configuration follow the upstream documentation below.

### Syncing with upstream

```bash
git remote add upstream https://github.com/korotovsky/slack-mcp-server.git
git fetch upstream
git rebase upstream/master
```

---

# Upstream README

> [!NOTE]
> Everything below is the original README of [korotovsky/slack-mcp-server](https://github.com/korotovsky/slack-mcp-server) at commit `b88c0de`. Requests for stars, issues and support refer to the upstream project. Install commands below install upstream's build; see [Installing this fork](#installing-this-fork) to get `SLACK_MCP_CHANNEL_TYPES`.

[![Trust Score](https://archestra.ai/mcp-catalog/api/badge/quality/korotovsky/slack-mcp-server)](https://archestra.ai/mcp-catalog/korotovsky__slack-mcp-server)

Model Context Protocol (MCP) server for Slack Workspaces. The most powerful MCP Slack server — supports Stdio, SSE and HTTP transports, proxy settings, DMs, Group DMs, Smart History fetch (by date or count), may work via OAuth or in complete stealth mode with no permissions and scopes in Workspace 😏.

> [!IMPORTANT]  
> We need your support! Each month, over 30,000 engineers visit this repository, and more than 9,000 are already using it.
> 
> If you appreciate the work our [contributors](https://github.com/korotovsky/slack-mcp-server/graphs/contributors) have put into this project, please consider giving the repository a star.

This feature-rich Slack MCP Server has:
- **Stealth and OAuth Modes**: Run the server without requiring additional permissions or bot installations (stealth mode), or use secure OAuth tokens for access without needing to refresh or extract tokens from the browser (OAuth mode).
- **Enterprise Workspaces Support**: Possibility to integrate with Enterprise Slack setups.
- **Channel and Thread Support with `#Name` `@Lookup`**: Fetch messages from channels and threads, including activity messages, and retrieve channels using their names (e.g., #general) as well as their IDs.
- **Smart History**: Fetch messages with pagination by date (d1, 7d, 1m) or message count.
- **Unread Messages**: Get all unread messages across channels efficiently with priority sorting (DMs > partner channels > internal), @mention filtering, and mark-as-read support.
- **Search Messages**: Search messages in channels, threads, and DMs using various filters like date, user, and content.
- **Safe Message Posting**: The `conversations_add_message` tool is disabled by default for safety. Enable it via an environment variable, with optional channel restrictions.
- **DM and Group DM support**: Retrieve direct messages and group direct messages.
- **Embedded user information**: Embed user information in messages, for better context.
- **Cache support**: Cache users and channels for faster access.
- **Stdio/SSE/HTTP Transports & Proxy Support**: Use the server with any MCP client that supports Stdio, SSE or HTTP transports, and configure it to route outgoing requests through a proxy if needed.

### Analytics Demo

![Analytics](images/feature-1.gif)

### Add Message Demo

![Add Message](images/feature-2.gif)

## Tools

### 1. conversations_history:
Get messages from the channel (or DM) by channel_id, the last row/column in the response is used as 'cursor' parameter for pagination if not empty
- **Parameters:**
  - `channel_id` (string, required):     - `channel_id` (string): ID of the channel in format Cxxxxxxxxxx or its name starting with `#...` or `@...` aka `#general` or `@username_dm`.
  - `include_activity_messages` (boolean, default: false): If true, the response will include activity messages such as `channel_join` or `channel_leave`. Default is boolean false.
  - `cursor` (string, optional): Cursor for pagination. Use the value of the last row and column in the response as next_cursor field returned from the previous request.
  - `limit` (string, default: "1d"): Limit of messages to fetch in format of maximum ranges of time (e.g. 1d - 1 day, 1w - 1 week, 30d - 30 days, 90d - 90 days which is a default limit for free tier history) or number of messages (e.g. 50). Must be empty when 'cursor' is provided.

### 2. conversations_replies:
Get a thread of messages posted to a conversation by channelID and `thread_ts`, the last row/column in the response is used as `cursor` parameter for pagination if not empty.
- **Parameters:**
  - `channel_id` (string, required): ID of the channel in format `Cxxxxxxxxxx` or its name starting with `#...` or `@...` aka `#general` or `@username_dm`.
  - `thread_ts` (string, required): Unique identifier of either a thread’s parent message or a message in the thread. ts must be the timestamp in format `1234567890.123456` of an existing message with 0 or more replies.
  - `include_activity_messages` (boolean, default: false): If true, the response will include activity messages such as 'channel_join' or 'channel_leave'. Default is boolean false.
  - `cursor` (string, optional): Cursor for pagination. Use the value of the last row and column in the response as next_cursor field returned from the previous request.
  - `limit` (string, default: "1d"): Limit of messages to fetch in format of maximum ranges of time (e.g. 1d - 1 day, 1w - 1 week, 30d - 30 days, 90d - 90 days which is a default limit for free tier history) or number of messages (e.g. 50). Must be empty when 'cursor' is provided.

### 3. conversations_add_message
Add a message to a public channel, private channel, or direct message (DM, or IM) conversation by channel_id and thread_ts.

> **Note:** Posting messages is disabled by default for safety. To enable, set the `SLACK_MCP_ADD_MESSAGE_TOOL` environment variable. If set to a comma-separated list of channel IDs, posting is enabled only for those specific channels. See the Environment Variables section below for details.

- **Parameters:**
  - `channel_id` (string, required): ID of the channel in format `Cxxxxxxxxxx` or its name starting with `#...` or `@...` aka `#general` or `@username_dm`.
  - `thread_ts` (string, optional): Unique identifier of either a thread’s parent message or a message in the thread_ts must be the timestamp in format `1234567890.123456` of an existing message with 0 or more replies. Optional, if not provided the message will be added to the channel itself, otherwise it will be added to the thread.
  - `payload` (string, required): Message payload in specified content_type format. Example: 'Hello, world!' for text/plain or '# Hello, world!' for text/markdown.
  - `content_type` (string, default: "text/markdown"): Content type of the message. Default is 'text/markdown'. Allowed values: 'text/markdown', 'text/plain'.

### 4. conversations_search_messages
Search messages in a public channel, private channel, or direct message (DM, or IM) conversation using filters. All filters are optional, if not provided then search_query is required.

> **Note**: This tool is not available when using bot tokens (`xoxb-*`). Bot tokens cannot use the `search.messages` API.
- **Parameters:**
  - `search_query` (string, optional): Search query to filter messages. Example: 'marketing report' or full URL of Slack message e.g. 'https://slack.com/archives/C1234567890/p1234567890123456', then the tool will return a single message matching given URL, herewith all other parameters will be ignored.
  - `filter_in_channel` (string, optional): Filter messages in a specific channel by its ID or name. Example: `C1234567890` or `#general`. If not provided, all channels will be searched.
  - `filter_in_im_or_mpim` (string, optional): Filter messages in a direct message (DM) or multi-person direct message (MPIM) conversation by its ID or name. Example: `D1234567890` or `@username_dm`. If not provided, all DMs and MPIMs will be searched.
  - `filter_users_with` (string, optional): Filter messages with a specific user by their ID or display name in threads and DMs. Example: `U1234567890` or `@username`. If not provided, all threads and DMs will be searched.
  - `filter_users_from` (string, optional): Filter messages from a specific user by their ID or display name. Example: `U1234567890` or `@username`. If not provided, all users will be searched.
  - `filter_date_before` (string, optional): Filter messages sent before a specific date in format `YYYY-MM-DD`. Example: `2023-10-01`, `July`, `Yesterday` or `Today`. If not provided, all dates will be searched.
  - `filter_date_after` (string, optional): Filter messages sent after a specific date in format `YYYY-MM-DD`. Example: `2023-10-01`, `July`, `Yesterday` or `Today`. If not provided, all dates will be searched.
  - `filter_date_on` (string, optional): Filter messages sent on a specific date in format `YYYY-MM-DD`. Example: `2023-10-01`, `July`, `Yesterday` or `Today`. If not provided, all dates will be searched.
  - `filter_date_during` (string, optional): Filter messages sent during a specific period in format `YYYY-MM-DD`. Example: `July`, `Yesterday` or `Today`. If not provided, all dates will be searched.
  - `filter_threads_only` (boolean, default: false): If true, the response will include only messages from threads. Default is boolean false.
  - `cursor` (string, default: ""): Cursor for pagination. Use the value of the last row and column in the response as next_cursor field returned from the previous request.
  - `limit` (number, default: 20): The maximum number of items to return. Must be an integer between 1 and 100.

### 5. channels_list:
Get list of channels
- **Parameters:**
  - `channel_types` (string, required): Comma-separated channel types. Allowed values: `mpim`, `im`, `public_channel`, `private_channel`. Example: `public_channel,private_channel,im`
  - `sort` (string, optional): Type of sorting. Allowed values: `popularity` - sort by number of members/participants in each channel.
  - `limit` (number, default: 100): The maximum number of items to return. Must be an integer between 1 and 1000 (maximum 999).
  - `cursor` (string, optional): Cursor for pagination. Use the value of the last row and column in the response as next_cursor field returned from the previous request.

### 6. reactions_add:
Add an emoji reaction to a message in a public channel, private channel, or direct message (DM, or IM) conversation.

> **Note:** Adding reactions is disabled by default for safety. To enable, set the `SLACK_MCP_REACTION_TOOL` environment variable. If set to a comma-separated list of channel IDs, reactions are enabled only for those specific channels. See the Environment Variables section below for details.

- **Parameters:**
  - `channel_id` (string, required): ID of the channel in format `Cxxxxxxxxxx` or its name starting with `#...` or `@...` aka `#general` or `@username_dm`.
  - `timestamp` (string, required): Timestamp of the message to add reaction to, in format `1234567890.123456`.
  - `emoji` (string, required): The name of the emoji to add as a reaction (without colons). Example: `thumbsup`, `heart`, `rocket`.

### 7. reactions_remove:
Remove an emoji reaction from a message in a public channel, private channel, or direct message (DM, or IM) conversation.

> **Note:** Removing reactions follows the same permission model as `reactions_add`. To enable, set the `SLACK_MCP_REACTION_TOOL` environment variable.

- **Parameters:**
  - `channel_id` (string, required): ID of the channel in format `Cxxxxxxxxxx` or its name starting with `#...` or `@...` aka `#general` or `@username_dm`.
  - `timestamp` (string, required): Timestamp of the message to remove reaction from, in format `1234567890.123456`.
  - `emoji` (string, required): The name of the emoji to remove as a reaction (without colons). Example: `thumbsup`, `heart`, `rocket`.

### 8. users_search:
Search for users by name, email, or display name. Returns user details and DM channel ID if available.

> **Note:** For OAuth tokens (`xoxp`/`xoxb`), this tool searches the local users cache using pattern matching. For browser session tokens (`xoxc`/`xoxd`), it uses the Slack edge API for real-time search.

- **Parameters:**
  - `query` (string, required): Search query - matches against real name, display name, username, or email.
  - `limit` (number, default: 10): Maximum number of results to return (1-100).

- **Returns:** CSV with fields:
  - `UserID`: User ID (e.g., `U1234567890`)
  - `UserName`: Slack username
  - `RealName`: User's real name
  - `DisplayName`: User's display name
  - `Email`: User's email address
  - `Title`: User's job title
  - `DMChannelID`: DM channel ID if available in cache (for quick messaging)

### 9. usergroups_list:
List all user groups (subteams) in the workspace.

- **Parameters:**
  - `include_users` (boolean, default: false): Include list of user IDs in each group.
  - `include_count` (boolean, default: true): Include user count for each group.
  - `include_disabled` (boolean, default: false): Include disabled/archived groups.

- **Returns:** CSV with fields: id, name, handle, description, user_count, is_external

> **Required OAuth scopes:** `usergroups:read`

### 10. usergroups_create:
Create a new user group in the workspace.

- **Parameters:**
  - `name` (string, required): Name of the user group (e.g., "Engineering Team").
  - `handle` (string, optional): Mention handle without @ (e.g., "engineering"). If not provided, Slack will auto-generate one.
  - `description` (string, optional): Purpose or description of the group.
  - `channels` (string, optional): Comma-separated channel IDs for default channels where group mentions will be highlighted.

- **Returns:** JSON with created group details (id, name, handle, description)

> **Required OAuth scopes:** `usergroups:write`

### 11. usergroups_update:
Update an existing user group's metadata.

- **Parameters:**
  - `usergroup_id` (string, required): ID of the user group (e.g., "S1234567890").
  - `name` (string, optional): New name for the group.
  - `handle` (string, optional): New mention handle.
  - `description` (string, optional): New description.
  - `channels` (string, optional): New default channels (comma-separated IDs). This replaces existing default channels.

- **Returns:** JSON with updated group details

> **Required OAuth scopes:** `usergroups:write`

### 12. usergroups_users_update:
Update the members of a user group. This replaces all existing members.

- **Parameters:**
  - `usergroup_id` (string, required): ID of the user group (e.g., "S1234567890").
  - `users` (string, required): Comma-separated user IDs to set as members (e.g., "U123,U456,U789").

- **Returns:** JSON with updated group details including new user list

> **Required OAuth scopes:** `usergroups:write`

### 13. usergroups_me:
Manage your user group membership: list groups you're in, join a group, or leave a group.

- **Parameters:**
  - `action` (string, required): Action to perform - `list` to see your groups, `join` to add yourself, `leave` to remove yourself.
  - `usergroup_id` (string, optional): ID of the user group (e.g., "S1234567890"). Required for `join` and `leave` actions.

- **Returns:**
  - For `list`: CSV with groups you're a member of
  - For `join`/`leave`: JSON with result message and updated group info

> **Required OAuth scopes:** `usergroups:read` (for list), `usergroups:read` + `usergroups:write` (for join/leave)

### 14. conversations_unreads
Get unread messages across all channels efficiently. Uses a single API call to identify channels with unreads, then fetches only those messages. Results are prioritized: DMs > partner channels (Slack Connect) > internal channels.

> **Note:** This tool works best with browser session tokens (`xoxc`/`xoxd`), which use the efficient `client.counts` API. For standard OAuth tokens (`xoxp`), a fallback method using `conversations.info` is used, which requires one API call per channel and may be slower for large workspaces. Not available with bot tokens (`xoxb`).

- **Parameters:**
  - `include_messages` (boolean, default: true): If true, returns the actual unread messages. If false, returns only a summary of channels with unreads.
  - `channel_types` (string, default: "all"): Filter by channel type: `all`, `dm` (direct messages), `group_dm` (group DMs), `partner` (externally shared channels), `internal` (regular workspace channels).
  - `max_channels` (number, default: 50): Maximum number of channels to fetch unreads from.
  - `max_messages_per_channel` (number, default: 10): Maximum messages to fetch per channel.
  - `mentions_only` (boolean, default: false): If true, only returns channels where you have @mentions. Note: This filter only works with browser tokens; OAuth tokens will return all unread channels.

### 15. conversations_mark
Mark a channel or DM as read.

> **Note:** Marking messages as read is disabled by default for safety. To enable, set the `SLACK_MCP_MARK_TOOL` environment variable to `true` or `1`. See the Environment Variables section below for details.

- **Parameters:**
  - `channel_id` (string, required): ID of the channel in format `Cxxxxxxxxxx` or its name starting with `#...` or `@...` (e.g., `#general`, `@username`).
  - `ts` (string, optional): Timestamp of the message to mark as read up to. If not provided, marks all messages as read.

### 16. saved_list
List saved items from Slack's "Save for Later" panel. Returns items the user has saved, with optional message content. This replaces the deprecated `stars.list` API ([changelog](https://api.slack.com/changelog/2023-07-its-later-already-for-stars-and-reminders)).

> **Note:** This tool requires browser session tokens (`xoxc`/`xoxd`). It is not available with standard OAuth (`xoxp`) or bot (`xoxb`) tokens.

- **Parameters:**
  - `filter` (string, default `"saved"`): Filter saved items: `"saved"` (active/in-progress), `"completed"` (marked done), `"archived"`.
  - `limit` (number, default `50`): Maximum number of items to return. Auto-paginates.
  - `include_messages` (boolean, default `true`): If true, fetches the actual saved message content. If false, returns metadata only.
  - `max_messages_per_item` (number, default `5`): Max messages to fetch per saved item (for thread replies).

### 17. saved_update
Update a saved item: mark as completed, set a due date/reminder, or both. Use `item_id` and `ts` values from `saved_list` output. This replaces the deprecated `stars.add`/`stars.remove` APIs.

> **Note:** This tool requires browser session tokens (`xoxc`/`xoxd`). It is not available with standard OAuth (`xoxp`) or bot (`xoxb`) tokens.

- **Parameters:**
  - `item_id` (string, required): Channel/DM ID where the saved message lives (from `saved_list` output).
  - `ts` (string, required): Message timestamp of the saved item (from `saved_list` output).
  - `mark` (string, optional): Set to `"completed"` to mark the item as done.
  - `date_due` (number, optional): Unix timestamp for due date/reminder. Set to `0` to clear.

### 18. saved_clear_completed
Clear all completed saved items from the "Save for Later" panel. This is a bulk operation that removes all items with `state="completed"`.

> **Note:** This tool requires browser session tokens (`xoxc`/`xoxd`). It is not available with standard OAuth (`xoxp`) or bot (`xoxb`) tokens.

- **Parameters:** None.

## Resources

The Slack MCP Server exposes two special directory resources for easy access to workspace metadata:

### 1. `slack://<workspace>/channels` — Directory of Channels

Fetches a CSV directory of all channels in the workspace, including public channels, private channels, DMs, and group DMs.

- **URI:** `slack://<workspace>/channels`
- **Format:** `text/csv`
- **Fields:**
  - `id`: Channel ID (e.g., `C1234567890`)
  - `name`: Channel name (e.g., `#general`, `@username_dm`)
  - `topic`: Channel topic (if any)
  - `purpose`: Channel purpose/description
  - `memberCount`: Number of members in the channel

### 2. `slack://<workspace>/users` — Directory of Users

Fetches a CSV directory of all users in the workspace.

- **URI:** `slack://<workspace>/users`
- **Format:** `text/csv`
- **Fields:**
  - `userID`: User ID (e.g., `U1234567890`)
  - `userName`: Slack username (e.g., `john`)
  - `realName`: User’s real name (e.g., `John Doe`)

## Setup Guide

- [Authentication Setup](docs/01-authentication-setup.md)
- [Installation](docs/02-installation.md)
- [Configuration and Usage](docs/03-configuration-and-usage.md)

### Environment Variables (Quick Reference)

| Variable                          | Required? | Default                   | Description                                                                                                                                                                                                                                                                               |
|-----------------------------------|-----------|---------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `SLACK_MCP_XOXC_TOKEN`            | Yes*      | `nil`                     | Slack browser token (`xoxc-...`)                                                                                                                                                                                                                                                          |
| `SLACK_MCP_XOXD_TOKEN`            | Yes*      | `nil`                     | Slack browser cookie `d` (`xoxd-...`)                                                                                                                                                                                                                                                     |
| `SLACK_MCP_XOXP_TOKEN`            | Yes*      | `nil`                     | User OAuth token (`xoxp-...`) — alternative to xoxc/xoxd                                                                                                                                                                                                                                  |
| `SLACK_MCP_XOXB_TOKEN`            | Yes*      | `nil`                     | Bot token (`xoxb-...`) — alternative to xoxp/xoxc/xoxd. Bot has limited access (invited channels only, no search)                                                                                                                                                                         |
| `SLACK_MCP_PORT`                  | No        | `13080`                   | Port for the MCP server to listen on                                                                                                                                                                                                                                                      |
| `SLACK_MCP_HOST`                  | No        | `127.0.0.1`               | Host for the MCP server to listen on                                                                                                                                                                                                                                                      |
| `SLACK_MCP_API_KEY`               | No        | `nil`                     | Bearer token for SSE and HTTP transports                                                                                                                                                                                                                                                            |
| `SLACK_MCP_PROXY`                 | No        | `nil`                     | Proxy URL for outgoing requests                                                                                                                                                                                                                                                           |
| `SLACK_MCP_USER_AGENT`            | No        | `nil`                     | Custom User-Agent (for Enterprise Slack environments)                                                                                                                                                                                                                                     |
| `SLACK_MCP_CUSTOM_TLS`            | No        | `nil`                     | Send custom TLS-handshake to Slack servers based on `SLACK_MCP_USER_AGENT` or default User-Agent. (for Enterprise Slack environments)                                                                                                                                                     |
| `SLACK_MCP_SERVER_CA`             | No        | `nil`                     | Path to CA certificate                                                                                                                                                                                                                                                                    |
| `SLACK_MCP_SERVER_CA_TOOLKIT`     | No        | `nil`                     | Inject HTTPToolkit CA certificate to root trust-store for MitM debugging                                                                                                                                                                                                                  |
| `SLACK_MCP_SERVER_CA_INSECURE`    | No        | `false`                   | Trust all insecure requests (NOT RECOMMENDED)                                                                                                                                                                                                                                             |
| `SLACK_MCP_ADD_MESSAGE_TOOL`      | No        | `nil`                     | Enable message posting via `conversations_add_message` by setting it to `true` for all channels, a comma-separated list of channel IDs to whitelist specific channels, or use `!` before a channel ID to allow all except specified ones. If empty, the tool is only registered when explicitly listed in `SLACK_MCP_ENABLED_TOOLS`. |
| `SLACK_MCP_ADD_MESSAGE_MARK`      | No        | `nil`                     | When `conversations_add_message` is enabled (via `SLACK_MCP_ADD_MESSAGE_TOOL` or `SLACK_MCP_ENABLED_TOOLS`), setting this to `true` will automatically mark sent messages as read.                                                                                                        |
| `SLACK_MCP_ADD_MESSAGE_UNFURLING` | No        | `nil`                     | Enable to let Slack unfurl posted links or set comma-separated list of domains e.g. `github.com,slack.com` to whitelist unfurling only for them. If text contains whitelisted and unknown domain unfurling will be disabled for security reasons.                                         |
| `SLACK_MCP_REACTION_TOOL`        | No        | `nil`                     | Enable `reactions_add` and `reactions_remove` tools by setting to `true` for all channels, a comma-separated list of channel IDs to whitelist specific channels, or use `!` before a channel ID to allow all except specified ones. If empty, the tools are only registered when explicitly listed in `SLACK_MCP_ENABLED_TOOLS`. |
| `SLACK_MCP_ATTACHMENT_TOOL`      | No        | `nil`                     | Enable the `attachment_get_data` tool by setting to `true`, `1`, or `yes`. Does not support channel-level restrictions. If empty, the tool is only registered when explicitly listed in `SLACK_MCP_ENABLED_TOOLS`. |
| `SLACK_MCP_MARK_TOOL`             | No        | `nil`                     | Enable the `conversations_mark` tool by setting to `true` or `1`. Disabled by default to prevent accidental marking of messages as read.                                                                                                                                                  |
| `SLACK_MCP_USERS_CACHE`           | No        | `~/Library/Caches/slack-mcp-server/users_cache.json` (macOS)<br>`~/.cache/slack-mcp-server/users_cache.json` (Linux)<br>`%LocalAppData%/slack-mcp-server/users_cache.json` (Windows) | Path to the users cache file. Used to cache Slack user information to avoid repeated API calls on startup. |
| `SLACK_MCP_CHANNELS_CACHE`        | No        | `~/Library/Caches/slack-mcp-server/channels_cache_v2.json` (macOS)<br>`~/.cache/slack-mcp-server/channels_cache_v2.json` (Linux)<br>`%LocalAppData%/slack-mcp-server/channels_cache_v2.json` (Windows) | Path to the channels cache file. Used to cache Slack channel information to avoid repeated API calls on startup. |
| `SLACK_MCP_CHANNEL_TYPES` *(fork)* | No        | all four types | Comma separated list of conversation types the server exposes: `public_channel`, `private_channel`, `im`, `mpim`. Example: `public_channel,private_channel` disables DM and group DM access. |
| `SLACK_MCP_READ_ONLY` *(fork)* | No        | `false` | Set to `true` to not register write tools (`conversations_add_message`, `reactions_*`, `conversations_mark`, `conversations_join`, `conversations_leave`, `usergroups_create`, `usergroups_update`, `usergroups_users_update`, `saved_update`, `saved_clear_completed`), even when other settings enable them. |
| `SLACK_MCP_LOG_LEVEL`             | No        | `info`                    | Log-level for stdout or stderr. Valid values are: `debug`, `info`, `warn`, `error`, `panic` and `fatal`                                                                                                                                                                                   |
| `SLACK_MCP_GOVSLACK`              | No        | `nil`                     | Set to `true` to enable [GovSlack](https://slack.com/solutions/govslack) mode. Routes API calls to `slack-gov.com` endpoints instead of `slack.com` for FedRAMP-compliant government workspaces.                                                                                          |
| `SLACK_MCP_ENABLED_TOOLS`         | No        | `nil`                     | Comma-separated list of tools to register. If empty, all read-only tools and usergroups tools are registered; write tools (`conversations_add_message`, `reactions_add`, `reactions_remove`, `attachment_get_data`) require their specific env var OR must be explicitly listed here. When a write tool is listed here, it's enabled without channel restrictions. Available tools: `conversations_history`, `conversations_replies`, `conversations_add_message`, `reactions_add`, `reactions_remove`, `attachment_get_data`, `conversations_search_messages`, `channels_list`, `usergroups_list`, `usergroups_me`, `usergroups_create`, `usergroups_update`, `usergroups_users_update`. |

*You need one of: `xoxp` (user), `xoxb` (bot), or both `xoxc`/`xoxd` tokens for authentication.

### Limitations matrix & Cache

| Users Cache        | Channels Cache     | Limitations                                                                                                                                                                                                                                                                                                                  |
|--------------------|--------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| :x:                | :x:                | No cache, No LLM context enhancement with user data, tool `channels_list` will be fully not functional. Tools `conversations_*` will have limited capabilities and you won't be able to search messages by `@userHandle` or `#channel-name`, getting messages by `@userHandle` or `#channel-name` won't be available either. |
| :white_check_mark: | :x:                | No channels cache, tool `channels_list` will be fully not functional. Tools `conversations_*` will have limited capabilities and you won't be able to search messages by `@userHandle` or `#channel-name`, getting messages by `@userHandle` or `#channel-name` won't be available either.                                   |
| :white_check_mark: | :white_check_mark: | No limitations, fully functional Slack MCP Server.                                                                                                                                                                                                                                                                           |

### Debugging Tools

```bash
# Run the inspector with stdio transport
npx @modelcontextprotocol/inspector go run mcp/mcp-server.go --transport stdio

# View logs
tail -n 20 -f ~/Library/Logs/Claude/mcp*.log
```

## Security

- Never share API tokens
- Keep .env files secure and private

## License

Licensed under MIT - see [LICENSE](LICENSE) file. This is not an official Slack product.
