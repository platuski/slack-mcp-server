package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/slack-go/slack"
)

const emailScope = "users:read.email"

// ErrEmailScopeMissing is returned for email queries when the token lacks
// users:read.email, without which Slack omits users' email addresses.
var ErrEmailScopeMissing = errors.New("looking users up by email needs the " + emailScope +
	" scope; add it to the Slack app's User Token Scopes and reinstall the app")

var emailQueryPattern = regexp.MustCompile(`^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$`)

// IsEmailQuery reports whether a users_search query is an email address.
func IsEmailQuery(query string) bool {
	return emailQueryPattern.MatchString(strings.TrimSpace(query))
}

// searchUsersByEmail matches an email address exactly (case-insensitive)
// against the users cache and falls back to users.lookupByEmail on a miss,
// for example while the cache still holds entries fetched before the scope
// was granted. scopes and known come from OAuthScopes.
func (ap *ApiProvider) searchUsersByEmail(ctx context.Context, email string, scopes []string, known bool) ([]slack.User, error) {
	email = strings.TrimSpace(email)
	if known && !containsScope(scopes, emailScope) {
		return nil, ErrEmailScopeMissing
	}
	if !ap.usersReady.Load() {
		return nil, ErrUsersNotReady
	}

	for _, u := range ap.usersSnapshot.Load().Users {
		if !u.Deleted && strings.EqualFold(strings.TrimSpace(u.Profile.Email), email) {
			return []slack.User{u}, nil
		}
	}

	u, err := ap.client.GetUserByEmailContext(ctx, email)
	if err != nil {
		var slackErr slack.SlackErrorResponse
		if errors.As(err, &slackErr) {
			switch slackErr.Err {
			case "users_not_found":
				return nil, nil
			case "missing_scope":
				return nil, ErrEmailScopeMissing
			}
		}
		return nil, fmt.Errorf("users.lookupByEmail: %w", err)
	}
	if u == nil || u.Deleted {
		return nil, nil
	}
	return []slack.User{*u}, nil
}

func containsScope(scopes []string, scope string) bool {
	for _, s := range scopes {
		if s == scope {
			return true
		}
	}
	return false
}
