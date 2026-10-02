package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func withChanTypes(t *testing.T, types []string) {
	t.Helper()
	prev := append([]string(nil), provider.AllChanTypes...)
	provider.SetChanTypes(types)
	t.Cleanup(func() { provider.SetChanTypes(prev) })
}

func searchRequest(args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Arguments = args
	return req
}

var testSearchChannels = &provider.ChannelsCache{
	Channels: map[string]provider.Channel{
		"C1": {ID: "C1", Name: "#general"},
		"G1": {ID: "G1", Name: "#secret", IsPrivate: true},
		"G2": {ID: "G2", Name: "mpdm-a--b-1", IsMpIM: true, IsPrivate: true},
		"D1": {ID: "D1", Name: "@alice", IsIM: true},
	},
}

// testLookup resolves from testSearchChannels, then from info (standing in
// for conversations.info).
func testLookup(info map[string]provider.Channel) searchChannelLookup {
	return func(id string) (provider.Channel, bool) {
		if c, ok := testSearchChannels.Channels[id]; ok {
			return c, true
		}
		c, ok := info[id]
		return c, ok
	}
}

func realTimeResultAllowed(m assistantSearchMessage, lookup searchChannelLookup, p *realTimeSearchParams) bool {
	return realTimeDropReason(m, lookup, p) == ""
}

func TestUnitRealTimeResultAllowed(t *testing.T) {
	withChanTypes(t, []string{provider.ChanTypePublic, provider.ChanTypePrivate})
	p := &realTimeSearchParams{channelTypes: []string{provider.ChanTypePublic, provider.ChanTypePrivate}}

	lookup := testLookup(map[string]provider.Channel{
		"C900": {ID: "C900", Name: "#archived"},
		"G900": {ID: "G900", Name: "mpdm-x--y-1", IsMpIM: true, IsPrivate: true},
	})
	allowed := func(m assistantSearchMessage) bool { return realTimeResultAllowed(m, lookup, p) }

	assert.True(t, allowed(assistantSearchMessage{ChannelID: "C1"}), "public channel")
	assert.True(t, allowed(assistantSearchMessage{ChannelID: "G1"}), "private channel")
	assert.False(t, allowed(assistantSearchMessage{ChannelID: "G2"}), "mpim is dropped")
	assert.False(t, allowed(assistantSearchMessage{ChannelID: "D1"}), "cached DM is dropped")
	assert.False(t, allowed(assistantSearchMessage{ChannelID: "D999"}), "uncached DM ID is dropped")
	assert.False(t, allowed(assistantSearchMessage{ChannelID: "C404"}), "channel absent from cache is dropped")
	assert.False(t, allowed(assistantSearchMessage{ChannelID: ""}), "missing channel ID is dropped")
	assert.True(t, allowed(assistantSearchMessage{ChannelID: "C900"}), "uncached channel verified by lookup")
	assert.False(t, allowed(assistantSearchMessage{ChannelID: "G900"}), "uncached mpim verified by lookup is dropped")
	noLookup := func(string) (provider.Channel, bool) { return provider.Channel{}, false }
	assert.False(t, realTimeResultAllowed(assistantSearchMessage{ChannelID: "C1"}, noLookup, p), "failed lookup fails closed")

	t.Run("drop reasons", func(t *testing.T) {
		reason := func(id string) string {
			return realTimeDropReason(assistantSearchMessage{ChannelID: id}, lookup, p)
		}
		assert.Equal(t, "", reason("C1"))
		assert.Equal(t, dropReasonDM, reason("D999"))
		assert.Equal(t, dropReasonDM, reason("G2"), "mpim counts as a DM type")
		assert.Equal(t, dropReasonUnverified, reason("C404"))
		assert.Equal(t, dropReasonDM, reason("G900"))
	})

	t.Run("type enabled but not requested", func(t *testing.T) {
		pub := &realTimeSearchParams{channelTypes: []string{provider.ChanTypePublic}}
		assert.False(t, realTimeResultAllowed(assistantSearchMessage{ChannelID: "G1"}, testLookup(nil), pub))
	})

	t.Run("local filters", func(t *testing.T) {
		f := &realTimeSearchParams{
			channelTypes:     p.channelTypes,
			contextChannelID: "C1",
			authorID:         "U1",
			threadsOnly:      true,
		}
		thread := "https://x.slack.com/archives/C1/p1?thread_ts=1.0"
		assert.True(t, realTimeResultAllowed(assistantSearchMessage{ChannelID: "C1", AuthorUserID: "U1", Permalink: thread}, testLookup(nil), f))
		assert.False(t, realTimeResultAllowed(assistantSearchMessage{ChannelID: "G1", AuthorUserID: "U1", Permalink: thread}, testLookup(nil), f), "other channel")
		assert.False(t, realTimeResultAllowed(assistantSearchMessage{ChannelID: "C1", AuthorUserID: "U2", Permalink: thread}, testLookup(nil), f), "other author")
		assert.False(t, realTimeResultAllowed(assistantSearchMessage{ChannelID: "C1", AuthorUserID: "U1", Permalink: "https://x.slack.com/archives/C1/p1"}, testLookup(nil), f), "not a thread reply")
	})
}

