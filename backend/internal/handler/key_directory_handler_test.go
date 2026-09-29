package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type directoryHandlerSettings struct {
	service.SettingRepository
	values map[string]string
}

func (s *directoryHandlerSettings) GetValue(_ context.Context, k string) (string, error) {
	v, ok := s.values[k]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return v, nil
}
func (s *directoryHandlerSettings) Set(_ context.Context, k, v string) error {
	s.values[k] = v
	return nil
}
func (s *directoryHandlerSettings) Delete(_ context.Context, k string) error {
	delete(s.values, k)
	return nil
}

type directoryHandlerUsers struct{ service.UserRepository }

func (s *directoryHandlerUsers) GetByID(_ context.Context, id int64) (*service.User, error) {
	return &service.User{ID: id, Status: service.StatusActive}, nil
}

type directoryHandlerKeys struct{ service.APIKeyRepository }

func (s *directoryHandlerKeys) ListByUserID(_ context.Context, id int64, p pagination.PaginationParams, f service.APIKeyListFilters) ([]service.APIKey, *pagination.PaginationResult, error) {
	rows := []service.APIKey{}
	if id == 7 && (f.ExactName == nil || *f.ExactName == "my-key") && (f.ID == nil || *f.ID == 70) {
		rows = append(rows, service.APIKey{ID: 70, UserID: 7, Name: "my-key", Key: "returned-secret", Status: "active", Group: &service.Group{Platform: service.PlatformOpenAI, ModelAllowlist: service.GroupModelAllowlist{Enabled: true, Models: []string{"gpt-5.6-sol"}}}})
	}
	return rows, &pagination.PaginationResult{Total: int64(len(rows))}, nil
}

type directoryAuditRepo struct {
	service.AuditLogRepository
	entries []*service.AuditLog
}

func (s *directoryAuditRepo) BatchInsert(_ context.Context, entries []*service.AuditLog) (int64, error) {
	s.entries = append(s.entries, entries...)
	return int64(len(entries)), nil
}

func TestKeyDirectoryHTTPContractAndAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := &directoryHandlerSettings{values: map[string]string{service.SettingKeyAPIBaseURL: "https://gateway.example/v1"}}
	svc := service.NewKeyDirectoryService(settings, &directoryHandlerUsers{}, &directoryHandlerKeys{})
	credential, err := svc.RotateCredential(context.Background(), 7, nil)
	require.NoError(t, err)
	auditRepo := &directoryAuditRepo{}
	audit := service.NewAuditLogService(auditRepo, nil)
	audit.Start()
	h := NewKeyDirectoryHandler(svc, audit)
	r := gin.New()
	group := r.Group("/api/v1/key-directory", h.Audit, h.Authenticate)
	group.GET("", h.List)
	group.POST("/resolve", h.Resolve)
	cases := []struct {
		name, method, path, token, body string
		status                          int
		secret                          bool
	}{
		{"missing", "GET", "/api/v1/key-directory", "", "", 401, false},
		{"model key", "GET", "/api/v1/key-directory", "sk-model-key", "", 401, false},
		{"jwt", "GET", "/api/v1/key-directory", "jwt.token.value", "", 401, false},
		{"directory", "GET", "/api/v1/key-directory?user_id=8", credential, "", 200, false},
		{"resolve", "POST", "/api/v1/key-directory/resolve", credential, `{"api_key_name":"my-key"}`, 200, true},
		{"scope injection", "POST", "/api/v1/key-directory/resolve", credential, `{"api_key_name":"my-key","user_id":8}`, 400, false},
		{"foreign id", "POST", "/api/v1/key-directory/resolve", credential, `{"api_key_name":"my-key","id":80}`, 404, false},
		{"extra body", "POST", "/api/v1/key-directory/resolve", credential, `{"api_key_name":"my-key"} {}`, 400, false},
		{"oversized", "POST", "/api/v1/key-directory/resolve", credential, `{"api_key_name":"` + strings.Repeat("a", 5000) + `"}`, 400, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			require.NotContains(t, w.Body.String(), credential)
			if tc.secret {
				require.Contains(t, w.Body.String(), "returned-secret")
				require.Contains(t, w.Body.String(), `"base_url":"https://gateway.example"`)
				require.Contains(t, w.Body.String(), `"platform":"openai"`)
				require.Contains(t, w.Body.String(), `"type":"api_key"`)
				require.Contains(t, w.Body.String(), `"model_names":["gpt-5.6-sol"]`)
				require.Contains(t, w.Body.String(), `"protocols":["openai_responses","openai_chat_completions"]`)
			} else {
				require.NotContains(t, w.Body.String(), "returned-secret")
			}
		})
	}
	audit.Stop()
	require.Len(t, auditRepo.entries, len(cases))
	raw, err := json.Marshal(auditRepo.entries)
	require.NoError(t, err)
	require.NotContains(t, string(raw), credential)
	require.NotContains(t, string(raw), "returned-secret")
	require.NotContains(t, string(raw), "sk-model-key")
	for _, e := range auditRepo.entries {
		require.Empty(t, e.RequestBody)
		require.Empty(t, e.CredentialMasked)
	}
	require.NoError(t, svc.RevokeCredential(context.Background(), 7))
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/key-directory", nil)
	req.Header.Set("Authorization", "Bearer "+credential)
	r.ServeHTTP(w, req)
	require.Equal(t, 401, w.Code)
}

func TestKeyDirectoryCredentialManagement(t *testing.T) {
	settings := &directoryHandlerSettings{values: map[string]string{}}
	svc := service.NewKeyDirectoryService(settings, &directoryHandlerUsers{}, &directoryHandlerKeys{})
	h := NewKeyDirectoryHandler(svc, nil)
	r := gin.New()
	// Admin authentication is checked separately at production route registration.
	r.POST("/users/:id/credential", h.RotateCredential)
	r.DELETE("/users/:id/credential", h.RevokeCredential)
	rotate := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/users/7/credential", strings.NewReader(body)))
		return w
	}
	w := rotate(`{}`)
	require.Equal(t, 200, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	var result struct {
		Data struct {
			Credential string `json:"credential"`
		}
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	_, err := svc.Authenticate(context.Background(), result.Data.Credential)
	require.NoError(t, err)
	require.Equal(t, 400, rotate(`{"user_id":8}`).Code)
	require.Equal(t, 400, rotate(`{"expires_at":"2000-01-01T00:00:00Z"}`).Code)
	require.Equal(t, 200, rotate(`{}`).Code)
	_, err = svc.Authenticate(context.Background(), result.Data.Credential)
	require.Error(t, err)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/users/7/credential", nil))
	require.Equal(t, 200, w.Code)
	require.Empty(t, settings.values)
}
