package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/slack-go/slack"
)

// SearchBackend selects the Slack API behind conversations_search_messages.
type SearchBackend int

const (
	// SearchBackendNone: no search tool (bot tokens, as upstream).
	SearchBackendNone SearchBackend = iota
	// SearchBackendLegacy: search.messages, which needs the aggregate
	// search:read scope (or a browser session token).
	SearchBackendLegacy
	// SearchBackendRealTime: assistant.search.context, which uses granular
	// search:read.public / .private / .mpim / .im user scopes.
	SearchBackendRealTime
)

func (b SearchBackend) String() string {
	switch b {
	case SearchBackendLegacy:
		return "search.messages"
	case SearchBackendRealTime:
		return "assistant.search.context"
	default:
		return "none"
	}
}

const legacySearchScope = "search:read"

// searchScopeChanTypes maps granular Real-time Search scopes to the
// conversation type each one unlocks.
var searchScopeChanTypes = map[string]string{
	"search:read.public":  ChanTypePublic,
	"search:read.private": ChanTypePrivate,
	"search:read.mpim":    ChanTypeMPIM,
	"search:read.im":      ChanTypeIM,
}

// SelectSearchBackend picks the search API for a token. Real-time Search is
// used only for user OAuth tokens whose scopes are known, include at least one
// granular search:read.* scope and do not include legacy search:read. Every
// other configuration keeps upstream behavior.
func SelectSearchBackend(isOAuth, isBotToken bool, scopes []string, scopesKnown bool) SearchBackend {
	if isBotToken {
		return SearchBackendNone
	}
	if !isOAuth || !scopesKnown {
		return SearchBackendLegacy
	}
	granular := false
	for _, s := range scopes {
		if s == legacySearchScope {
			return SearchBackendLegacy
		}
		if _, ok := searchScopeChanTypes[s]; ok {
			granular = true
		}
	}
	if granular {
		return SearchBackendRealTime
	}
	return SearchBackendLegacy
}

// RealTimeSearchChanTypes returns the conversation types that are both enabled
// by SLACK_MCP_CHANNEL_TYPES and covered by a granted search:read.* scope, in
// canonical order.
func RealTimeSearchChanTypes(scopes []string) []string {
	granted := make(map[string]bool)
	for _, s := range scopes {
		if t, ok := searchScopeChanTypes[s]; ok {
			granted[t] = true
		}
	}
	var res []string
	for _, t := range SupportedChanTypes {
		if granted[t] && IsChanTypeAllowed(t) {
			res = append(res, t)
		}
	}
	return res
}

// SearchScopeForChanType returns the granular search scope a conversation type
// needs.
func SearchScopeForChanType(chanType string) string {
	for s, t := range searchScopeChanTypes {
		if t == chanType {
			return s
		}
	}
	return ""
}

// SearchBackend reports which search API this provider uses.
func (ap *ApiProvider) SearchBackend() SearchBackend {
	scopes, known := ap.OAuthScopes()
	return SelectSearchBackend(ap.IsOAuth(), ap.IsBotToken(), scopes, known)
}

// OAuthScopes returns the scopes granted to an OAuth token, read from the
// X-OAuth-Scopes header at startup. known is false when they could not be read.
func (ap *ApiProvider) OAuthScopes() (scopes []string, known bool) {
	client, ok := ap.client.(*MCPSlackClient)
	if !ok || client == nil {
		return nil, false
	}
	return client.oauthScopes, client.oauthScopesKnown
}

// SlackAPIError is a Slack Web API error with sanitized fields that are safe
// to show to the caller.
type SlackAPIError struct {
	Method string
	Code   string
	// Needed and Provided are set by Slack on missing_scope errors.
	Needed   string
	Provided string
}

func (e *SlackAPIError) Error() string {
	msg := fmt.Sprintf("%s failed: %s", e.Method, e.Code)
	if e.Needed != "" {
		msg += fmt.Sprintf(" (needed: %s)", e.Needed)
	}
	return msg
}

var (
	slackErrorCodePattern = regexp.MustCompile(`^[a-z0-9_]{1,128}$`)
	slackScopeListPattern = regexp.MustCompile(`^[a-z0-9_.:,]{1,512}$`)
)

func sanitizeSlackField(value string, pattern *regexp.Regexp) string {
	value = strings.TrimSpace(value)
	if !pattern.MatchString(value) {
		return ""
	}
	return value
}

// AssistantSearchContextRequest holds the assistant.search.context arguments
// this server uses. See
// https://docs.slack.dev/reference/methods/assistant.search.context
type AssistantSearchContextRequest struct {
	Query                 string
	ChannelTypes          []string
	ContextChannelID      string
	Cursor                string
	Limit                 int
	Before                int64
	After                 int64
	DisableSemanticSearch bool
}

