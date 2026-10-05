package provider

import (
	"slices"

	"go.uber.org/zap"
)

// dmScopes are user token scopes that give access to DM or group DM content.
var dmScopes = []string{
	"im:history", "im:read", "mpim:history", "mpim:read",
	"search:read", "search:read.im", "search:read.mpim",
	"reactions:read",
}

// DMScopes returns the granted scopes that can reach DM or group DM content.
// files:read is not listed: it is not limited by conversation type either,
// but attachment_get_data guards it when SLACK_MCP_CHANNEL_TYPES is set.
func DMScopes(scopes []string) []string {
	var found []string
	for _, s := range scopes {
		if slices.Contains(dmScopes, s) {
			found = append(found, s)
		}
	}
	return found
}

// logOAuthScopes logs the scopes granted to a user OAuth token, and warns
// about scopes that reach DMs or group DMs. Scope names only, no secrets.
func logOAuthScopes(logger *zap.Logger, scopes []string) {
	logger.Info("OAuth token scopes",
		zap.Strings("scopes", scopes),
		zap.String("context", "console"),
	)
	if dm := DMScopes(scopes); len(dm) > 0 {
		logger.Warn("OAuth token has scopes that can read DMs or group DMs",
			zap.Strings("scopes", dm),
			zap.Strings("enabled_types", AllChanTypes),
			zap.String("context", "console"),
		)
	}
}
