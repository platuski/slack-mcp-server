package provider

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestUnitDMScopes(t *testing.T) {
	safe := []string{"channels:read", "channels:history", "groups:read", "groups:history",
		"users:read", "users:read.email", "files:read", "search:read.public", "search:read.private", "search:read.files"}
	assert.Empty(t, DMScopes(safe))
	assert.Equal(t, []string{"im:history", "search:read"},
		DMScopes(append(append([]string{}, safe...), "im:history", "search:read")))
	assert.Equal(t, []string{"reactions:read"}, DMScopes([]string{"reactions:read", "emoji:read"}))
}

func TestUnitLogOAuthScopes(t *testing.T) {
	t.Run("channel scopes: info only", func(t *testing.T) {
		core, logs := observer.New(zap.InfoLevel)
		logOAuthScopes(zap.New(core), []string{"channels:read", "search:read.public"})
		assert.Equal(t, 1, logs.FilterMessage("OAuth token scopes").Len())
		assert.Zero(t, logs.FilterLevelExact(zap.WarnLevel).Len())
	})

	t.Run("DM scope: warning names it", func(t *testing.T) {
		core, logs := observer.New(zap.InfoLevel)
		logOAuthScopes(zap.New(core), []string{"channels:read", "im:history"})
		warns := logs.FilterLevelExact(zap.WarnLevel).All()
		if assert.Len(t, warns, 1) {
			assert.Equal(t, []interface{}{"im:history"}, warns[0].ContextMap()["scopes"])
		}
	})
}

func TestUnitSlackAppManifestIsDMSafe(t *testing.T) {
	raw, err := os.ReadFile("../../slack-app-manifest.json")
	require.NoError(t, err)
	var manifest struct {
		OAuthConfig struct {
			Scopes struct {
				User []string `json:"user"`
				Bot  []string `json:"bot"`
			} `json:"scopes"`
		} `json:"oauth_config"`
	}
	require.NoError(t, json.Unmarshal(raw, &manifest))
	scopes := manifest.OAuthConfig.Scopes.User

	assert.Empty(t, DMScopes(scopes), "the manifest must not request DM scopes")
	assert.Empty(t, manifest.OAuthConfig.Scopes.Bot, "the server uses a user token only")
	for _, s := range []string{"channels:read", "channels:history", "groups:read", "groups:history",
		"users:read", "search:read.public", "search:read.private"} {
		assert.Contains(t, scopes, s)
	}
	assert.NotContains(t, scopes, "search:read", "legacy search:read also covers DMs")
}
