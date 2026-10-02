package provider

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rusq/slackdump/v3/auth"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testFileChannels = &ChannelsCache{
	Channels: map[string]Channel{
		"C1": {ID: "C1", Name: "#general"},
		"G1": {ID: "G1", Name: "#secret", IsPrivate: true},
		"D1": {ID: "D1", Name: "@alice", IsIM: true},
		"G2": {ID: "G2", Name: "mpdm-a--b-1", IsMpIM: true, IsPrivate: true},
	},
}

func TestUnitFileShareConversationIDs(t *testing.T) {
	f := &slack.File{
		Channels: []string{"C1", "C1"},
		Groups:   []string{"G1"},
		IMs:      []string{"D1"},
		Shares: slack.Share{
			Public:  map[string][]slack.ShareFileInfo{"C2": nil},
			Private: map[string][]slack.ShareFileInfo{"G1": nil, "D2": nil},
		},
	}
	assert.Equal(t, []string{"C1", "C2", "D1", "D2", "G1"}, FileShareConversationIDs(f))
	assert.Empty(t, FileShareConversationIDs(nil))
}

func TestUnitFileAllowed(t *testing.T) {
	restricted := []string{ChanTypePublic, ChanTypePrivate}

	tests := []struct {
		name  string
		types []string
		file  *slack.File
		want  bool
	}{
		{"public channel", restricted, &slack.File{Channels: []string{"C1"}}, true},
		{"private channel", restricted, &slack.File{Groups: []string{"G1"}}, true},
		{"only in an IM", restricted, &slack.File{IMs: []string{"D1"}}, false},
		{"only in an uncached IM", restricted, &slack.File{IMs: []string{"D9"}}, false},
		{"only in an MPIM", restricted, &slack.File{Groups: []string{"G2"}}, false},
		{"IM and public channel", restricted, &slack.File{IMs: []string{"D1"}, Channels: []string{"C1"}}, true},
		{"no shares", restricted, &slack.File{}, false},
		{"nil file", restricted, nil, false},
		{"channel missing from cache", restricted, &slack.File{Channels: []string{"C404"}}, false},
		{"only in shares.public", restricted, &slack.File{Shares: slack.Share{Public: map[string][]slack.ShareFileInfo{"C1": nil}}}, true},
		{"only in shares.private as an IM", restricted, &slack.File{Shares: slack.Share{Private: map[string][]slack.ShareFileInfo{"D1": nil}}}, false},
		{"public only: private channel rejected", []string{ChanTypePublic}, &slack.File{Groups: []string{"G1"}}, false},
		{"types unset: IM-only file allowed", SupportedChanTypes, &slack.File{IMs: []string{"D1"}}, true},
		{"types unset: no shares allowed", SupportedChanTypes, &slack.File{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withChanTypes(t, tt.types)
			assert.Equal(t, tt.want, FileAllowed(tt.file, testFileChannels))
		})
	}

	t.Run("no channels cache fails closed", func(t *testing.T) {
		withChanTypes(t, restricted)
		assert.False(t, FileAllowed(&slack.File{Channels: []string{"C1"}}, nil))
	})

	t.Run("error does not reveal the share", func(t *testing.T) {
		withChanTypes(t, restricted)
		ap := &ApiProvider{}
		ap.channelsSnapshot.Store(testFileChannels)
		err := ap.CheckFileAllowed(&slack.File{IMs: []string{"D1"}})
		require.ErrorIs(t, err, ErrChannelTypeNotAllowed)
		assert.NotContains(t, err.Error(), "D1")
		assert.NoError(t, ap.CheckFileAllowed(&slack.File{Channels: []string{"C1"}}))
	})
}

