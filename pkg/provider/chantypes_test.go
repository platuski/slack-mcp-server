package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func withChanTypes(t *testing.T, types []string) {
	t.Helper()
	prev := append([]string(nil), AllChanTypes...)
	SetChanTypes(types)
	t.Cleanup(func() { SetChanTypes(prev) })
}

func TestUnitParseChanTypes(t *testing.T) {
	t.Run("empty returns all supported types", func(t *testing.T) {
		got, err := ParseChanTypes("  ")
		require.NoError(t, err)
		assert.Equal(t, SupportedChanTypes, got)
	})

	t.Run("subset keeps canonical order, trims and lowercases", func(t *testing.T) {
		got, err := ParseChanTypes(" Private_Channel , public_channel,,")
		require.NoError(t, err)
		assert.Equal(t, []string{ChanTypePublic, ChanTypePrivate}, got)
	})

	t.Run("duplicates collapse", func(t *testing.T) {
		got, err := ParseChanTypes("im,im,mpim")
		require.NoError(t, err)
		assert.Equal(t, []string{ChanTypeMPIM, ChanTypeIM}, got)
	})

	t.Run("unknown type is rejected", func(t *testing.T) {
		_, err := ParseChanTypes("public_channel,dm")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"dm"`)
	})

	t.Run("only separators is rejected", func(t *testing.T) {
		_, err := ParseChanTypes(",,")
		require.Error(t, err)
	})
}

func TestUnitChanTypeOf(t *testing.T) {
	assert.Equal(t, ChanTypeIM, ChanTypeOf(true, false, false))
	assert.Equal(t, ChanTypeMPIM, ChanTypeOf(false, true, true))
	assert.Equal(t, ChanTypePrivate, ChanTypeOf(false, false, true))
	assert.Equal(t, ChanTypePublic, ChanTypeOf(false, false, false))
}

func TestUnitChanTypesRestriction(t *testing.T) {
	withChanTypes(t, []string{ChanTypePublic, ChanTypePrivate})

	assert.False(t, AllChanTypesAllowed())
	assert.Equal(t, []string{ChanTypePublic, ChanTypePrivate}, AllChanTypes)

	assert.True(t, IsChannelAllowed(Channel{ID: "C1"}))
	assert.True(t, IsChannelAllowed(Channel{ID: "G1", IsPrivate: true}))
	assert.False(t, IsChannelAllowed(Channel{ID: "D1", IsIM: true}))
	assert.False(t, IsChannelAllowed(Channel{ID: "G2", IsMpIM: true, IsPrivate: true}))

	assert.True(t, IsSearchChannelAllowed(slack.CtxChannel{ID: "C1"}))
	assert.True(t, IsSearchChannelAllowed(slack.CtxChannel{ID: "C2", IsPrivate: true}))
	assert.False(t, IsSearchChannelAllowed(slack.CtxChannel{ID: "D1"}))
	assert.False(t, IsSearchChannelAllowed(slack.CtxChannel{ID: "C3", IsMPIM: true, IsPrivate: true}))
}

func TestUnitCheckChannelTypeAllowed(t *testing.T) {
	ap := &ApiProvider{}
	ap.channelsSnapshot.Store(&ChannelsCache{
		Channels: map[string]Channel{
			"C1": {ID: "C1", Name: "#general"},
			"G1": {ID: "G1", Name: "mpdm-a--b-1", IsMpIM: true, IsPrivate: true},
		},
		ChannelsInv: map[string]string{"#general": "C1"},
	})
	ctx := context.Background()

	t.Run("no restriction allows everything without lookups", func(t *testing.T) {
		withChanTypes(t, SupportedChanTypes)
		assert.NoError(t, ap.CheckChannelTypeAllowed(ctx, "D123"))
		assert.NoError(t, ap.CheckChannelTypeAllowed(ctx, "UNKNOWN"))
	})

	t.Run("restricted to channels", func(t *testing.T) {
		withChanTypes(t, []string{ChanTypePublic, ChanTypePrivate})

		assert.NoError(t, ap.CheckChannelTypeAllowed(ctx, "C1"))

		err := ap.CheckChannelTypeAllowed(ctx, "D123")
		assert.True(t, errors.Is(err, ErrChannelTypeNotAllowed))

		err = ap.CheckChannelTypeAllowed(ctx, "G1")
		assert.True(t, errors.Is(err, ErrChannelTypeNotAllowed))
	})

	t.Run("DM prefix passes when im is enabled", func(t *testing.T) {
		withChanTypes(t, []string{ChanTypeIM, ChanTypePublic})
		ap2 := &ApiProvider{}
		ap2.channelsSnapshot.Store(&ChannelsCache{
			Channels:    map[string]Channel{"D9": {ID: "D9", IsIM: true}},
			ChannelsInv: map[string]string{},
		})
		assert.NoError(t, ap2.CheckChannelTypeAllowed(ctx, "D9"))
	})
}

func TestUnitResolveRequestedChanTypes(t *testing.T) {
	logger := zap.NewNop()

	t.Run("only disabled types requested returns an error", func(t *testing.T) {
		withChanTypes(t, []string{ChanTypePublic, ChanTypePrivate})
		got, err := ResolveRequestedChanTypes("im,mpim", logger)
		require.Error(t, err)
		assert.Nil(t, got)
		assert.True(t, errors.Is(err, ErrChannelTypeNotAllowed))
		assert.Contains(t, err.Error(), "im,mpim")
		assert.Contains(t, err.Error(), "public_channel,private_channel")
	})

	t.Run("mixed request keeps enabled types", func(t *testing.T) {
		withChanTypes(t, []string{ChanTypePublic, ChanTypePrivate})
		got, err := ResolveRequestedChanTypes("im, public_channel ,mpim", logger)
		require.NoError(t, err)
		assert.Equal(t, []string{ChanTypePublic}, got)
	})

	t.Run("empty request uses defaults", func(t *testing.T) {
		withChanTypes(t, SupportedChanTypes)
		got, err := ResolveRequestedChanTypes("", logger)
		require.NoError(t, err)
		assert.Equal(t, []string{ChanTypePublic, ChanTypePrivate}, got)
	})

	t.Run("unknown only request uses defaults", func(t *testing.T) {
		withChanTypes(t, SupportedChanTypes)
		got, err := ResolveRequestedChanTypes("dm,channel", logger)
		require.NoError(t, err)
		assert.Equal(t, []string{ChanTypePublic, ChanTypePrivate}, got)
	})

	t.Run("defaults limited to enabled types", func(t *testing.T) {
		withChanTypes(t, []string{ChanTypePublic})
		got, err := ResolveRequestedChanTypes("", logger)
		require.NoError(t, err)
		assert.Equal(t, []string{ChanTypePublic}, got)
	})

	t.Run("defaults fully disabled fall back to AllChanTypes", func(t *testing.T) {
		withChanTypes(t, []string{ChanTypeMPIM, ChanTypeIM})
		got, err := ResolveRequestedChanTypes("bogus", logger)
		require.NoError(t, err)
		assert.Equal(t, []string{ChanTypeMPIM, ChanTypeIM}, got)
	})

	t.Run("empty request with only DM types enabled uses AllChanTypes", func(t *testing.T) {
		withChanTypes(t, []string{ChanTypeMPIM, ChanTypeIM})
		got, err := ResolveRequestedChanTypes("", logger)
		require.NoError(t, err)
		assert.Equal(t, []string{ChanTypeMPIM, ChanTypeIM}, got)
	})

	t.Run("unrestricted allows im and mpim", func(t *testing.T) {
		withChanTypes(t, SupportedChanTypes)
		got, err := ResolveRequestedChanTypes("im,mpim", logger)
		require.NoError(t, err)
		assert.Equal(t, []string{ChanTypeIM, ChanTypeMPIM}, got)
	})

	t.Run("duplicates collapse", func(t *testing.T) {
		withChanTypes(t, SupportedChanTypes)
		got, err := ResolveRequestedChanTypes("im,public_channel, im ,public_channel", logger)
		require.NoError(t, err)
		assert.Equal(t, []string{ChanTypeIM, ChanTypePublic}, got)
	})
}
