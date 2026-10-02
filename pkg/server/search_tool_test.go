package server

import (
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
)

func withServerChanTypes(t *testing.T, types []string) {
	t.Helper()
	prev := append([]string(nil), provider.AllChanTypes...)
	provider.SetChanTypes(types)
	t.Cleanup(func() { provider.SetChanTypes(prev) })
}

func searchTool(backend provider.SearchBackend, scopes []string) mcp.Tool {
	return mcp.NewTool(ToolConversationsSearchMessages, searchToolOptions(backend, scopes)...)
}

func TestUnitSearchToolSchema(t *testing.T) {
	channelScopes := []string{"search:read.public", "search:read.private"}

	t.Run("legacy unrestricted keeps the upstream schema", func(t *testing.T) {
		withServerChanTypes(t, provider.SupportedChanTypes)
		tool := searchTool(provider.SearchBackendLegacy, nil)
		assert.Contains(t, tool.InputSchema.Properties, "filter_in_im_or_mpim")
		assert.Contains(t, tool.InputSchema.Properties, "filter_users_with")
		assert.Contains(t, tool.Description, "direct message (DM, or IM)")
		assert.Len(t, tool.InputSchema.Properties, 12)
	})

	t.Run("legacy without DM types hides DM filters", func(t *testing.T) {
		withServerChanTypes(t, []string{provider.ChanTypePublic, provider.ChanTypePrivate})
		tool := searchTool(provider.SearchBackendLegacy, nil)
		assert.NotContains(t, tool.InputSchema.Properties, "filter_in_im_or_mpim")
		assert.NotContains(t, tool.InputSchema.Properties, "filter_users_with")
		assert.NotContains(t, tool.Description, "direct message")
	})

	t.Run("real-time with channel scopes hides DM filters", func(t *testing.T) {
		withServerChanTypes(t, provider.SupportedChanTypes)
		tool := searchTool(provider.SearchBackendRealTime, channelScopes)
		assert.NotContains(t, tool.InputSchema.Properties, "filter_in_im_or_mpim")
		assert.NotContains(t, tool.InputSchema.Properties, "filter_users_with")
		assert.Contains(t, tool.Description, "public_channel, private_channel")
		assert.NotContains(t, tool.Description, "mpim")
	})

	t.Run("real-time with mpim scope keeps filter_in_im_or_mpim only", func(t *testing.T) {
		withServerChanTypes(t, provider.SupportedChanTypes)
		tool := searchTool(provider.SearchBackendRealTime, append(channelScopes, "search:read.mpim"))
		assert.Contains(t, tool.InputSchema.Properties, "filter_in_im_or_mpim")
		assert.NotContains(t, tool.InputSchema.Properties, "filter_users_with")
	})
}

func TestUnitHasSearchTool(t *testing.T) {
	assert.False(t, hasSearchTool(provider.SearchBackendNone))
	assert.True(t, hasSearchTool(provider.SearchBackendLegacy))
	assert.True(t, hasSearchTool(provider.SearchBackendRealTime))
}
