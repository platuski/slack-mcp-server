package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type emailLookupMock struct {
	SlackAPI
	user  *slack.User
	err   error
	calls int
}

func (m *emailLookupMock) GetUserByEmailContext(ctx context.Context, email string) (*slack.User, error) {
	m.calls++
	return m.user, m.err
}

func newEmailTestProvider(mock *emailLookupMock) *ApiProvider {
	ap := &ApiProvider{client: mock}
	users := map[string]slack.User{
		"U1": {ID: "U1", Name: "ana", Profile: slack.UserProfile{Email: "Ana@Example.com"}},
		"U2": {ID: "U2", Name: "hana", Profile: slack.UserProfile{Email: "hana@example.com"}},
		"U3": {ID: "U3", Name: "gone", Deleted: true, Profile: slack.UserProfile{Email: "gone@example.com"}},
	}
	ap.usersSnapshot.Store(&UsersCache{Users: users, UsersInv: map[string]string{}})
	ap.usersReady.Store(true)
	return ap
}

func TestUnitIsEmailQuery(t *testing.T) {
	assert.True(t, IsEmailQuery("ana@example.com"))
	assert.True(t, IsEmailQuery(" first.last+tag@sub.example.co "))
	assert.False(t, IsEmailQuery("ana"))
	assert.False(t, IsEmailQuery("@ana"))
	assert.False(t, IsEmailQuery("ana@localhost"))
	assert.False(t, IsEmailQuery("ana smith@example.com"))
	assert.False(t, IsEmailQuery("<mailto:ana@example.com>"))
}

func TestUnitSearchUsersByEmail(t *testing.T) {
	scopes := []string{"users:read", "users:read.email"}
	ctx := context.Background()

	t.Run("exact case-insensitive match from the cache", func(t *testing.T) {
		mock := &emailLookupMock{}
		users, err := newEmailTestProvider(mock).searchUsersByEmail(ctx, "ana@example.com", scopes, true)
		require.NoError(t, err)
		require.Len(t, users, 1)
		assert.Equal(t, "U1", users[0].ID, "hana@example.com must not match as a substring")
		assert.Zero(t, mock.calls)
	})

	t.Run("missing scope names users:read.email", func(t *testing.T) {
		_, err := newEmailTestProvider(&emailLookupMock{}).searchUsersByEmail(ctx, "ana@example.com", []string{"users:read"}, true)
		require.ErrorIs(t, err, ErrEmailScopeMissing)
		assert.Contains(t, err.Error(), "users:read.email")
	})

	t.Run("cache miss falls back to users.lookupByEmail", func(t *testing.T) {
		mock := &emailLookupMock{user: &slack.User{ID: "U9", Name: "new"}}
		users, err := newEmailTestProvider(mock).searchUsersByEmail(ctx, "new@example.com", scopes, true)
		require.NoError(t, err)
		require.Len(t, users, 1)
		assert.Equal(t, "U9", users[0].ID)
		assert.Equal(t, 1, mock.calls)
	})

	t.Run("deleted user in the cache is skipped", func(t *testing.T) {
		mock := &emailLookupMock{err: slack.SlackErrorResponse{Err: "users_not_found"}}
		users, err := newEmailTestProvider(mock).searchUsersByEmail(ctx, "gone@example.com", scopes, true)
		require.NoError(t, err)
		assert.Empty(t, users)
	})

	t.Run("users_not_found is an empty result", func(t *testing.T) {
		mock := &emailLookupMock{err: slack.SlackErrorResponse{Err: "users_not_found"}}
		users, err := newEmailTestProvider(mock).searchUsersByEmail(ctx, "nobody@example.com", scopes, true)
		require.NoError(t, err)
		assert.Empty(t, users)
	})

	t.Run("missing_scope from Slack names the scope", func(t *testing.T) {
		mock := &emailLookupMock{err: slack.SlackErrorResponse{Err: "missing_scope"}}
		_, err := newEmailTestProvider(mock).searchUsersByEmail(ctx, "nobody@example.com", nil, false)
		require.ErrorIs(t, err, ErrEmailScopeMissing)
	})

	t.Run("other errors are returned", func(t *testing.T) {
		mock := &emailLookupMock{err: errors.New("boom")}
		_, err := newEmailTestProvider(mock).searchUsersByEmail(ctx, "nobody@example.com", scopes, true)
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrEmailScopeMissing)
	})
}
