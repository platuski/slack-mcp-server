package server

import (
	"fmt"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
)

// hasSearchTool reports whether conversations_search_messages is registered.
// Bot tokens get no search tool, as upstream.
func hasSearchTool(backend provider.SearchBackend) bool {
	return backend != provider.SearchBackendNone
}

// searchToolOptions builds the conversations_search_messages schema. With
// search.messages and no SLACK_MCP_CHANNEL_TYPES restriction it is the
// upstream schema. Otherwise the description names the searchable types and
// filters that cannot be honored are left out, so agents do not try them.
func searchToolOptions(backend provider.SearchBackend, scopes []string) []mcp.ToolOption {
	realTime := backend == provider.SearchBackendRealTime
	inDM, usersWith := provider.SearchFilterSupport(backend, scopes)

	description := "Search messages in a public channel, private channel, or direct message (DM, or IM) conversation using filters. All filters are optional, if not provided then search_query is required."
	queryDescription := "Search query to filter messages. Example: 'marketing report' or full URL of Slack message e.g. 'https://slack.com/archives/C1234567890/p1234567890123456', then the tool will return a single message matching given URL, herewith all other parameters will be ignored."
	switch {
	case realTime:
		description = fmt.Sprintf("Search messages with Slack Real-time Search in these conversation types: %s. search_query must contain search text; all filters are optional.",
			strings.Join(provider.RealTimeSearchChanTypes(scopes), ", "))
		queryDescription = "Search text, required. Optional modifiers: 'in:#channel', 'from:@user', 'before:', 'after:', 'on:' or 'during:' with a date, 'is:thread'. Example: 'deploy failed from:@alice after:2025-01-01'."
	case !provider.AllChanTypesAllowed():
		description = fmt.Sprintf("Search messages in these conversation types: %s. All filters are optional, if not provided then search_query is required.",
			strings.Join(provider.AllChanTypes, ", "))
	}

	opts := []mcp.ToolOption{
		mcp.WithDescription(description),
		mcp.WithTitleAnnotation("Search Messages"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("search_query",
			mcp.Description(queryDescription),
		),
		mcp.WithString("filter_in_channel",
			mcp.Description("Filter messages in a specific public/private channel by its ID or name. Example: 'C1234567890', 'G1234567890', or '#general'. If not provided, all channels will be searched."),
		),
	}
	if inDM {
		opts = append(opts, mcp.WithString("filter_in_im_or_mpim",
			mcp.Description("Filter messages in a direct message (DM) or multi-person direct message (MPIM) conversation by its ID or name. Example: 'D1234567890' or '@username_dm'. If not provided, all DMs and MPIMs will be searched."),
		))
	}
	if usersWith {
		opts = append(opts, mcp.WithString("filter_users_with",
			mcp.Description("Filter messages with a specific user by their ID or display name in threads and DMs. Example: 'U1234567890' or '@username'. If not provided, all threads and DMs will be searched."),
		))
	}
	return append(opts,
		mcp.WithString("filter_users_from",
			mcp.Description("Filter messages from a specific user by their ID or display name. Example: 'U1234567890' or '@username'. If not provided, all users will be searched."),
		),
		mcp.WithString("filter_date_before",
			mcp.Description("Filter messages sent before a specific date in format 'YYYY-MM-DD'. Example: '2023-10-01', 'July', 'Yesterday' or 'Today'. If not provided, all dates will be searched."),
		),
		mcp.WithString("filter_date_after",
			mcp.Description("Filter messages sent after a specific date in format 'YYYY-MM-DD'. Example: '2023-10-01', 'July', 'Yesterday' or 'Today'. If not provided, all dates will be searched."),
		),
		mcp.WithString("filter_date_on",
			mcp.Description("Filter messages sent on a specific date in format 'YYYY-MM-DD'. Example: '2023-10-01', 'July', 'Yesterday' or 'Today'. If not provided, all dates will be searched."),
		),
		mcp.WithString("filter_date_during",
			mcp.Description("Filter messages sent during a specific period in format 'YYYY-MM-DD'. Example: 'July', 'Yesterday' or 'Today'. If not provided, all dates will be searched."),
		),
		mcp.WithBoolean("filter_threads_only",
			mcp.Description("If true, the response will include only messages from threads. Default is boolean false."),
		),
		mcp.WithString("cursor",
			mcp.DefaultString(""),
			mcp.Description("Cursor for pagination. Use the value of the last row and column in the response as next_cursor field returned from the previous request."),
		),
		mcp.WithNumber("limit",
			mcp.DefaultNumber(20),
			mcp.Description("The maximum number of items to return. Must be an integer between 1 and 100."),
		),
	)
}
