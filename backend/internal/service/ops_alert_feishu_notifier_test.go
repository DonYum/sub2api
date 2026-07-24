package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type opsFeishuRoundTripper func(*http.Request) (*http.Response, error)

func (fn opsFeishuRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type recordingOpsAlertNotifier struct {
	calls int
}

func (n *recordingOpsAlertNotifier) Notify(context.Context, *OpsAlertRule, *OpsAlertEvent) (bool, error) {
	n.calls++
	return true, nil
}

func TestOpsFeishuAlertNotifierNotify(t *testing.T) {
	fixedNow := time.Unix(1700000000, 0).UTC()
	var requestBody []byte
	notifier := &opsFeishuAlertNotifier{
		cfg: config.OpsFeishuAlertConfig{
			Enabled:          true,
			WebhookURL:       "https://open.feishu.cn/open-apis/bot/v2/hook/test-hook",
			Secret:           "secret",
			MinSeverity:      "P1",
			RateLimitPerHour: 20,
			IncludeResolved:  true,
		},
		client: &http.Client{Transport: opsFeishuRoundTripper(func(req *http.Request) (*http.Response, error) {
			var err error
			requestBody, err = io.ReadAll(req.Body)
			require.NoError(t, err)
			require.Equal(t, "https://open.feishu.cn/open-apis/bot/v2/hook/test-hook", req.URL.String())
			require.Equal(t, "application/json", req.Header.Get("Content-Type"))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"code":0,"msg":"success"}`)),
			}, nil
		})},
		limiter: newSlidingWindowLimiter(20, time.Hour),
		now:     func() time.Time { return fixedNow },
	}
	metricValue := 12.5
	thresholdValue := 5.0
	rule := &OpsAlertRule{ID: 7, Name: "OpenAI upstream error rate", Severity: "P1", MetricType: "upstream_error_rate", Operator: ">", Threshold: thresholdValue}
	event := &OpsAlertEvent{ID: 42, RuleID: 7, Status: OpsAlertStatusFiring, MetricValue: &metricValue, ThresholdValue: &thresholdValue, Description: "upstream failures exceeded threshold", FiredAt: fixedNow}

	sent, err := notifier.Notify(context.Background(), rule, event)
	require.NoError(t, err)
	require.True(t, sent)
	require.NotContains(t, string(requestBody), "secret")

	var payload map[string]any
	require.NoError(t, json.Unmarshal(requestBody, &payload))
	require.Equal(t, "1700000000", payload["timestamp"])
	require.Equal(t, "fiWS2+gh28DOydAv7hzONH/mDn9+b1Y4Y5ivXWXy8vA=", payload["sign"])
	require.Equal(t, "interactive", payload["msg_type"])
	card := payload["card"].(map[string]any)
	header := card["header"].(map[string]any)
	require.Equal(t, "orange", header["template"])
	title := header["title"].(map[string]any)
	require.Contains(t, title["content"], "[FIRING][P1]")
}

func TestOpsFeishuAlertNotifierSkipsResolvedAndLowSeverity(t *testing.T) {
	calls := 0
	notifier := &opsFeishuAlertNotifier{
		cfg: config.OpsFeishuAlertConfig{
			WebhookURL:      "https://open.feishu.cn/open-apis/bot/v2/hook/test-hook",
			Secret:          "secret",
			MinSeverity:     "P1",
			IncludeResolved: false,
		},
		client: &http.Client{Transport: opsFeishuRoundTripper(func(req *http.Request) (*http.Response, error) {
			calls++
			return nil, nil
		})},
		limiter: newSlidingWindowLimiter(20, time.Hour),
	}

	sent, err := notifier.Notify(context.Background(), &OpsAlertRule{Severity: "P1"}, &OpsAlertEvent{Status: OpsAlertStatusResolved})
	require.NoError(t, err)
	require.False(t, sent)
	sent, err = notifier.Notify(context.Background(), &OpsAlertRule{Severity: "P2"}, &OpsAlertEvent{Status: OpsAlertStatusFiring})
	require.NoError(t, err)
	require.False(t, sent)
	require.Zero(t, calls)
}

func TestSignOpsFeishuWebhook(t *testing.T) {
	require.Equal(t, "fiWS2+gh28DOydAv7hzONH/mDn9+b1Y4Y5ivXWXy8vA=", signOpsFeishuWebhook(1700000000, "secret"))
}

func TestOpsAlertEvaluatorSendsConfiguredNotification(t *testing.T) {
	notifier := &recordingOpsAlertNotifier{}
	svc := &OpsAlertEvaluatorService{alertNotifier: notifier}

	sent := svc.maybeSendAlertNotification(
		context.Background(),
		nil,
		&OpsAlertRule{ID: 7, Severity: "P1"},
		&OpsAlertEvent{ID: 42, Status: OpsAlertStatusFiring},
	)

	require.True(t, sent)
	require.Equal(t, 1, notifier.calls)
}