func (r AssistantSearchContextRequest) form() url.Values {
	v := url.Values{}
	v.Set("query", r.Query)
	v.Set("content_types", "messages")
	if len(r.ChannelTypes) > 0 {
		v.Set("channel_types", strings.Join(r.ChannelTypes, ","))
	}
	if r.ContextChannelID != "" {
		v.Set("context_channel_id", r.ContextChannelID)
	}
	if r.Cursor != "" {
		v.Set("cursor", r.Cursor)
	}
	if r.Limit > 0 {
		v.Set("limit", strconv.Itoa(r.Limit))
	}
	if r.Before > 0 {
		v.Set("before", strconv.FormatInt(r.Before, 10))
	}
	if r.After > 0 {
		v.Set("after", strconv.FormatInt(r.After, 10))
	}
	if r.DisableSemanticSearch {
		v.Set("disable_semantic_search", "true")
	}
	return v
}

const (
	maxSlackResponseBytes = 5 << 20
	maxSlackRetryAfter    = 5 * time.Minute
)

// AssistantSearchContext calls assistant.search.context. slack-go only
// implements the legacy search.messages method. Based on the client in
// korotovsky/slack-mcp-server PR #328 by Jason George.
func (c *MCPSlackClient) AssistantSearchContext(ctx context.Context, params AssistantSearchContextRequest) (json.RawMessage, error) {
	const method = "assistant.search.context"
	body, header, err := c.postForm(ctx, method, params.form())
	if err != nil {
		return nil, err
	}
	var envelope struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Needed   string `json:"needed"`
		Provided string `json:"provided"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", method, err)
	}
	if !envelope.OK {
		code := sanitizeSlackField(envelope.Error, slackErrorCodePattern)
		if code == "" {
			code = "unknown_error"
		}
		if code == "ratelimited" || code == "rate_limited" {
			return nil, &slack.RateLimitedError{RetryAfter: parseRetryAfter(header)}
		}
		return nil, &SlackAPIError{
			Method:   method,
			Code:     code,
			Needed:   sanitizeSlackField(envelope.Needed, slackScopeListPattern),
			Provided: sanitizeSlackField(envelope.Provided, slackScopeListPattern),
		}
	}
	return json.RawMessage(body), nil
}

// postForm sends a credentialed Web API request without following redirects,
// so the Authorization header never crosses a redirect boundary.
func (c *MCPSlackClient) postForm(ctx context.Context, method string, form url.Values) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.teamEndpoint+"api/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, fmt.Errorf("create %s request: %w", method, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.authProvider.SlackToken())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpClient := *c.httpClient
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%s request: %w", method, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, nil, &slack.RateLimitedError{RetryAfter: parseRetryAfter(resp.Header)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSlackResponseBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("read %s response: %w", method, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, nil, fmt.Errorf("%s: HTTP %d", method, resp.StatusCode)
	}
	return body, resp.Header, nil
}

func parseRetryAfter(header http.Header) time.Duration {
	retryAfter := time.Second
	if header == nil {
		return retryAfter
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(header.Get("Retry-After")), 10, 64)
	if err == nil && seconds > 0 && time.Duration(seconds)*time.Second <= maxSlackRetryAfter {
		retryAfter = time.Duration(seconds) * time.Second
	}
	return retryAfter
}

// fetchOAuthScopes reads the scopes granted to an OAuth token from the
// X-OAuth-Scopes header of an auth.test call.
func (c *MCPSlackClient) fetchOAuthScopes(ctx context.Context) ([]string, error) {
	_, header, err := c.postForm(ctx, "auth.test", url.Values{})
	if err != nil {
		return nil, err
	}
	raw := header.Get("X-OAuth-Scopes")
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("auth.test returned no X-OAuth-Scopes header")
	}
	return parseScopeList(raw), nil
}

func parseScopeList(raw string) []string {
	var scopes []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			scopes = append(scopes, s)
		}
	}
	return scopes
}

// SearchFilterSupport reports whether the DM-oriented search filters can be
// honored: filter_in_im_or_mpim needs a searchable im or mpim type, and
// filter_users_with also needs search.messages (Real-time Search has no
// participant filter). Unrestricted legacy configurations support both.
func SearchFilterSupport(backend SearchBackend, scopes []string) (inIMOrMPIM, usersWith bool) {
	switch backend {
	case SearchBackendLegacy:
		dm := IsChanTypeAllowed(ChanTypeIM) || IsChanTypeAllowed(ChanTypeMPIM)
		return dm, dm
	case SearchBackendRealTime:
		for _, t := range RealTimeSearchChanTypes(scopes) {
			if t == ChanTypeIM || t == ChanTypeMPIM {
				return true, false
			}
		}
	}
	return false, false
}
