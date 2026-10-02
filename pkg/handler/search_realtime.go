package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/limiter"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

// Real-time Search (assistant.search.context) path of
// conversations_search_messages, used for user OAuth tokens with granular
// search:read.* scopes. Slack enforces conversation access through those
// scopes; results are additionally checked against SLACK_MCP_CHANNEL_TYPES and
// the channels cache, failing closed.

const (
	realTimeSearchPageSize     = 20 // Slack's per-page maximum
	realTimeSearchMaxPages     = 10
	realTimeSearchDefaultLimit = 20
	realTimeSearchMaxLimit     = 100
)

type realTimeSearchParams struct {
	query            string
	channelTypes     []string
	contextChannelID string
	authorID         string
	threadsOnly      bool
	after            int64
	before           int64
	limit            int
	cursor           string
}

type assistantSearchMessage struct {
	AuthorName   string `json:"author_name"`
	AuthorUserID string `json:"author_user_id"`
	ChannelID    string `json:"channel_id"`
	ChannelName  string `json:"channel_name"`
	Content      string `json:"content"`
	IsAuthorBot  bool   `json:"is_author_bot"`
	MessageTS    string `json:"message_ts"`
	Permalink    string `json:"permalink"`
}

type assistantSearchPage struct {
	Messages   []assistantSearchMessage
	NextCursor string
}

func (ch *ConversationsHandler) realTimeSearch(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Results are checked against the channels cache, so an unloaded cache
	// would drop everything and look like an empty result.
	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	scopes, _ := ch.apiProvider.OAuthScopes()
	params, err := ch.parseParamsRealTimeSearch(ctx, request, scopes)
	if err != nil {
		ch.logger.Error("Failed to parse search params", zap.Error(err))
		return nil, err
	}
	ch.logger.Debug("Real-time search params parsed",
		zap.String("query", params.query),
		zap.Strings("channel_types", params.channelTypes),
		zap.String("context_channel_id", params.contextChannelID),
		zap.Int("limit", params.limit),
	)

	rl := limiter.Tier2.Limiter()
	fetch := func(ctx context.Context, req provider.AssistantSearchContextRequest) (json.RawMessage, error) {
		return limiter.CallWithRetry(ctx, rl, 2, slackRetryAfter, func() (json.RawMessage, error) {
			return ch.apiProvider.Slack().AssistantSearchContext(ctx, req)
		})
	}
	channels := ch.apiProvider.ProvideChannelsMaps()
	lookup := ch.newSearchChannelLookup(ctx, channels)
	dropReasons := map[string]int{}
	keep := func(m assistantSearchMessage) bool {
		reason := realTimeDropReason(m, lookup, params)
		if reason != "" {
			dropReasons[reason]++
		}
		return reason == ""
	}

	matches, nextCursor, dropped, err := collectRealTimeSearch(ctx, params, fetch, keep)
	if err != nil {
		err = realTimeSearchError(err, params.channelTypes)
		ch.logger.Error("Real-time search failed", zap.Error(err))
		return nil, err
	}
	ch.logger.Debug("Real-time search completed",
		zap.Int("matches", len(matches)), zap.Int("dropped", dropped))
	if dropReasons[dropReasonDM] > 0 || dropReasons[dropReasonType] > 0 || dropReasons[dropReasonUnverified] > 0 {
		// Slack returned results outside the requested conversation types, or
		// results whose channel type could not be verified. Counts only;
		// message content is never logged.
		ch.logger.Warn("Search results outside the enabled conversation types were dropped",
			zap.Int(dropReasonDM, dropReasons[dropReasonDM]),
			zap.Int(dropReasonType, dropReasons[dropReasonType]),
			zap.Int(dropReasonUnverified, dropReasons[dropReasonUnverified]),
		)
	}

	messages := ch.convertMessagesFromRealTimeSearch(ctx, matches, channels)
	if nextCursor != "" {
		if len(messages) == 0 {
			// Every fetched result was filtered out locally, but Slack has
			// more: return the cursor so the caller can continue.
			messages = append(messages, Message{})
		}
		messages[len(messages)-1].Cursor = nextCursor
	}
	return marshalMessagesToCSV(messages)
}