type fakeSearchPage struct {
	channels []string
	next     string
}

func fakeSearchFetch(pages []fakeSearchPage, calls *[]provider.AssistantSearchContextRequest) func(context.Context, provider.AssistantSearchContextRequest) (json.RawMessage, error) {
	return func(_ context.Context, req provider.AssistantSearchContextRequest) (json.RawMessage, error) {
		i := len(*calls)
		*calls = append(*calls, req)
		if i >= len(pages) {
			return nil, errors.New("unexpected extra page")
		}
		var msgs []assistantSearchMessage
		for n, ch := range pages[i].channels {
			if n >= req.Limit {
				break
			}
			msgs = append(msgs, assistantSearchMessage{ChannelID: ch, MessageTS: fmt.Sprintf("1700000000.%06d", i*100+n)})
		}
		body, _ := json.Marshal(map[string]any{
			"ok":                true,
			"results":           map[string]any{"messages": msgs},
			"response_metadata": map[string]any{"next_cursor": pages[i].next},
		})
		return body, nil
	}
}

func TestUnitCollectRealTimeSearch(t *testing.T) {
	withChanTypes(t, []string{provider.ChanTypePublic, provider.ChanTypePrivate})
	p := &realTimeSearchParams{
		query:        "deploy",
		channelTypes: []string{provider.ChanTypePublic, provider.ChanTypePrivate},
		limit:        3,
	}
	keep := func(m assistantSearchMessage) bool { return realTimeResultAllowed(m, testLookup(nil), p) }

	t.Run("drops DM and mpim results and pages until the limit", func(t *testing.T) {
		var calls []provider.AssistantSearchContextRequest
		pages := []fakeSearchPage{
			{channels: []string{"C1", "D1", "G2"}, next: "c2"},
			{channels: []string{"G1", "D5"}, next: "c3"},
			{channels: []string{"C1"}, next: "c4"},
		}
		matches, next, dropped, err := collectRealTimeSearch(context.Background(), p, fakeSearchFetch(pages, &calls), keep)
		require.NoError(t, err)
		require.Len(t, matches, 3)
		for _, m := range matches {
			assert.NotEqual(t, "D", m.ChannelID[:1])
			assert.NotEqual(t, "G2", m.ChannelID)
		}
		assert.Equal(t, 3, dropped)
		assert.Equal(t, "c4", next)
		require.Len(t, calls, 3)
		for _, c := range calls {
			assert.Equal(t, []string{provider.ChanTypePublic, provider.ChanTypePrivate}, c.ChannelTypes)
			assert.True(t, c.DisableSemanticSearch)
		}
		assert.Equal(t, 3, calls[0].Limit)
		assert.Equal(t, 2, calls[1].Limit, "later pages only ask for the remaining count")
		assert.Equal(t, "c2", calls[1].Cursor)
	})

	t.Run("never returns more than limit when Slack over-delivers", func(t *testing.T) {
		fetch := func(context.Context, provider.AssistantSearchContextRequest) (json.RawMessage, error) {
			var msgs []assistantSearchMessage
			for i := 0; i < 5; i++ {
				msgs = append(msgs, assistantSearchMessage{ChannelID: "C1", MessageTS: fmt.Sprintf("1700000000.00000%d", i)})
			}
			body, _ := json.Marshal(map[string]any{"ok": true, "results": map[string]any{"messages": msgs}})
			return body, nil
		}
		matches, _, _, err := collectRealTimeSearch(context.Background(), p, fetch, keep)
		require.NoError(t, err)
		assert.Len(t, matches, p.limit)
	})

	t.Run("message repeated across pages is returned once", func(t *testing.T) {
		calls := 0
		fetch := func(context.Context, provider.AssistantSearchContextRequest) (json.RawMessage, error) {
			calls++
			msgs := []assistantSearchMessage{{ChannelID: "C1", MessageTS: "1700000000.000001"}}
			next := "c2"
			if calls == 2 {
				msgs = append(msgs, assistantSearchMessage{ChannelID: "C1", MessageTS: "1700000000.000002"})
				next = ""
			}
			body, _ := json.Marshal(map[string]any{
				"ok":                true,
				"results":           map[string]any{"messages": msgs},
				"response_metadata": map[string]any{"next_cursor": next},
			})
			return body, nil
		}
		matches, _, _, err := collectRealTimeSearch(context.Background(), p, fetch, keep)
		require.NoError(t, err)
		require.Len(t, matches, 2)
		assert.NotEqual(t, matches[0].MessageTS, matches[1].MessageTS)
	})

	t.Run("repeated cursor is an error", func(t *testing.T) {
		var calls []provider.AssistantSearchContextRequest
		pages := []fakeSearchPage{{channels: []string{"D1"}, next: "same"}, {channels: []string{"D1"}, next: "same"}}
		_, _, _, err := collectRealTimeSearch(context.Background(), p, fakeSearchFetch(pages, &calls), keep)
		require.Error(t, err)
	})

	t.Run("stops at the page cap and returns the cursor", func(t *testing.T) {
		var calls []provider.AssistantSearchContextRequest
		var pages []fakeSearchPage
		for i := 0; i < realTimeSearchMaxPages+1; i++ {
			pages = append(pages, fakeSearchPage{channels: []string{"D1"}, next: fmt.Sprintf("c%d", i+1)})
		}
		matches, next, _, err := collectRealTimeSearch(context.Background(), p, fakeSearchFetch(pages, &calls), keep)
		require.NoError(t, err)
		assert.Empty(t, matches)
		assert.Len(t, calls, realTimeSearchMaxPages)
		assert.Equal(t, fmt.Sprintf("c%d", realTimeSearchMaxPages), next)
	})

	t.Run("response without results is an error", func(t *testing.T) {
		fetch := func(context.Context, provider.AssistantSearchContextRequest) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		}
		_, _, _, err := collectRealTimeSearch(context.Background(), p, fetch, keep)
		require.Error(t, err)
	})
}

