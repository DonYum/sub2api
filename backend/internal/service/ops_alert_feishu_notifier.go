package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const opsFeishuResponseMaxBytes = 64 * 1024

type opsAlertNotifier interface {
	Notify(ctx context.Context, rule *OpsAlertRule, event *OpsAlertEvent) (bool, error)
}

type opsFeishuAlertNotifier struct {
	cfg     config.OpsFeishuAlertConfig
	client  *http.Client
	limiter *slidingWindowLimiter
	now     func() time.Time
}

type opsFeishuWebhookResponse struct {
	Code int `json:"code"`
}

func newOpsFeishuAlertNotifier(cfg config.OpsFeishuAlertConfig) opsAlertNotifier {
	if !cfg.Enabled {
		return nil
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &opsFeishuAlertNotifier{
		cfg:     cfg,
		client:  &http.Client{Timeout: timeout},
		limiter: newSlidingWindowLimiter(cfg.RateLimitPerHour, time.Hour),
		now:     time.Now,
	}
}

func (n *opsFeishuAlertNotifier) Notify(ctx context.Context, rule *OpsAlertRule, event *OpsAlertEvent) (bool, error) {
	if n == nil || rule == nil || event == nil || n.client == nil {
		return false, nil
	}
	if event.Status == OpsAlertStatusResolved && !n.cfg.IncludeResolved {
		return false, nil
	}
	if !shouldSendOpsAlertByMinSeverity(n.cfg.MinSeverity, rule.Severity) {
		return false, nil
	}
	now := time.Now().UTC()
	if n.now != nil {
		now = n.now().UTC()
	}
	if n.limiter != nil && !n.limiter.Allow(now) {
		return false, nil
	}

	payload, err := buildOpsFeishuAlertPayload(n.cfg.Secret, now, rule, event)
	if err != nil {
		return false, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("marshal feishu alert: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(n.cfg.WebhookURL), bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("build feishu alert request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("send feishu alert: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, opsFeishuResponseMaxBytes))
		return false, fmt.Errorf("feishu alert returned HTTP %d", resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, opsFeishuResponseMaxBytes)
	var result opsFeishuWebhookResponse
	if err := json.NewDecoder(limited).Decode(&result); err != nil {
		return false, fmt.Errorf("decode feishu alert response: %w", err)
	}
	if result.Code != 0 {
		return false, fmt.Errorf("feishu alert returned code %d", result.Code)
	}
	return true, nil
}

func shouldSendOpsAlertByMinSeverity(minSeverity string, eventSeverity string) bool {
	rank := func(value string) int {
		switch strings.ToUpper(strings.TrimSpace(value)) {
		case "P0":
			return 4
		case "P1":
			return 3
		case "P2":
			return 2
		case "P3":
			return 1
		default:
			return 0
		}
	}
	minimum := rank(minSeverity)
	if minimum == 0 {
		return true
	}
	return rank(eventSeverity) >= minimum
}

func buildOpsFeishuAlertPayload(secret string, now time.Time, rule *OpsAlertRule, event *OpsAlertEvent) (map[string]any, error) {
	if rule == nil || event == nil {
		return nil, fmt.Errorf("rule and event are required")
	}
	timestamp := now.Unix()
	statusLabel := "FIRING"
	headerTemplate := opsFeishuSeverityTemplate(rule.Severity)
	if event.Status == OpsAlertStatusResolved {
		statusLabel = "RESOLVED"
		headerTemplate = "green"
	}
	metricValue := "-"
	if event.MetricValue != nil {
		metricValue = fmt.Sprintf("%.2f", *event.MetricValue)
	}
	thresholdValue := fmt.Sprintf("%.2f", rule.Threshold)
	if event.ThresholdValue != nil {
		thresholdValue = fmt.Sprintf("%.2f", *event.ThresholdValue)
	}
	title := truncateString(fmt.Sprintf("[%s][%s] %s", statusLabel, strings.TrimSpace(rule.Severity), strings.TrimSpace(rule.Name)), 120)
	description := truncateString(strings.TrimSpace(event.Description), 2000)
	if description == "" {
		description = "No description"
	}

	return map[string]any{
		"timestamp": strconv.FormatInt(timestamp, 10),
		"sign":      signOpsFeishuWebhook(timestamp, secret),
		"msg_type":  "interactive",
		"card": map[string]any{
			"config": map[string]any{"wide_screen_mode": true},
			"header": map[string]any{
				"template": headerTemplate,
				"title":    map[string]any{"tag": "plain_text", "content": title},
			},
			"elements": []any{
				map[string]any{
					"tag": "div",
					"fields": []any{
						opsFeishuField("Status", statusLabel),
						opsFeishuField("Metric", strings.TrimSpace(rule.MetricType)),
						opsFeishuField("Current", metricValue),
						opsFeishuField("Threshold", strings.TrimSpace(rule.Operator)+" "+thresholdValue),
					},
				},
				map[string]any{
					"tag":  "div",
					"text": map[string]any{"tag": "plain_text", "content": description},
				},
				map[string]any{
					"tag": "note",
					"elements": []any{
						map[string]any{"tag": "plain_text", "content": fmt.Sprintf("Event ID: %d | Rule ID: %d | Fired: %s", event.ID, rule.ID, event.FiredAt.UTC().Format(time.RFC3339))},
					},
				},
			},
		},
	}, nil
}

func opsFeishuField(label string, value string) map[string]any {
	return map[string]any{
		"is_short": true,
		"text": map[string]any{
			"tag":     "lark_md",
			"content": fmt.Sprintf("**%s**\n%s", label, truncateString(strings.TrimSpace(value), 200)),
		},
	}
}

func opsFeishuSeverityTemplate(severity string) string {
	switch strings.ToUpper(strings.TrimSpace(severity)) {
	case "P0":
		return "red"
	case "P1":
		return "orange"
	case "P2":
		return "yellow"
	default:
		return "blue"
	}
}

func signOpsFeishuWebhook(timestamp int64, secret string) string {
	stringToSign := fmt.Sprintf("%d\n%s", timestamp, strings.TrimSpace(secret))
	mac := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