// collectRealTimeSearch pages through assistant.search.context until limit
// kept results, the last page, or realTimeSearchMaxPages. Each page asks only
// for the remaining count, so a page never has to be cut and no result is
// skipped by the returned cursor. Pagination loop based on
// korotovsky/slack-mcp-server PR #328.
func collectRealTimeSearch(
	ctx context.Context,
	params *realTimeSearchParams,
	fetch func(context.Context, provider.AssistantSearchContextRequest) (json.RawMessage, error),
	keep func(assistantSearchMessage) bool,
) (matches []assistantSearchMessage, nextCursor string, dropped int, err error) {
	cursor := params.cursor
	seen := map[string]bool{}
	if cursor != "" {
		seen[cursor] = true
	}
	// Slack can return the same message on more than one page.
	seenMessages := map[string]bool{}
	for page := 0; page < realTimeSearchMaxPages && len(matches) < params.limit; page++ {
		pageLimit := params.limit - len(matches)
		if pageLimit > realTimeSearchPageSize {
			pageLimit = realTimeSearchPageSize
		}
		raw, err := fetch(ctx, provider.AssistantSearchContextRequest{
			Query:                 params.query,
			ChannelTypes:          params.channelTypes,
			ContextChannelID:      params.contextChannelID,
			Cursor:                cursor,
			Limit:                 pageLimit,
			Before:                params.before,
			After:                 params.after,
			DisableSemanticSearch: true,
		})
		if err != nil {
			return nil, "", dropped, err
		}
		result, err := decodeAssistantSearchPage(raw)
		if err != nil {
			return nil, "", dropped, err
		}
		for _, m := range result.Messages {
			if len(matches) == params.limit {
				// Slack returned more than requested; the cursor still
				// points past this page.
				break
			}
			key := m.ChannelID + "/" + m.MessageTS
			if seenMessages[key] {
				continue
			}
			seenMessages[key] = true
			if keep(m) {
				matches = append(matches, m)
			} else {
				dropped++
			}
		}
		nextCursor = result.NextCursor
		if nextCursor == "" {
			break
		}
		if seen[nextCursor] {
			return nil, "", dropped, fmt.Errorf("assistant.search.context returned a repeated cursor")
		}
		seen[nextCursor] = true
		cursor = nextCursor
	}
	return matches, nextCursor, dropped, nil
}