func TestUnitRejectUnsupportedSearchFilters(t *testing.T) {
	channelScopes := []string{"search:read.public", "search:read.private"}
	dmFilter := searchRequest(map[string]any{"filter_in_im_or_mpim": "@alice"})
	withFilter := searchRequest(map[string]any{"filter_users_with": "@alice"})

	t.Run("DM types disabled rejects both filters", func(t *testing.T) {
		withChanTypes(t, []string{provider.ChanTypePublic, provider.ChanTypePrivate})
		for _, backend := range []provider.SearchBackend{provider.SearchBackendLegacy, provider.SearchBackendRealTime} {
			err := rejectUnsupportedSearchFilters(dmFilter, backend, channelScopes)
			assert.True(t, errors.Is(err, provider.ErrChannelTypeNotAllowed), backend.String())
			assert.Error(t, rejectUnsupportedSearchFilters(withFilter, backend, channelScopes), backend.String())
		}
		err := rejectUnsupportedSearchFilters(withFilter, provider.SearchBackendLegacy, nil)
		assert.True(t, errors.Is(err, provider.ErrChannelTypeNotAllowed))
	})

	t.Run("unrestricted legacy accepts both filters", func(t *testing.T) {
		withChanTypes(t, provider.SupportedChanTypes)
		assert.NoError(t, rejectUnsupportedSearchFilters(dmFilter, provider.SearchBackendLegacy, nil))
		assert.NoError(t, rejectUnsupportedSearchFilters(withFilter, provider.SearchBackendLegacy, nil))
	})

	t.Run("real-time never accepts filter_users_with", func(t *testing.T) {
		withChanTypes(t, provider.SupportedChanTypes)
		all := append(channelScopes, "search:read.mpim", "search:read.im")
		assert.Error(t, rejectUnsupportedSearchFilters(withFilter, provider.SearchBackendRealTime, all))
		assert.NoError(t, rejectUnsupportedSearchFilters(dmFilter, provider.SearchBackendRealTime, all))
	})

	t.Run("filters absent is fine", func(t *testing.T) {
		withChanTypes(t, []string{provider.ChanTypePublic})
		assert.NoError(t, rejectUnsupportedSearchFilters(searchRequest(map[string]any{"search_query": "x"}), provider.SearchBackendRealTime, channelScopes))
	})
}

