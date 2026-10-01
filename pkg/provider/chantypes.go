package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

// ChannelTypesEnv is the environment variable that restricts which Slack
// conversation types the server exposes. It accepts a comma separated list of
// public_channel, private_channel, im and mpim. When unset or empty, all four
// types are exposed (upstream default behavior).
const ChannelTypesEnv = "SLACK_MCP_CHANNEL_TYPES"

const (
	ChanTypePublic  = "public_channel"
	ChanTypePrivate = "private_channel"
	ChanTypeIM      = "im"
	ChanTypeMPIM    = "mpim"
)

// SupportedChanTypes lists every conversation type the server understands.
var SupportedChanTypes = []string{ChanTypeMPIM, ChanTypeIM, ChanTypePublic, ChanTypePrivate}

// ErrChannelTypeNotAllowed is returned when a request targets a conversation
// whose type is excluded by SLACK_MCP_CHANNEL_TYPES.
var ErrChannelTypeNotAllowed = errors.New("conversation type is not allowed by " + ChannelTypesEnv)

// AllChanTypes holds the conversation types enabled for this process.
// It is resolved once from SLACK_MCP_CHANNEL_TYPES at startup.
var AllChanTypes []string

// chanTypesErr keeps a parse error so New() can fail loudly with the logger.
var chanTypesErr error

var allowedChanTypes map[string]bool

func init() {
	types, err := ParseChanTypes(os.Getenv(ChannelTypesEnv))
	if err != nil {
		chanTypesErr = err
		// Fail closed: expose public channels only until New() aborts startup.
		types = []string{ChanTypePublic}
	}
	SetChanTypes(types)
}

// ParseChanTypes parses a comma separated list of conversation types.
// An empty value returns all supported types. Unknown values are rejected.
// The result keeps the canonical order of SupportedChanTypes.
func ParseChanTypes(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return append([]string(nil), SupportedChanTypes...), nil
	}

	supported := make(map[string]bool, len(SupportedChanTypes))
	for _, t := range SupportedChanTypes {
		supported[t] = true
	}

	requested := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		t := strings.ToLower(strings.TrimSpace(part))
		if t == "" {
			continue
		}
		if !supported[t] {
			return nil, fmt.Errorf("%s: unknown conversation type %q (supported: %s)",
				ChannelTypesEnv, t, strings.Join(SupportedChanTypes, ","))
		}
		requested[t] = true
	}
	if len(requested) == 0 {
		return nil, fmt.Errorf("%s: no conversation types given", ChannelTypesEnv)
	}

	var res []string
	for _, t := range SupportedChanTypes {
		if requested[t] {
			res = append(res, t)
		}
	}
	return res, nil
}

// SetChanTypes replaces the enabled conversation types. Intended for startup
// and tests; it is not safe to call concurrently with request handling.
func SetChanTypes(types []string) {
	AllChanTypes = append([]string(nil), types...)
	allowedChanTypes = make(map[string]bool, len(types))
	for _, t := range types {
		allowedChanTypes[t] = true
	}
}

// defaultRequestChanTypes is used when a request names no usable type.
var defaultRequestChanTypes = []string{ChanTypePublic, ChanTypePrivate}