func decodeAssistantSearchPage(raw json.RawMessage) (assistantSearchPage, error) {
	var response struct {
		Results *struct {
			Messages []assistantSearchMessage `json:"messages"`
		} `json:"results"`
		ResponseMetadata struct {
			NextCursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return assistantSearchPage{}, fmt.Errorf("decode assistant.search.context response: %w", err)
	}
	if response.Results == nil {
		return assistantSearchPage{}, errors.New("assistant.search.context response has no results")
	}
	return assistantSearchPage{
		Messages:   response.Results.Messages,
		NextCursor: response.ResponseMetadata.NextCursor,
	}, nil
}

const (
	dropReasonDM         = "dm"
	dropReasonType       = "type_not_enabled"
	dropReasonUnverified = "type_unverified"
	dropReasonFilter     = "filter"
)

// searchChannelLookup returns the conversation behind a result's channel ID.
type searchChannelLookup func(channelID string) (provider.Channel, bool)

// newSearchChannelLookup resolves channel IDs from the channels cache and,
// for C/G IDs missing from it (archived channels, channels created after the
// cache was built, --no-cache), from conversations.info. Lookups are memoized
// for one request; a failed lookup reports false so the result is dropped.
// DM IDs are never looked up.
func (ch *ConversationsHandler) newSearchChannelLookup(ctx context.Context, channels *provider.ChannelsCache) searchChannelLookup {
	memo := map[string]*provider.Channel{}
	rl := limiter.Tier3.Limiter()
	return func(id string) (provider.Channel, bool) {
		if channels != nil {
			if c, ok := channels.Channels[id]; ok {
				return c, true
			}
		}
		if !strings.HasPrefix(id, "C") && !strings.HasPrefix(id, "G") {
			return provider.Channel{}, false
		}
		if c, seen := memo[id]; seen {
			if c == nil {
				return provider.Channel{}, false
			}
			return *c, true
		}
		memo[id] = nil
		info, err := limiter.CallWithRetry(ctx, rl, 2, slackRetryAfter, func() (*slack.Channel, error) {
			return ch.apiProvider.Slack().GetConversationInfoContext(ctx, &slack.GetConversationInfoInput{ChannelID: id})
		})
		if err != nil || info == nil || info.ID != id {
			ch.logger.Debug("Could not verify search result channel type", zap.String("channel", id), zap.Error(err))
			return provider.Channel{}, false
		}
		c := provider.Channel{
			ID:        info.ID,
			Name:      "#" + info.Name,
			IsIM:      info.IsIM,
			IsMpIM:    info.IsMpIM,
			IsPrivate: info.IsPrivate,
		}
		memo[id] = &c
		return c, true
	}
}

// realTimeDropReason is the local check on every result and returns why it
// is dropped, or "" to keep it. It fails closed: a result is dropped unless
// its channel can be resolved, is of an enabled and requested type, and
// matches the request's local filters.
func realTimeDropReason(m assistantSearchMessage, lookup searchChannelLookup, p *realTimeSearchParams) string {
	if strings.HasPrefix(m.ChannelID, "D") && !provider.IsChanTypeAllowed(provider.ChanTypeIM) {
		return dropReasonDM
	}
	c, ok := lookup(m.ChannelID)
	if !ok {
		return dropReasonUnverified
	}
	t := provider.ChanTypeOf(c.IsIM, c.IsMpIM, c.IsPrivate)
	if !provider.IsChanTypeAllowed(t) {
		if t == provider.ChanTypeIM || t == provider.ChanTypeMPIM {
			return dropReasonDM
		}
		return dropReasonType
	}
	if !containsString(p.channelTypes, t) {
		return dropReasonType
	}
	if p.contextChannelID != "" && m.ChannelID != p.contextChannelID {
		return dropReasonFilter
	}
	if p.authorID != "" && m.AuthorUserID != p.authorID {
		return dropReasonFilter
	}
	if p.threadsOnly {
		if ts, _ := extractThreadTS(m.Permalink); ts == "" {
			return dropReasonFilter
		}
	}
	return ""
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func (ch *ConversationsHandler) parseParamsRealTimeSearch(ctx context.Context, req mcp.CallToolRequest, scopes []string) (*realTimeSearchParams, error) {
	if err := rejectUnsupportedSearchFilters(req, provider.SearchBackendRealTime, scopes); err != nil {
		return nil, err
	}

	searchable := provider.RealTimeSearchChanTypes(scopes)
	if len(searchable) == 0 {
		return nil, fmt.Errorf("no enabled conversation type is covered by the token's search scopes (enabled: %s); "+
			"add %s to the Slack app's User Token Scopes and reinstall it",
			strings.Join(provider.AllChanTypes, ","), strings.Join(searchScopesFor(provider.AllChanTypes), " or "))
	}

	freeText, filters := splitQuery(strings.TrimSpace(req.GetString("search_query", "")))
	if len(filters["with"]) > 0 {
		return nil, errors.New("the with: modifier is not supported by Real-time Search")
	}
	p := &realTimeSearchParams{channelTypes: searchable}

	// Channel scope: in: modifiers, filter_in_channel and filter_in_im_or_mpim
	// must all name the same conversation.
	var channelRefs []string
	channelRefs = append(channelRefs, filters["in"]...)
	for _, key := range []string{"filter_in_channel", "filter_in_im_or_mpim"} {
		if v := strings.TrimSpace(req.GetString(key, "")); v != "" {
			channelRefs = append(channelRefs, v)
		}
	}
	for _, ref := range channelRefs {
		c, err := ch.resolveSearchChannel(ctx, ref)
		if err != nil {
			return nil, err
		}
		if p.contextChannelID != "" && p.contextChannelID != c.ID {
			return nil, fmt.Errorf("conflicting channel filters: %s and %s", p.contextChannelID, c.ID)
		}
		t := provider.ChanTypeOf(c.IsIM, c.IsMpIM, c.IsPrivate)
		if !provider.IsChanTypeAllowed(t) {
			return nil, fmt.Errorf("%w: %s is of type %s", provider.ErrChannelTypeNotAllowed, c.ID, t)
		}
		if !containsString(searchable, t) {
			return nil, fmt.Errorf("searching %s conversations needs the %s scope; add it to the Slack app's User Token Scopes and reinstall it",
				t, provider.SearchScopeForChanType(t))
		}
		p.contextChannelID = c.ID
		p.channelTypes = []string{t}
	}

	// Author: one from: modifier or filter_users_from.
	from := filters["from"]
	if v := strings.TrimSpace(req.GetString("filter_users_from", "")); v != "" {
		from = append(from, v)
	}
	if len(from) > 1 {
		return nil, errors.New("only one author filter is supported (from: modifier or filter_users_from)")
	}
	if len(from) == 1 {
		f, err := ch.paramFormatUser(ctx, normalizeUserMention(from[0]))
		if err != nil {
			return nil, err
		}
		p.authorID = strings.TrimSuffix(strings.TrimPrefix(f, "<@"), ">")
	}

	for _, v := range filters["is"] {
		if strings.ToLower(v) != "thread" {
			return nil, fmt.Errorf("the is:%s modifier is not supported by Real-time Search", v)
		}
		p.threadsOnly = true
	}
	if req.GetBool("filter_threads_only", false) {
		p.threadsOnly = true
	}

	dates := map[string]string{}
	for _, key := range []string{"before", "after", "on", "during"} {
		param := strings.TrimSpace(req.GetString("filter_date_"+key, ""))
		mods := filters[key]
		if len(mods) > 1 || (param != "" && len(mods) == 1 && mods[0] != param) {
			return nil, fmt.Errorf("conflicting %s date filters", key)
		}
		if param == "" && len(mods) == 1 {
			param = mods[0]
		}
		dates[key] = param
	}
	after, before, err := realTimeDateRange(dates["before"], dates["after"], dates["on"], dates["during"])
	if err != nil {
		return nil, err
	}
	p.after, p.before = after, before

	p.query = strings.Join(freeText, " ")
	if p.query == "" {
		return nil, errors.New("search_query must contain search text; Real-time Search does not accept filter-only queries")
	}

	p.limit = req.GetInt("limit", realTimeSearchDefaultLimit)
	if p.limit < 1 || p.limit > realTimeSearchMaxLimit {
		return nil, fmt.Errorf("limit must be between 1 and %d", realTimeSearchMaxLimit)
	}
	p.cursor = req.GetString("cursor", "")
	return p, nil
}

// resolveSearchChannel resolves an ID, #name or @user DM name to a cached
// conversation.
func (ch *ConversationsHandler) resolveSearchChannel(ctx context.Context, raw string) (provider.Channel, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "#") && !strings.HasPrefix(raw, "@") &&
		!strings.HasPrefix(raw, "C") && !strings.HasPrefix(raw, "G") && !strings.HasPrefix(raw, "D") {
		raw = "#" + raw
	}
	id, err := ch.resolveChannelID(ctx, raw)
	if err != nil {
		return provider.Channel{}, err
	}
	if strings.HasPrefix(id, "D") && !provider.IsChanTypeAllowed(provider.ChanTypeIM) {
		return provider.Channel{}, fmt.Errorf("%w: %s is a direct message", provider.ErrChannelTypeNotAllowed, id)
	}
	channels := ch.apiProvider.ProvideChannelsMaps()
	c, ok := channels.Channels[id]
	if !ok {
		return provider.Channel{}, fmt.Errorf("channel %q not found", raw)
	}
	return c, nil
}

// normalizeUserMention turns a <@U123> or <@U123|name> mention into U123.
func normalizeUserMention(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "<@") || !strings.HasSuffix(raw, ">") {
		return raw
	}
	raw = strings.TrimSuffix(strings.TrimPrefix(raw, "<@"), ">")
	if i := strings.IndexByte(raw, '|'); i >= 0 {
		raw = raw[:i]
	}
	return raw
}