func TestUnitParseParamsRealTimeSearch(t *testing.T) {
	withChanTypes(t, []string{provider.ChanTypePublic, provider.ChanTypePrivate})
	ch := &ConversationsHandler{apiProvider: &provider.ApiProvider{}, logger: zap.NewNop()}
	scopes := []string{"search:read.public", "search:read.private", "search:read.im"}
	ctx := context.Background()

	t.Run("query, types and dates", func(t *testing.T) {
		p, err := ch.parseParamsRealTimeSearch(ctx, searchRequest(map[string]any{
			"search_query":      "<@U0123456789> is:thread",
			"filter_date_after": "2026-09-24",
			"limit":             float64(50),
		}), scopes)
		require.NoError(t, err)
		assert.Equal(t, "<@U0123456789>", p.query)
		assert.Equal(t, []string{provider.ChanTypePublic, provider.ChanTypePrivate}, p.channelTypes, "im scope is ignored while im is disabled")
		assert.True(t, p.threadsOnly)
		assert.Equal(t, time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC).Unix()-1, p.after)
		assert.Zero(t, p.before)
		assert.Equal(t, 50, p.limit)
	})

	t.Run("default limit", func(t *testing.T) {
		p, err := ch.parseParamsRealTimeSearch(ctx, searchRequest(map[string]any{"search_query": "x"}), scopes)
		require.NoError(t, err)
		assert.Equal(t, realTimeSearchDefaultLimit, p.limit)
	})

	for name, args := range map[string]map[string]any{
		"filter-only query":     {"search_query": "after:2026-01-01"},
		"with: modifier":        {"search_query": "x with:@alice"},
		"filter_users_with":     {"search_query": "x", "filter_users_with": "@alice"},
		"filter_in_im_or_mpim":  {"search_query": "x", "filter_in_im_or_mpim": "@alice"},
		"unsupported is:":       {"search_query": "x is:saved"},
		"limit too high":        {"search_query": "x", "limit": float64(101)},
		"limit zero":            {"search_query": "x", "limit": float64(0)},
		"conflicting dates":     {"search_query": "x after:2026-01-01", "filter_date_after": "2026-02-01"},
		"on with other filters": {"search_query": "x", "filter_date_on": "2026-01-01", "filter_date_after": "2025-01-01"},
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			_, err := ch.parseParamsRealTimeSearch(ctx, searchRequest(args), scopes)
			assert.Error(t, err)
		})
	}

	t.Run("no searchable type names the scopes", func(t *testing.T) {
		_, err := ch.parseParamsRealTimeSearch(ctx, searchRequest(map[string]any{"search_query": "x"}), []string{"search:read.im"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "search:read.public")
		assert.Contains(t, err.Error(), "search:read.private")
	})
}

func TestUnitRealTimeDateRange(t *testing.T) {
	day := func(y int, m time.Month, d int) int64 { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() }

	after, before, err := realTimeDateRange("2026-03-10", "2026-03-01", "", "")
	require.NoError(t, err)
	assert.Equal(t, day(2026, 3, 2)-1, after, "after: excludes the given day")
	assert.Equal(t, day(2026, 3, 10), before, "before: excludes the given day")

	after, before, err = realTimeDateRange("", "", "2026-03-05", "")
	require.NoError(t, err)
	assert.Equal(t, day(2026, 3, 5)-1, after)
	assert.Equal(t, day(2026, 3, 6), before)

	after, before, err = realTimeDateRange("", "", "", "July 2026")
	require.NoError(t, err)
	assert.Equal(t, day(2026, 7, 1)-1, after)
	assert.Equal(t, day(2026, 8, 1), before, "month-year spans the month")

	_, _, err = realTimeDateRange("2026-01-01", "2026-02-01", "", "")
	assert.Error(t, err, "after later than before")
}

func TestUnitRealTimeSearchError(t *testing.T) {
	types := []string{provider.ChanTypePublic, provider.ChanTypePrivate}

	err := realTimeSearchError(&provider.SlackAPIError{Method: "assistant.search.context", Code: "missing_scope", Needed: "search:read.private"}, types)
	assert.Contains(t, err.Error(), "search:read.private")
	assert.Contains(t, err.Error(), "reinstall")

	err = realTimeSearchError(&provider.SlackAPIError{Method: "assistant.search.context", Code: "missing_scope"}, types)
	assert.Contains(t, err.Error(), "search:read.public, search:read.private")

	err = realTimeSearchError(&provider.SlackAPIError{Method: "assistant.search.context", Code: "not_allowed_token_type"}, types)
	assert.Contains(t, err.Error(), "user OAuth token")

	for _, code := range []string{"feature_not_enabled", "assistant_search_context_disabled"} {
		err = realTimeSearchError(&provider.SlackAPIError{Method: "assistant.search.context", Code: code}, types)
		assert.Contains(t, err.Error(), "not available")
		var apiErr *provider.SlackAPIError
		assert.True(t, errors.As(err, &apiErr), "original error stays wrapped")
	}

	plain := errors.New("boom")
	assert.Equal(t, plain, realTimeSearchError(plain, types))
}

func TestUnitNormalizeUserMention(t *testing.T) {
	assert.Equal(t, "U123", normalizeUserMention("<@U123>"))
	assert.Equal(t, "U123", normalizeUserMention(" <@U123|alice> "))
	assert.Equal(t, "@alice", normalizeUserMention("@alice"))
	assert.Equal(t, "U123", normalizeUserMention("U123"))
	assert.Equal(t, "<@U123", normalizeUserMention("<@U123"), "unterminated mention is left alone")
}
