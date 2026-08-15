package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
)

const (
	OpenAICapacityBreakerExtraKey = "openai_capacity_breaker"
	openAICapacityBreakerWindow   = 30 * time.Minute
)

type OpenAICapacityBreakerModelState struct {
	Level         int    `json:"level"`
	LastErrorAt   string `json:"last_error_at"`
	DisabledUntil string `json:"disabled_until,omitempty"`
	Permanent     bool   `json:"permanent,omitempty"`
	Reason        string `json:"reason"`
	Message       string `json:"message,omitempty"`
	StatusCode    int    `json:"status_code,omitempty"`
	UpdatedAt     string `json:"updated_at"`
}

type OpenAICapacityBreakerState struct {
	Models map[string]OpenAICapacityBreakerModelState `json:"models,omitempty"`
}

type OpenAICapacityBreakerApplyInput struct {
	AccountID      int64
	GroupID        int64
	Model          string
	Now            time.Time
	StatusCode     int
	Message        string
	PeerAccountIDs []int64
}

type OpenAICapacityBreakerDecision struct {
	Applied            bool
	Permanent          bool
	Level              int
	Until              *time.Time
	Reason             string
	SkippedReason      string
	RemainingPeerCount int
}

type OpenAICapacityBreakerRepository interface {
	ApplyOpenAICapacityBreaker(ctx context.Context, input OpenAICapacityBreakerApplyInput) (*OpenAICapacityBreakerDecision, error)
}

func IsOpenAICapacityShedFailoverError(err *UpstreamFailoverError) bool {
	return err != nil && err.Reason == GatewayFailureReason("openai_capacity_shed")
}

func NextOpenAICapacityBreakerState(existing OpenAICapacityBreakerModelState, now time.Time, statusCode int, message string) (OpenAICapacityBreakerModelState, *OpenAICapacityBreakerDecision) {
	now = now.UTC()
	if existing.Permanent {
		return existing, &OpenAICapacityBreakerDecision{
			Applied:       false,
			Permanent:     true,
			Level:         existing.Level,
			Reason:        existing.Reason,
			SkippedReason: "already_permanent",
		}
	}
	if disabledUntil, ok := parseOpenAICapacityBreakerTime(existing.DisabledUntil); ok && now.Before(disabledUntil) {
		until := disabledUntil
		return existing, &OpenAICapacityBreakerDecision{
			Applied:       false,
			Level:         existing.Level,
			Until:         &until,
			Reason:        existing.Reason,
			SkippedReason: "already_disabled",
		}
	}

	level := existing.Level
	if disabledUntil, ok := parseOpenAICapacityBreakerTime(existing.DisabledUntil); !ok || now.After(disabledUntil.Add(openAICapacityBreakerWindow)) {
		level = 0
	}
	level++

	updated := OpenAICapacityBreakerModelState{
		Level:       level,
		LastErrorAt: now.Format(time.RFC3339),
		Reason:      "server_is_overloaded",
		Message:     strings.TrimSpace(message),
		StatusCode:  statusCode,
		UpdatedAt:   now.Format(time.RFC3339),
	}
	decision := &OpenAICapacityBreakerDecision{
		Applied: true,
		Level:   level,
		Reason:  updated.Reason,
	}

	switch level {
	case 1:
		until := now.Add(30 * time.Minute)
		updated.DisabledUntil = until.Format(time.RFC3339)
		decision.Until = &until
	case 2:
		until := now.Add(time.Hour)
		updated.DisabledUntil = until.Format(time.RFC3339)
		decision.Until = &until
	case 3:
		until := now.Add(2 * time.Hour)
		updated.DisabledUntil = until.Format(time.RFC3339)
		decision.Until = &until
	default:
		updated.Level = 4
		updated.Permanent = true
		decision.Level = 4
		decision.Permanent = true
	}
	return updated, decision
}

func parseOpenAICapacityBreakerTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

func OpenAICapacityBreakerStateFromExtra(extra map[string]any) OpenAICapacityBreakerState {
	raw := extra[OpenAICapacityBreakerExtraKey]
	if raw == nil {
		return OpenAICapacityBreakerState{Models: map[string]OpenAICapacityBreakerModelState{}}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return OpenAICapacityBreakerState{Models: map[string]OpenAICapacityBreakerModelState{}}
	}
	var state OpenAICapacityBreakerState
	if err := json.Unmarshal(encoded, &state); err != nil {
		return OpenAICapacityBreakerState{Models: map[string]OpenAICapacityBreakerModelState{}}
	}
	if state.Models == nil {
		state.Models = map[string]OpenAICapacityBreakerModelState{}
	}
	return state
}

