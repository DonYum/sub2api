package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

type directorySettingsStub struct {
	SettingRepository
	values map[string]string
	err    error
}

func (s *directorySettingsStub) GetValue(_ context.Context, k string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	v, ok := s.values[k]
	if !ok {
		return "", ErrSettingNotFound
	}
	return v, nil
}
func (s *directorySettingsStub) Set(_ context.Context, k, v string) error {
	s.values[k] = v
	return nil
}
func (s *directorySettingsStub) Delete(_ context.Context, k string) error {
	delete(s.values, k)
	return nil
}

type directoryUsersStub struct {
	UserRepository
	users map[int64]*User
}

func (s *directoryUsersStub) GetByID(_ context.Context, id int64) (*User, error) {
	u, ok := s.users[id]
	if !ok {
		return nil, ErrUserNotFound
	}
	return u, nil
}

type directoryKeysStub struct {
	APIKeyRepository
	rows  []APIKey
	owner int64
}

func (s *directoryKeysStub) ListByUserID(_ context.Context, id int64, p pagination.PaginationParams, f APIKeyListFilters) ([]APIKey, *pagination.PaginationResult, error) {
	s.owner = id
	var rows []APIKey
	for _, k := range s.rows {
		if k.UserID == id && (f.ExactName == nil || k.Name == *f.ExactName) && (f.ID == nil || k.ID == *f.ID) {
			rows = append(rows, k)
		}
	}
	total := len(rows)
	if len(rows) > p.PageSize {
		rows = rows[:p.PageSize]
	}
	return rows, &pagination.PaginationResult{Total: int64(total)}, nil
}
func newDirectoryTestService() (*KeyDirectoryService, *directorySettingsStub, *directoryUsersStub, *directoryKeysStub) {
	settings := &directorySettingsStub{values: map[string]string{SettingKeyAPIBaseURL: "https://gateway.example/v1/"}}
	users := &directoryUsersStub{users: map[int64]*User{1: {ID: 1, Status: StatusActive}, 2: {ID: 2, Status: StatusActive}}}
	keys := &directoryKeysStub{rows: []APIKey{{ID: 11, UserID: 1, Name: "alpha", Key: "secret-a", Status: "active"}, {ID: 21, UserID: 2, Name: "alpha", Key: "secret-b"}}}
	return NewKeyDirectoryService(settings, users, keys), settings, users, keys
}
func TestKeyDirectoryCredentialLifecycle(t *testing.T) {
	ctx := context.Background()
	s, settings, users, _ := newDirectoryTestService()
	token, err := s.RotateCredential(ctx, 1, nil)
	require.NoError(t, err)
	require.NotContains(t, settings.values[directorySettingKey(1)], token)
	u, err := s.Authenticate(ctx, token)
	require.NoError(t, err)
	require.EqualValues(t, 1, u.ID)
	for _, bad := range []string{"", "sk-model-key", "jwt.token.here", strings.Replace(token, "kdir_1_", "kdir_2_", 1), token + "0"} {
		_, err = s.Authenticate(ctx, bad)
		require.ErrorIs(t, err, ErrKeyDirectoryCredential)
	}
	next, err := s.RotateCredential(ctx, 1, nil)
	require.NoError(t, err)
	require.NotEqual(t, token, next)
	_, err = s.Authenticate(ctx, token)
	require.ErrorIs(t, err, ErrKeyDirectoryCredential)
	_, err = s.Authenticate(ctx, next)
	require.NoError(t, err)
	users.users[1].Status = "disabled"
	_, err = s.Authenticate(ctx, next)
	require.ErrorIs(t, err, ErrKeyDirectoryCredential)
	users.users[1].Status = StatusActive
	require.NoError(t, s.RevokeCredential(ctx, 1))
	_, err = s.Authenticate(ctx, next)
	require.ErrorIs(t, err, ErrKeyDirectoryCredential)
}
func TestKeyDirectoryExpiredAndStorageFailure(t *testing.T) {
	ctx := context.Background()
	s, settings, users, _ := newDirectoryTestService()
	past := time.Now().Add(-time.Hour)
	_, err := s.RotateCredential(ctx, 1, &past)
	require.Error(t, err)
	future := time.Now().Add(time.Hour)
	token, err := s.RotateCredential(ctx, 1, &future)
	require.NoError(t, err)
	var r keyDirectoryCredential
	require.NoError(t, json.Unmarshal([]byte(settings.values[directorySettingKey(1)]), &r))
	r.ExpiresAt = &past
	raw, _ := json.Marshal(r)
	settings.values[directorySettingKey(1)] = string(raw)
	_, err = s.Authenticate(ctx, token)
	require.ErrorIs(t, err, ErrKeyDirectoryCredential)
	token, err = s.RotateCredential(ctx, 1, nil)
	require.NoError(t, err)
	delete(users.users, 1)
	_, err = s.Authenticate(ctx, token)
	require.ErrorIs(t, err, ErrKeyDirectoryCredential)
	settings.err = errors.New("database unavailable")
	_, err = s.Authenticate(ctx, token)
	require.ErrorContains(t, err, "database unavailable")
}
func TestKeyDirectoryResolveIsolationAndAmbiguity(t *testing.T) {
	ctx := context.Background()
	s, _, _, keys := newDirectoryTestService()
	list, _, err := s.List(ctx, 1, pagination.PaginationParams{Page: 1, PageSize: 100})
	require.NoError(t, err)
	require.Len(t, list, 1)
	raw, _ := json.Marshal(list)
	require.NotContains(t, string(raw), "secret")
	require.NotContains(t, string(raw), "token")
	conn, err := s.Resolve(ctx, 1, "alpha", nil)
	require.NoError(t, err)
	require.Equal(t, "secret-a", conn.Token)
	require.Equal(t, "https://gateway.example", conn.BaseURL)
	require.EqualValues(t, 1, keys.owner)
	otherID := int64(21)
	_, err = s.Resolve(ctx, 1, "alpha", &otherID)
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
	_, err = s.Resolve(ctx, 1, "alp", nil)
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
	keys.rows = append(keys.rows, APIKey{ID: 12, UserID: 1, Name: "alpha", Key: "second"})
	_, err = s.Resolve(ctx, 1, "alpha", nil)
	require.ErrorContains(t, err, "multiple keys")
	id := int64(11)
	conn, err = s.Resolve(ctx, 1, "alpha", &id)
	require.NoError(t, err)
	require.Equal(t, "secret-a", conn.Token)
	_, err = s.Resolve(ctx, 1, "wrong-name", &id)
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
}
func TestKeyDirectoryBaseURL(t *testing.T) {
	for _, base := range []string{"", "/v1", "https://user:password@gateway.example", "https://gateway.example?token=secret", "https://gateway.example/#fragment", "ftp://gateway.example"} {
		t.Run(base, func(t *testing.T) {
			s, settings, _, _ := newDirectoryTestService()
			settings.values[SettingKeyAPIBaseURL] = base
			conn, err := s.Resolve(context.Background(), 1, "alpha", nil)
			require.Error(t, err)
			require.Nil(t, conn)
		})
	}
}
