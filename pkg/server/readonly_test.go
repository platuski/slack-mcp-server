package server

import (
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestUnitReadOnlyMode(t *testing.T) {
	for value, want := range map[string]bool{"": false, "false": false, "0": false, "true": true, "1": true, "yes": true, " TRUE ": true} {
		t.Setenv(ReadOnlyEnv, value)
		assert.Equal(t, want, ReadOnlyMode(), "value %q", value)
	}
}

func registeredTools(t *testing.T, enabledTools []string) map[string]bool {
	t.Helper()
	// Demo credentials give a provider that answers auth.test offline.
	t.Setenv("SLACK_MCP_XOXP_TOKEN", "demo")
	s := NewMCPServer(provider.New("stdio", zap.NewNop()), zap.NewNop(), enabledTools)
	names := map[string]bool{}
	for name := range s.server.ListTools() {
		names[name] = true
	}
	return names
}

func TestUnitReadOnlyRegistersNoWriteTools(t *testing.T) {
	// Every way of enabling write tools is set; read-only mode must win.
	t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "true")
	t.Setenv("SLACK_MCP_REACTION_TOOL", "true")
	t.Setenv("SLACK_MCP_ATTACHMENT_TOOL", "true")

	t.Run("read-only on", func(t *testing.T) {
		t.Setenv(ReadOnlyEnv, "true")
		tools := registeredTools(t, nil)
		for _, w := range WriteTools {
			assert.False(t, tools[w], "write tool %s must not be registered", w)
		}
		for _, r := range []string{ToolConversationsHistory, ToolConversationsReplies, ToolChannelsList, ToolUsersSearch, ToolConversationsUnreads, ToolAttachmentGetData} {
			assert.True(t, tools[r], "read tool %s must stay registered", r)
		}
	})

	t.Run("read-only on beats an explicit enabled tools list", func(t *testing.T) {
		t.Setenv(ReadOnlyEnv, "true")
		tools := registeredTools(t, []string{ToolConversationsHistory, ToolConversationsAddMessage, ToolConversationsJoin})
		assert.Equal(t, map[string]bool{ToolConversationsHistory: true}, tools)
	})

	t.Run("read-only off keeps upstream registration", func(t *testing.T) {
		t.Setenv(ReadOnlyEnv, "")
		tools := registeredTools(t, nil)
		for _, w := range WriteTools {
			assert.True(t, tools[w], "write tool %s is registered upstream", w)
		}
	})
}

func TestUnitWriteToolsAreKnownTools(t *testing.T) {
	for _, w := range WriteTools {
		assert.Contains(t, ValidToolNames, w)
	}
	assert.False(t, IsWriteTool(ToolConversationsUnreads), "unreads does not mark messages as read")
	assert.False(t, IsWriteTool(ToolAttachmentGetData))
}