var monthYearPattern = regexp.MustCompile(`^(\d{4})\s+[A-Za-z]+$|^[A-Za-z]+\s+(\d{4})$`)

// realTimeDateRange converts the date filters to exclusive UNIX bounds with
// the same day semantics as Slack's legacy before:/after:/on:/during:
// modifiers, using whole UTC days (after: excludes the given day).
func realTimeDateRange(before, after, on, during string) (afterUnix, beforeUnix int64, err error) {
	if _, err := buildDateFilters(before, after, on, during); err != nil {
		return 0, 0, err
	}
	const day = 24 * time.Hour
	dayStart := func(s string) time.Time {
		t, _, _ := parseFlexibleDate(s)
		return t
	}
	switch {
	case on != "":
		t := dayStart(on)
		return t.Unix() - 1, t.Add(day).Unix(), nil
	case during != "":
		t := dayStart(during)
		end := t.Add(day)
		if monthYearPattern.MatchString(strings.TrimSpace(during)) {
			end = t.AddDate(0, 1, 0)
		}
		return t.Unix() - 1, end.Unix(), nil
	}
	if after != "" {
		afterUnix = dayStart(after).Add(day).Unix() - 1
	}
	if before != "" {
		beforeUnix = dayStart(before).Unix()
	}
	return afterUnix, beforeUnix, nil
}

