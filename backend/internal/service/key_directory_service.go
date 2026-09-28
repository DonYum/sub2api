package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

var ErrKeyDirectoryCredential = infraerrors.Unauthorized("INVALID_DIRECTORY_CREDENTIAL", "invalid or expired directory credential")

// One credential per user, stored in settings. Rotation replaces the previous
// digest; no credential table, credential listing, or general-purpose scopes.
type KeyDirectoryService struct {
	settings SettingRepository
	users    UserRepository
	keys     APIKeyRepository
}

func NewKeyDirectoryService(settings SettingRepository, users UserRepository, keys APIKeyRepository) *KeyDirectoryService {
	return &KeyDirectoryService{settings: settings, users: users, keys: keys}
}

type keyDirectoryCredential struct {
	Digest    string     `json:"digest"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func directorySettingKey(userID int64) string {
	return "key_directory_credential:" + strconv.FormatInt(userID, 10)
}

func (s *KeyDirectoryService) RotateCredential(ctx context.Context, userID int64, expiresAt *time.Time) (string, error) {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return "", err
	}
	if user.Status != StatusActive {
		return "", infraerrors.BadRequest("USER_DISABLED", "user is disabled")
	}
	if expiresAt != nil && !expiresAt.After(time.Now()) {
		return "", infraerrors.BadRequest("INVALID_EXPIRY", "expires_at must be in the future")
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	token := "kdir_" + strconv.FormatInt(userID, 10) + "_" + hex.EncodeToString(random)
	digest := sha256.Sum256([]byte(token))
	record, err := json.Marshal(keyDirectoryCredential{Digest: hex.EncodeToString(digest[:]), ExpiresAt: expiresAt})
	if err != nil {
		return "", err
	}
	if err := s.settings.Set(ctx, directorySettingKey(userID), string(record)); err != nil {
		return "", err
	}
	return token, nil
}

func (s *KeyDirectoryService) RevokeCredential(ctx context.Context, userID int64) error {
	return s.settings.Delete(ctx, directorySettingKey(userID))
}

func (s *KeyDirectoryService) Authenticate(ctx context.Context, token string) (*User, error) {
	parts := strings.Split(token, "_")
	if len(parts) != 3 || parts[0] != "kdir" || len(parts[2]) != 64 {
		return nil, ErrKeyDirectoryCredential
	}
	userID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || userID <= 0 || strconv.FormatInt(userID, 10) != parts[1] {
		return nil, ErrKeyDirectoryCredential
	}
	if _, err := hex.DecodeString(parts[2]); err != nil {
		return nil, ErrKeyDirectoryCredential
	}
	raw, err := s.settings.GetValue(ctx, directorySettingKey(userID))
	if errors.Is(err, ErrSettingNotFound) {
		return nil, ErrKeyDirectoryCredential
	}
	if err != nil {
		return nil, err
	}
	var record keyDirectoryCredential
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return nil, ErrKeyDirectoryCredential
	}
	expected, err := hex.DecodeString(record.Digest)
	digest := sha256.Sum256([]byte(token))
	if err != nil || subtle.ConstantTimeCompare(expected, digest[:]) != 1 || (record.ExpiresAt != nil && !record.ExpiresAt.After(time.Now())) {
		return nil, ErrKeyDirectoryCredential
	}
	// No authentication cache: revocation, rotation and user disablement take
	// effect on the next request, including across multiple server instances.
	user, err := s.users.GetByID(ctx, userID)
	if errors.Is(err, ErrUserNotFound) {
		return nil, ErrKeyDirectoryCredential
	}
	if err != nil {
		return nil, err
	}
	if user.Status != StatusActive {
		return nil, ErrKeyDirectoryCredential
	}
	return user, nil
}

type KeyDirectoryEntry struct {
	ID     int64  `json:"id"`
	Name   string `json:"api_key_name"`
	Status string `json:"status"`
}

type KeyDirectoryConnection struct {
	KeyDirectoryEntry
	Token     string     `json:"api_key_token"`
	BaseURL   string     `json:"base_url"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func (s *KeyDirectoryService) List(ctx context.Context, userID int64, params pagination.PaginationParams) ([]KeyDirectoryEntry, *pagination.PaginationResult, error) {
	keys, page, err := s.keys.ListByUserID(ctx, userID, params, APIKeyListFilters{})
	if err != nil {
		return nil, nil, err
	}
	out := make([]KeyDirectoryEntry, 0, len(keys))
	for _, key := range keys {
		out = append(out, KeyDirectoryEntry{ID: key.ID, Name: key.Name, Status: key.Status})
	}
	return out, page, nil
}

func (s *KeyDirectoryService) Resolve(ctx context.Context, userID int64, name string, id *int64) (*KeyDirectoryConnection, error) {
	if strings.TrimSpace(name) == "" || len(name) > 100 || (id != nil && *id <= 0) {
		return nil, infraerrors.BadRequest("INVALID_KEY_LOOKUP", "api_key_name and optional id must be valid")
	}
	keys, _, err := s.keys.ListByUserID(ctx, userID, pagination.PaginationParams{Page: 1, PageSize: 2, SortBy: "id", SortOrder: "asc"}, APIKeyListFilters{ExactName: &name, ID: id})
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, ErrAPIKeyNotFound
	}
	if len(keys) > 1 {
		return nil, infraerrors.Conflict("API_KEY_NAME_AMBIGUOUS", "multiple keys have this name; specify id from the directory")
	}
	base, err := s.settings.GetValue(ctx, SettingKeyAPIBaseURL)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return nil, err
	}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	// The existing UI accepts both a gateway root and an OpenAI /v1 base URL.
	base = strings.TrimSuffix(base, "/v1")
	u, parseErr := url.Parse(base)
	if parseErr != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, infraerrors.ServiceUnavailable("DIRECTORY_BASE_URL_UNCONFIGURED", "configure a valid public api_base_url before resolving keys")
	}
	key := keys[0]
	return &KeyDirectoryConnection{KeyDirectoryEntry: KeyDirectoryEntry{ID: key.ID, Name: key.Name, Status: key.Status}, Token: key.Key, BaseURL: base, ExpiresAt: key.ExpiresAt}, nil
}
