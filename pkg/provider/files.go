package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/slack-go/slack"
)

// files:read is not limited by conversation type: files.info and the file
// download succeed for files that were only shared in a DM. When
// SLACK_MCP_CHANNEL_TYPES excludes any type, a file is therefore only returned
// when it is shared in a cached conversation of an enabled type.

// MaxFileDownloadBytes caps the size of a downloaded file.
const MaxFileDownloadBytes = 5 * 1024 * 1024

// ErrFileNotAllowed is returned for a file that is not shared in any
// conversation of an enabled type. It does not say where the file is shared.
var ErrFileNotAllowed = fmt.Errorf("%w: the file is not shared in an enabled conversation", ErrChannelTypeNotAllowed)

// FileShareConversationIDs returns the IDs of all conversations a file is
// shared in, from files.info's channels, groups and ims lists and its
// shares.public / shares.private maps, sorted and without duplicates.
func FileShareConversationIDs(f *slack.File) []string {
	if f == nil {
		return nil
	}
	seen := map[string]bool{}
	add := func(id string) {
		if id = strings.TrimSpace(id); id != "" {
			seen[id] = true
		}
	}
	for _, list := range [][]string{f.Channels, f.Groups, f.IMs} {
		for _, id := range list {
			add(id)
		}
	}
	for id := range f.Shares.Public {
		add(id)
	}
	for id := range f.Shares.Private {
		add(id)
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// FileAllowed reports whether a file may be returned. Without a
// SLACK_MCP_CHANNEL_TYPES restriction every file is allowed (upstream
// behavior). Otherwise the file must be shared in at least one conversation
// that is in the channels cache and of an enabled type. No shares, missing
// share data, DM IDs and conversations absent from the cache all fail closed.
func FileAllowed(f *slack.File, channels *ChannelsCache) bool {
	if AllChanTypesAllowed() {
		return true
	}
	if channels == nil {
		return false
	}
	for _, id := range FileShareConversationIDs(f) {
		if strings.HasPrefix(id, "D") && !IsChanTypeAllowed(ChanTypeIM) {
			continue
		}
		if c, ok := channels.Channels[id]; ok && IsChannelAllowed(c) {
			return true
		}
	}
	return false
}

// CheckFileAllowed returns ErrFileNotAllowed unless FileAllowed.
func (ap *ApiProvider) CheckFileAllowed(f *slack.File) error {
	if FileAllowed(f, ap.ProvideChannelsMaps()) {
		return nil
	}
	return ErrFileNotAllowed
}

// fileDownloadHosts returns the hosts Slack serves private files from.
func fileDownloadHosts() []string {
	if os.Getenv("SLACK_MCP_GOVSLACK") == "true" {
		return []string{"files.slack-gov.com"}
	}
	return []string{"files.slack.com"}
}

// checkFileDownloadURL accepts only https URLs on a Slack file host.
func checkFileDownloadURL(u *url.URL) error {
	if u == nil || u.Scheme != "https" {
		return fmt.Errorf("file download URL must use https")
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range fileDownloadHosts() {
		if host == h {
			return nil
		}
	}
	return fmt.Errorf("file download host %q is not a Slack file host", host)
}

// GetFileContext downloads a private file. The token is only sent to Slack's
// file host: the URL must be on it, and a redirect to any other host or to a
// non-https URL is refused rather than followed. The download is capped at
// MaxFileDownloadBytes.
func (c *MCPSlackClient) GetFileContext(ctx context.Context, downloadURL string, writer io.Writer) error {
	u, err := url.Parse(downloadURL)
	if err != nil {
		return fmt.Errorf("invalid file download URL: %w", err)
	}
	if err := checkFileDownloadURL(u); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("create file download request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.authProvider.SlackToken())

	httpClient := *c.httpClient
	httpClient.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects downloading file")
		}
		return checkFileDownloadURL(r.URL)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("file download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("file download: HTTP %d", resp.StatusCode)
	}
	n, err := io.Copy(writer, io.LimitReader(resp.Body, MaxFileDownloadBytes+1))
	if err != nil {
		return fmt.Errorf("file download: %w", err)
	}
	if n > MaxFileDownloadBytes {
		return fmt.Errorf("file is larger than %d bytes", MaxFileDownloadBytes)
	}
	return nil
}