func TestUnitCheckFileDownloadURL(t *testing.T) {
	ok := func(raw string) error {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		return checkFileDownloadURL(u)
	}
	assert.NoError(t, ok("https://files.slack.com/files-pri/T1-F1/download/a.txt"))
	assert.NoError(t, ok("https://FILES.slack.com/files-pri/T1-F1/a.txt"))
	assert.Error(t, ok("http://files.slack.com/files-pri/T1-F1/a.txt"), "plain http")
	assert.Error(t, ok("https://files.slack.com.evil.example/a.txt"), "look-alike host")
	assert.Error(t, ok("https://evil.example/files.slack.com/a.txt"))
	assert.Error(t, ok("https://slack.com/a.txt"))

	t.Setenv("SLACK_MCP_GOVSLACK", "true")
	assert.NoError(t, ok("https://files.slack-gov.com/files-pri/T1-F1/a.txt"))
	assert.Error(t, ok("https://files.slack.com/files-pri/T1-F1/a.txt"))
}

// roundTripFunc serves requests for any host, so the download can be tested
// against https://files.slack.com without network access.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newFileTestClient(t *testing.T, rt roundTripFunc) *MCPSlackClient {
	t.Helper()
	ap, err := auth.NewValueAuth("xoxp-test-token", "")
	require.NoError(t, err)
	return &MCPSlackClient{httpClient: &http.Client{Transport: rt}, authProvider: ap}
}

func response(status int, header http.Header, body string) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

func TestUnitGetFileContext(t *testing.T) {
	const fileURL = "https://files.slack.com/files-pri/T1-F1/download/a.txt"

	t.Run("downloads from the Slack file host with the token", func(t *testing.T) {
		var auth string
		c := newFileTestClient(t, func(r *http.Request) (*http.Response, error) {
			auth = r.Header.Get("Authorization")
			return response(http.StatusOK, nil, "hello"), nil
		})
		var buf bytes.Buffer
		require.NoError(t, c.GetFileContext(context.Background(), fileURL, &buf))
		assert.Equal(t, "hello", buf.String())
		assert.Equal(t, "Bearer xoxp-test-token", auth)
	})

	t.Run("refuses a URL on another host before any request", func(t *testing.T) {
		called := false
		c := newFileTestClient(t, func(r *http.Request) (*http.Response, error) {
			called = true
			return response(http.StatusOK, nil, "x"), nil
		})
		err := c.GetFileContext(context.Background(), "https://evil.example/a.txt", io.Discard)
		require.Error(t, err)
		assert.False(t, called)
	})

	t.Run("refuses a redirect to another host", func(t *testing.T) {
		var hosts []string
		c := newFileTestClient(t, func(r *http.Request) (*http.Response, error) {
			hosts = append(hosts, r.URL.Host)
			return response(http.StatusFound, http.Header{"Location": {"https://evil.example/steal"}}, ""), nil
		})
		err := c.GetFileContext(context.Background(), fileURL, io.Discard)
		require.Error(t, err)
		assert.Equal(t, []string{"files.slack.com"}, hosts, "the token must never reach another host")
	})

	t.Run("follows a redirect on the Slack file host", func(t *testing.T) {
		var auths []string
		c := newFileTestClient(t, func(r *http.Request) (*http.Response, error) {
			auths = append(auths, r.Header.Get("Authorization"))
			if r.URL.Path == "/files-pri/T1-F1/download/a.txt" {
				return response(http.StatusFound, http.Header{"Location": {"https://files.slack.com/files-pri/T1-F1/a.txt"}}, ""), nil
			}
			return response(http.StatusOK, nil, "moved"), nil
		})
		var buf bytes.Buffer
		require.NoError(t, c.GetFileContext(context.Background(), fileURL, &buf))
		assert.Equal(t, "moved", buf.String())
		assert.Len(t, auths, 2)
	})

	t.Run("non-2xx is an error", func(t *testing.T) {
		c := newFileTestClient(t, func(r *http.Request) (*http.Response, error) {
			return response(http.StatusForbidden, nil, "no"), nil
		})
		assert.Error(t, c.GetFileContext(context.Background(), fileURL, io.Discard))
	})

	t.Run("download is capped", func(t *testing.T) {
		big := strings.Repeat("a", MaxFileDownloadBytes+1)
		c := newFileTestClient(t, func(r *http.Request) (*http.Response, error) {
			return response(http.StatusOK, nil, big), nil
		})
		assert.Error(t, c.GetFileContext(context.Background(), fileURL, io.Discard))
	})
}
