package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rusq/slackdump/v3/auth"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnitSelectSearchBackend(t *testing.T) {
	granular := []string{"channels:read", "search:read.public", "search:read.private"}

	tests := []struct {
		name        string
		isOAuth     bool
		isBot       bool
		scopes      []string
		scopesKnown bool
		want        SearchBackend
	}{
		{"bot token has no search", true, true, granular, true, SearchBackendNone},
		{"browser session token keeps search.messages", false, false, nil, false, SearchBackendLegacy},
		{"user token with unknown scopes keeps search.messages", true, false, nil, false, SearchBackendLegacy},
		{"user token with legacy search:read", true, false, []string{"channels:read", "search:read"}, true, SearchBackendLegacy},
		{"legacy search:read wins over granular scopes", true, false, append([]string{"search:read"}, granular...), true, SearchBackendLegacy},
		{"user token with granular scopes only", true, false, granular, true, SearchBackendRealTime},
		{"user token with private scope only", true, false, []string{"search:read.private"}, true, SearchBackendRealTime},
		{"user token without search scopes keeps search.messages", true, false, []string{"channels:read"}, true, SearchBackendLegacy},
		{"files and users scopes are not message scopes", true, false, []string{"search:read.files", "search:read.users"}, true, SearchBackendLegacy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SelectSearchBackend(tt.isOAuth, tt.isBot, tt.scopes, tt.scopesKnown))
		})
	}
}

func TestUnitRealTimeSearchChanTypes(t *testing.T) {
	all := []string{"search:read.public", "search:read.private", "search:read.mpim", "search:read.im"}

	t.Run("limited to enabled types", func(t *testing.T) {
		withChanTypes(t, []string{ChanTypePublic, ChanTypePrivate})
		assert.Equal(t, []string{ChanTypePublic, ChanTypePrivate}, RealTimeSearchChanTypes(all))
	})

	t.Run("limited to granted scopes", func(t *testing.T) {
		withChanTypes(t, SupportedChanTypes)
		assert.Equal(t, []string{ChanTypePublic}, RealTimeSearchChanTypes([]string{"search:read.public", "channels:read"}))
	})

	t.Run("nothing searchable", func(t *testing.T) {
		withChanTypes(t, []string{ChanTypeIM})
		assert.Empty(t, RealTimeSearchChanTypes([]string{"search:read.public"}))
	})
}

func TestUnitSearchFilterSupport(t *testing.T) {
	channelScopes := []string{"search:read.public", "search:read.private"}

	t.Run("legacy unrestricted supports both", func(t *testing.T) {
		withChanTypes(t, SupportedChanTypes)
		inDM, with := SearchFilterSupport(SearchBackendLegacy, nil)
		assert.True(t, inDM)
		assert.True(t, with)
	})

	t.Run("legacy without DM types supports neither", func(t *testing.T) {
		withChanTypes(t, []string{ChanTypePublic, ChanTypePrivate})
		inDM, with := SearchFilterSupport(SearchBackendLegacy, nil)
		assert.False(t, inDM)
		assert.False(t, with)
	})

	t.Run("real-time with channel scopes supports neither", func(t *testing.T) {
		withChanTypes(t, SupportedChanTypes)
		inDM, with := SearchFilterSupport(SearchBackendRealTime, channelScopes)
		assert.False(t, inDM)
		assert.False(t, with)
	})

	t.Run("real-time with mpim scope supports in_im_or_mpim only", func(t *testing.T) {
		withChanTypes(t, SupportedChanTypes)
		inDM, with := SearchFilterSupport(SearchBackendRealTime, append(channelScopes, "search:read.mpim"))
		assert.True(t, inDM)
		assert.False(t, with)
	})

	t.Run("real-time mpim scope but mpim disabled", func(t *testing.T) {
		withChanTypes(t, []string{ChanTypePublic, ChanTypePrivate})
		inDM, _ := SearchFilterSupport(SearchBackendRealTime, append(channelScopes, "search:read.mpim"))
		assert.False(t, inDM)
	})
}

func newTestSearchClient(t *testing.T, handler http.HandlerFunc) *MCPSlackClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	ap, err := auth.NewValueAuth("xoxp-test-token", "")
	require.NoError(t, err)
	return &MCPSlackClient{
		httpClient:   srv.Client(),
		authProvider: ap,
		teamEndpoint: srv.URL + "/",
		isOAuth:      true,
	}
}