func OpenAICapacityBreakerReasonJSON(input OpenAICapacityBreakerApplyInput, decision *OpenAICapacityBreakerDecision) string {
	payload := map[string]any{
		"type":                   "openai_capacity_breaker",
		"matched":                "server_is_overloaded",
		"level":                  decision.Level,
		"trigger_window_minutes": int(openAICapacityBreakerWindow / time.Minute),
		"status_code":            input.StatusCode,
		"message":                strings.TrimSpace(input.Message),
	}
	if decision.Until != nil {
		payload["disabled_until"] = decision.Until.UTC().Format(time.RFC3339)
	}
	if decision.Permanent {
		payload["permanent"] = true
	} else if decision.Until != nil {
		payload["disabled_minutes"] = int(decision.Until.Sub(input.Now.UTC()) / time.Minute)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "openai_capacity_breaker"
	}
	return string(raw)
}

func (s *OpenAIGatewayService) RecordOpenAICapacityShed(ctx context.Context, account *Account, groupID *int64, requestedModel string, failoverErr *UpstreamFailoverError) *OpenAICapacityBreakerDecision {
	if s == nil || account == nil || !IsOpenAICapacityShedFailoverError(failoverErr) {
		return nil
	}
	if account.Platform != PlatformOpenAI {
		return &OpenAICapacityBreakerDecision{Applied: false, SkippedReason: "non_openai_account"}
	}
	if groupID == nil || *groupID <= 0 {
		return &OpenAICapacityBreakerDecision{Applied: false, SkippedReason: "missing_group"}
	}
	recorder, ok := s.accountRepo.(OpenAICapacityBreakerRepository)
	if !ok || recorder == nil {
		return &OpenAICapacityBreakerDecision{Applied: false, SkippedReason: "repository_unsupported"}
	}

	modelKey := strings.TrimSpace(account.GetMappedModel(requestedModel))
	if modelKey == "" {
		modelKey = strings.TrimSpace(requestedModel)
	}
	if modelKey == "" {
		return &OpenAICapacityBreakerDecision{Applied: false, SkippedReason: "missing_model"}
	}

	peerIDs, err := s.openAICapacityBreakerPeerIDs(ctx, *groupID, account.ID, requestedModel)
	if err != nil {
		slog.Warn("openai_capacity_breaker_peer_scan_failed", "account_id", account.ID, "group_id", *groupID, "model", modelKey, "error", err)
		return &OpenAICapacityBreakerDecision{Applied: false, SkippedReason: "peer_scan_failed"}
	}
	decision, err := recorder.ApplyOpenAICapacityBreaker(ctx, OpenAICapacityBreakerApplyInput{
		AccountID:      account.ID,
		GroupID:        *groupID,
		Model:          modelKey,
		Now:            time.Now().UTC(),
		StatusCode:     failoverErr.StatusCode,
		Message:        failoverErr.ClientMessage,
		PeerAccountIDs: peerIDs,
	})
	if err != nil {
		slog.Warn("openai_capacity_breaker_apply_failed", "account_id", account.ID, "group_id", *groupID, "model", modelKey, "error", err)
		return &OpenAICapacityBreakerDecision{Applied: false, SkippedReason: "apply_failed"}
	}
	if decision != nil && !decision.Applied {
		slog.Warn("openai_capacity_breaker_skipped", "account_id", account.ID, "group_id", *groupID, "model", modelKey, "reason", decision.SkippedReason, "remaining_peer_count", decision.RemainingPeerCount)
	}
	return decision
}

func (s *OpenAIGatewayService) openAICapacityBreakerPeerIDs(ctx context.Context, groupID int64, currentAccountID int64, requestedModel string) ([]int64, error) {
	accounts, err := s.accountRepo.ListSchedulableByGroupIDAndPlatform(ctx, groupID, PlatformOpenAI)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if account.ID == currentAccountID || account.Platform != PlatformOpenAI {
			continue
		}
		if strings.TrimSpace(requestedModel) != "" && !account.IsModelSupported(requestedModel) {
			continue
		}
		if !account.IsSchedulableForModelWithContext(ctx, requestedModel) {
			continue
		}
		ids = append(ids, account.ID)
	}
	return ids, nil
}
