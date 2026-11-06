package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *usageLogRepository) SaveUsageUserInput(ctx context.Context, record service.UsageUserInputRecord) error {
	if r == nil || r.sql == nil {
		return nil
	}
	requestID := strings.TrimSpace(record.RequestID)
	userInput := strings.TrimSpace(record.UserInput)
	if requestID == "" || record.APIKeyID <= 0 || userInput == "" {
		return nil
	}
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err := r.sql.ExecContext(ctx, `
		INSERT INTO usage_user_inputs (request_id, api_key_id, user_id, model, user_input, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (request_id, api_key_id) DO NOTHING
	`, requestID, record.APIKeyID, record.UserID, strings.TrimSpace(record.Model), userInput, createdAt.UTC())
	return err
}

func (r *usageLogRepository) BatchGetUsageUserInputs(ctx context.Context, keys []service.UsageUserInputKey) (map[service.UsageUserInputKey]string, error) {
	out := make(map[service.UsageUserInputKey]string)
	if r == nil || r.sql == nil || len(keys) == 0 {
		return out, nil
	}

	seen := make(map[service.UsageUserInputKey]struct{}, len(keys))
	unique := make([]service.UsageUserInputKey, 0, len(keys))
	for _, k := range keys {
		k.RequestID = strings.TrimSpace(k.RequestID)
		if k.RequestID == "" || k.APIKeyID <= 0 {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		unique = append(unique, k)
	}
	if len(unique) == 0 {
		return out, nil
	}

	clauses := make([]string, 0, len(unique))
	args := make([]any, 0, len(unique)*2)
	for i, k := range unique {
		pos := i*2 + 1
		clauses = append(clauses, fmt.Sprintf("(request_id = $%d AND api_key_id = $%d)", pos, pos+1))
		args = append(args, k.RequestID, k.APIKeyID)
	}

	query := "SELECT request_id, api_key_id, user_input FROM usage_user_inputs WHERE " + strings.Join(clauses, " OR ")
	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var reqID string
		var apiKeyID int64
		var userInput string
		if err := rows.Scan(&reqID, &apiKeyID, &userInput); err != nil {
			return nil, err
		}
		out[service.UsageUserInputKey{RequestID: reqID, APIKeyID: apiKeyID}] = userInput
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *usageLogRepository) CleanupExpiredUsageUserInputs(ctx context.Context, before time.Time) (int64, error) {
	if r == nil || r.sql == nil {
		return 0, nil
	}
	res, err := r.sql.ExecContext(ctx, `DELETE FROM usage_user_inputs WHERE created_at < $1`, before.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
