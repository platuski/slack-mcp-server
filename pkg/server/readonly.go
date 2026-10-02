package server

import (
	"os"
	"slices"
	"strings"
)

// ReadOnlyEnv turns on read-only mode: write tools are not registered, even
// when SLACK_MCP_ENABLED_TOOLS or a tool's own environment variable enables
// them. Off by default (upstream behavior).
const ReadOnlyEnv = "SLACK_MCP_READ_ONLY"

// WriteTools are the tools that change Slack state.
var WriteTools = []string{
	ToolConversationsAddMessage,
	ToolReactionsAdd,
	ToolReactionsRemove,
	ToolConversationsMark,
	ToolConversationsJoin,
	ToolConversationsLeave,
	ToolUsergroupsCreate,
	ToolUsergroupsUpdate,
	ToolUsergroupsUsersUpdate,
	ToolSavedUpdate,
	ToolSavedClearCompleted,
}

// ReadOnlyMode reports whether SLACK_MCP_READ_ONLY is true, 1 or yes.
func ReadOnlyMode() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(ReadOnlyEnv))) {
	case "true", "1", "yes":
		return true
	}
	return false
}

// IsWriteTool reports whether a tool changes Slack state.
func IsWriteTool(name string) bool {
	return slices.Contains(WriteTools, name)
}

// shouldRegisterTool applies read-only mode on top of shouldAddTool.
func shouldRegisterTool(name string, enabledTools []string, readOnly bool, envVarName string) bool {
	if readOnly && IsWriteTool(name) {
		return false
	}
	return shouldAddTool(name, enabledTools, envVarName)
}
