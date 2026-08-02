package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type rawMessageRepository struct{ db *sql.DB }

func NewRawMessageRepository(db *sql.DB) service.RawMessageRepository {
	return &rawMessageRepository{db: db}
}

func (r *rawMessageRepository) Create(ctx context.Context, v *service.RawMessageRecord) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO raw_message_records (
			request_id, api_key_id, user_id, method, endpoint, status_code, content_type,
			relative_path, request_bytes, response_bytes, request_stored_bytes,
			response_stored_bytes, request_truncated, response_truncated, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		RETURNING id`,
		v.RequestID, v.APIKeyID, v.UserID, v.Method, v.Endpoint, v.StatusCode, nullableString(v.ContentType),
		v.RelativePath, v.RequestBytes, v.ResponseBytes, v.RequestStoredBytes, v.ResponseStoredBytes,
		v.RequestTruncated, v.ResponseTruncated, v.CreatedAt,
	).Scan(&v.ID)
}

func (r *rawMessageRepository) GetByRequestID(ctx context.Context, requestID string, apiKeyID int64) (*service.RawMessageRecord, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, request_id, api_key_id, user_id, method, endpoint, status_code,
		       COALESCE(content_type,''), relative_path, request_bytes, response_bytes,
		       request_stored_bytes, response_stored_bytes, request_truncated,
		       response_truncated, created_at
		FROM raw_message_records WHERE request_id = $1 AND api_key_id = $2 ORDER BY id DESC LIMIT 1`, requestID, apiKeyID)
	v := &service.RawMessageRecord{}
	if err := row.Scan(&v.ID, &v.RequestID, &v.APIKeyID, &v.UserID, &v.Method, &v.Endpoint,
		&v.StatusCode, &v.ContentType, &v.RelativePath, &v.RequestBytes, &v.ResponseBytes,
		&v.RequestStoredBytes, &v.ResponseStoredBytes, &v.RequestTruncated,
		&v.ResponseTruncated, &v.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, service.ErrRawMessageNotFound
		}
		return nil, err
	}
	return v, nil
}

func (r *rawMessageRepository) ExistingRequestIDs(ctx context.Context, requestIDs []string) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT request_id, api_key_id FROM raw_message_records WHERE request_id = ANY($1)`, pq.Array(requestIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]bool, len(requestIDs))
	for rows.Next() {
		var id string
		var apiKeyID int64
		if err := rows.Scan(&id, &apiKeyID); err != nil {
			return nil, err
		}
		out[service.RawMessageUsageKey(id, apiKeyID)] = true
	}
	return out, rows.Err()
}

func rawMessageCleanupWhere(filter service.RawMessageCleanupFilter) (string, []any) {
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 3)
	if filter.APIKeyID > 0 {
		conditions = append(conditions, fmt.Sprintf("api_key_id = $%d", len(args)+1))
		args = append(args, filter.APIKeyID)
	}
	if filter.StartTime != nil {
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", len(args)+1))
		args = append(args, *filter.StartTime)
	}
	if filter.EndTime != nil {
		conditions = append(conditions, fmt.Sprintf("created_at < $%d", len(args)+1))
		args = append(args, *filter.EndTime)
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func (r *rawMessageRepository) PreviewCleanup(ctx context.Context, filter service.RawMessageCleanupFilter) (*service.RawMessageCleanupPreview, error) {
	where, args := rawMessageCleanupWhere(filter)
	out := &service.RawMessageCleanupPreview{}
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(request_stored_bytes + response_stored_bytes),0) FROM raw_message_records`+where, args...).Scan(&out.Records, &out.StoredBytes)
	return out, err
}

func (r *rawMessageRepository) ListCleanup(ctx context.Context, filter service.RawMessageCleanupFilter) ([]service.RawMessageRecord, error) {
	where, args := rawMessageCleanupWhere(filter)
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, relative_path, request_stored_bytes, response_stored_bytes
		FROM raw_message_records`+where+fmt.Sprintf(" ORDER BY id LIMIT $%d", len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]service.RawMessageRecord, 0)
	for rows.Next() {
		var v service.RawMessageRecord
		if err := rows.Scan(&v.ID, &v.RelativePath, &v.RequestStoredBytes, &v.ResponseStoredBytes); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *rawMessageRepository) DeleteByIDs(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM raw_message_records WHERE id = ANY($1)`, pq.Array(ids))
	return err
}

func nullableString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