// ResolveRequestedChanTypes turns a channel_types request parameter into the
// list of types to query. Unknown values are ignored. Supported but disabled
// values are dropped when at least one enabled type remains; when only
// disabled types are requested, an error wrapping ErrChannelTypeNotAllowed is
// returned. When nothing usable is requested, public_channel,private_channel
// limited to the enabled types is used, or AllChanTypes if that is empty.
func ResolveRequestedChanTypes(raw string, logger *zap.Logger) ([]string, error) {
	supported := make(map[string]bool, len(SupportedChanTypes))
	for _, t := range SupportedChanTypes {
		supported[t] = true
	}

	seen := make(map[string]bool)
	var enabled, disabled []string
	for _, part := range strings.Split(raw, ",") {
		t := strings.TrimSpace(part)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		switch {
		case !supported[t]:
			logger.Warn("Invalid channel type ignored", zap.String("type", t))
		case IsChanTypeAllowed(t):
			enabled = append(enabled, t)
		default:
			disabled = append(disabled, t)
		}
	}

	if len(enabled) > 0 {
		if len(disabled) > 0 {
			logger.Warn("Channel types excluded by "+ChannelTypesEnv+" ignored",
				zap.Strings("types", disabled),
				zap.Strings("allowed", AllChanTypes),
			)
		}
		return enabled, nil
	}

	if len(disabled) > 0 {
		return nil, fmt.Errorf("%w: requested %s, enabled types are %s", ErrChannelTypeNotAllowed,
			strings.Join(disabled, ","), strings.Join(AllChanTypes, ","))
	}

	var defaults []string
	for _, t := range defaultRequestChanTypes {
		if IsChanTypeAllowed(t) {
			defaults = append(defaults, t)
		}
	}
	if len(defaults) == 0 {
		defaults = append([]string(nil), AllChanTypes...)
	}
	logger.Debug("No valid channel types provided, using defaults", zap.Strings("types", defaults))
	return defaults, nil
}

// ChanTypesConfigError returns the SLACK_MCP_CHANNEL_TYPES parse error, if any.
func ChanTypesConfigError() error {
	return chanTypesErr
}

// IsChanTypeAllowed reports whether the given conversation type is enabled.
func IsChanTypeAllowed(t string) bool {
	return allowedChanTypes[t]
}

// AllChanTypesAllowed reports whether no restriction is configured.
func AllChanTypesAllowed() bool {
	for _, t := range SupportedChanTypes {
		if !allowedChanTypes[t] {
			return false
		}
	}
	return true
}

// ChanTypeOf classifies a conversation by its flags.
func ChanTypeOf(isIM, isMpIM, isPrivate bool) string {
	switch {
	case isIM:
		return ChanTypeIM
	case isMpIM:
		return ChanTypeMPIM
	case isPrivate:
		return ChanTypePrivate
	default:
		return ChanTypePublic
	}
}

// IsChannelAllowed reports whether a cached channel's type is enabled.
func IsChannelAllowed(c Channel) bool {
	return IsChanTypeAllowed(ChanTypeOf(c.IsIM, c.IsMpIM, c.IsPrivate))
}

// IsSearchChannelAllowed reports whether a search match's channel type is
// enabled. Search results carry no explicit IM flag; IM IDs start with "D".
func IsSearchChannelAllowed(c slack.CtxChannel) bool {
	isIM := strings.HasPrefix(c.ID, "D")
	return IsChanTypeAllowed(ChanTypeOf(isIM, c.IsMPIM, c.IsPrivate))
}

// CheckChannelTypeAllowed verifies that the conversation with the given ID is
// of an enabled type. It uses the channels cache first and falls back to
// conversations.info. It fails closed when the type cannot be determined.
func (ap *ApiProvider) CheckChannelTypeAllowed(ctx context.Context, channelID string) error {
	if AllChanTypesAllowed() {
		return nil
	}
	if channelID == "" {
		return nil
	}

	if strings.HasPrefix(channelID, "D") && !IsChanTypeAllowed(ChanTypeIM) {
		return fmt.Errorf("%w: %s is a direct message", ErrChannelTypeNotAllowed, channelID)
	}

	if snap := ap.ProvideChannelsMaps(); snap != nil {
		if c, ok := snap.Channels[channelID]; ok {
			if IsChannelAllowed(c) {
				return nil
			}
			return fmt.Errorf("%w: %s is of type %s", ErrChannelTypeNotAllowed, channelID,
				ChanTypeOf(c.IsIM, c.IsMpIM, c.IsPrivate))
		}
	}

	info, err := ap.Slack().GetConversationInfoContext(ctx, &slack.GetConversationInfoInput{
		ChannelID: channelID,
	})
	if err != nil {
		return fmt.Errorf("%w: unable to determine type of %s: %v", ErrChannelTypeNotAllowed, channelID, err)
	}
	t := ChanTypeOf(info.IsIM, info.IsMpIM, info.IsPrivate)
	if !IsChanTypeAllowed(t) {
		return fmt.Errorf("%w: %s is of type %s", ErrChannelTypeNotAllowed, channelID, t)
	}
	return nil
}