func TestUnitAssistantSearchContextRequest(t *testing.T) {
	var got *http.Request
	c := newTestSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		got = r
		_, _ = w.Write([]byte(`{"ok":true,"results":{"messages":[]}}`))
	})

	_, err := c.AssistantSearchContext(context.Background(), AssistantSearchContextRequest{
		Query:                 "deploy",
		ChannelTypes:          []string{ChanTypePublic, ChanTypePrivate},
		ContextChannelID:      "C1",
		Limit:                 20,
		After:                 1700000000,
		Before:                1800000000,
		DisableSemanticSearch: true,
	})
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, "/api/assistant.search.context", got.URL.Path)
	assert.Equal(t, "Bearer xoxp-test-token", got.Header.Get("Authorization"))
	assert.Equal(t, "deploy", got.PostForm.Get("query"))
	assert.Equal(t, "public_channel,private_channel", got.PostForm.Get("channel_types"))
	assert.Equal(t, "messages", got.PostForm.Get("content_types"))
	assert.Equal(t, "C1", got.PostForm.Get("context_channel_id"))
	assert.Equal(t, "20", got.PostForm.Get("limit"))
	assert.Equal(t, "1700000000", got.PostForm.Get("after"))
	assert.Equal(t, "1800000000", got.PostForm.Get("before"))
	assert.Equal(t, "true", got.PostForm.Get("disable_semantic_search"))
	assert.Empty(t, got.PostForm.Get("token"), "token must not be sent in the body")
}

func TestUnitAssistantSearchContextErrors(t *testing.T) {
	t.Run("missing_scope carries the needed scope", func(t *testing.T) {
		c := newTestSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"ok":false,"error":"missing_scope","needed":"search:read.public","provided":"channels:read"}`))
		})
		_, err := c.AssistantSearchContext(context.Background(), AssistantSearchContextRequest{Query: "x"})
		var apiErr *SlackAPIError
		require.True(t, errors.As(err, &apiErr))
		assert.Equal(t, "missing_scope", apiErr.Code)
		assert.Equal(t, "search:read.public", apiErr.Needed)
	})

	t.Run("unsafe error code is not echoed", func(t *testing.T) {
		c := newTestSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"ok":false,"error":"<script>alert(1)</script>"}`))
		})
		_, err := c.AssistantSearchContext(context.Background(), AssistantSearchContextRequest{Query: "x"})
		var apiErr *SlackAPIError
		require.True(t, errors.As(err, &apiErr))
		assert.Equal(t, "unknown_error", apiErr.Code)
	})

	t.Run("HTTP 429 is a retryable rate limit", func(t *testing.T) {
		c := newTestSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
		})
		_, err := c.AssistantSearchContext(context.Background(), AssistantSearchContextRequest{Query: "x"})
		var rle *slack.RateLimitedError
		require.True(t, errors.As(err, &rle))
		assert.Equal(t, 7*time.Second, rle.RetryAfter)
	})

	t.Run("redirects are not followed", func(t *testing.T) {
		var leaked atomic.Bool
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			leaked.Store(true)
		}))
		t.Cleanup(target.Close)
		c := newTestSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusFound)
		})
		_, err := c.AssistantSearchContext(context.Background(), AssistantSearchContextRequest{Query: "x"})
		require.Error(t, err)
		assert.False(t, leaked.Load(), "credentialed request must not follow a redirect")
	})
}

func TestUnitFetchOAuthScopes(t *testing.T) {
	t.Run("reads X-OAuth-Scopes", func(t *testing.T) {
		c := newTestSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/api/auth.test", r.URL.Path)
			w.Header().Set("X-OAuth-Scopes", "channels:read, search:read.public,search:read.private")
			_, _ = w.Write([]byte(`{"ok":true}`))
		})
		scopes, err := c.fetchOAuthScopes(context.Background())
		require.NoError(t, err)
		assert.Equal(t, []string{"channels:read", "search:read.public", "search:read.private"}, scopes)
	})

	t.Run("missing header is an error", func(t *testing.T) {
		c := newTestSearchClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"ok":true}`))
		})
		_, err := c.fetchOAuthScopes(context.Background())
		require.Error(t, err)
	})
}