func searchScopesFor(chanTypes []string) []string {
	var scopes []string
	for _, t := range chanTypes {
		if s := provider.SearchScopeForChanType(t); s != "" {
			scopes = append(scopes, s)
		}
	}
	return scopes
}

// realTimeSearchError turns Slack error codes into actionable tool errors.
func realTimeSearchError(err error, chanTypes []string) error {
	var apiErr *provider.SlackAPIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.Code {
	case "missing_scope":
		needed := apiErr.Needed
		if needed == "" {
			needed = strings.Join(searchScopesFor(chanTypes), ", ")
		}
		return fmt.Errorf("Slack token is missing the %s scope; add it to the Slack app's User Token Scopes and reinstall the app: %w", needed, err)
	case "not_allowed_token_type":
		return fmt.Errorf("Real-time Search does not accept this token type; use a user OAuth token (xoxp) from an internal or directory-published Slack app: %w", err)
	case "feature_not_enabled", "assistant_search_context_disabled":
		return fmt.Errorf("Real-time Search is not available on this Slack workspace right now: %w", err)
	}
	return err
}

func (ch *ConversationsHandler) convertMessagesFromRealTimeSearch(ctx context.Context, source []assistantSearchMessage, channels *provider.ChannelsCache) []Message {
	resolver := ch.newUserResolver(ctx)
	messages := make([]Message, 0, len(source))
	for _, m := range source {
		timestamp, err := text.TimestampToIsoRFC3339(m.MessageTS)
		if err != nil {
			ch.logger.Warn("Skipping search result with invalid timestamp", zap.Error(err))
			continue
		}
		userName, realName, ok := resolver.resolve(m.AuthorUserID)
		if !ok || realName == "" {
			realName = m.AuthorName
		}
		name := strings.TrimPrefix(m.ChannelName, "#")
		if c, ok := channels.Channels[m.ChannelID]; ok && c.Name != "" {
			name = strings.TrimPrefix(c.Name, "#")
		}
		botName := ""
		if m.IsAuthorBot {
			botName = m.AuthorName
		}
		threadTs, _ := extractThreadTS(m.Permalink)
		messages = append(messages, Message{
			MsgID:     m.MessageTS,
			UserID:    m.AuthorUserID,
			UserName:  userName,
			RealName:  realName,
			Channel:   fmt.Sprintf("%s (#%s)", m.ChannelID, name),
			ThreadTs:  threadTs,
			Text:      text.ProcessText(m.Content),
			Time:      timestamp,
			Permalink: m.Permalink,
			BotName:   botName,
		})
	}
	return messages
}

// rejectUnsupportedSearchFilters rejects DM-only filters when no DM type can
// be searched, and filters the active search API cannot honor.
func rejectUnsupportedSearchFilters(req mcp.CallToolRequest, backend provider.SearchBackend, scopes []string) error {
	inDM, usersWith := provider.SearchFilterSupport(backend, scopes)
	if !inDM && strings.TrimSpace(req.GetString("filter_in_im_or_mpim", "")) != "" {
		return fmt.Errorf("%w: filter_in_im_or_mpim needs im or mpim, enabled types are %s",
			provider.ErrChannelTypeNotAllowed, strings.Join(provider.AllChanTypes, ","))
	}
	if !usersWith && strings.TrimSpace(req.GetString("filter_users_with", "")) != "" {
		if backend == provider.SearchBackendRealTime {
			return errors.New("filter_users_with is not supported by Real-time Search")
		}
		return fmt.Errorf("%w: filter_users_with needs im or mpim, enabled types are %s",
			provider.ErrChannelTypeNotAllowed, strings.Join(provider.AllChanTypes, ","))
	}
	return nil
}
