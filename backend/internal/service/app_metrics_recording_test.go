//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/appmetrics"
	"github.com/Wei-Shaw/sub2api/internal/config"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/require"
)

func appMetricForLabels(t *testing.T, name string, labels map[string]string) *dto.Metric {
	t.Helper()
	var parser expfmt.TextParser
	families, err := parser.TextToMetricFamilies(strings.NewReader(scrapeAppMetrics(t)))
	require.NoError(t, err)
	for _, metric := range families[name].GetMetric() {
		matched := 0
		for _, label := range metric.GetLabel() {
			if value, ok := labels[label.GetName()]; ok && value == label.GetValue() {
				matched++
			}
		}
		if matched == len(labels) {
			return metric
		}
	}
	return nil
}

func TestGatewayRecordUsageEmitsAppMetrics(t *testing.T) {
	appmetrics.Enable()
	for _, platform := range []string{PlatformOpenAI, PlatformAnthropic} {
		for _, mode := range []string{config.RunModeStandard, config.RunModeSimple} {
			t.Run(platform+"/"+mode, func(t *testing.T) {
				endpoint, metricEndpoint := "/v1/responses", "responses"
				if platform == PlatformAnthropic {
					endpoint, metricEndpoint = "/v1/messages", "messages"
				}
				requestLabels := map[string]string{
					"platform": platform, "endpoint": metricEndpoint,
					"request_type": "stream", "outcome": "success", "status_code": "200",
				}
				durationLabels := map[string]string{
					"platform": platform, "endpoint": metricEndpoint, "request_type": "stream",
				}
				firstOutputLabels := map[string]string{"platform": platform, "endpoint": metricEndpoint}
				beforeRequests := appMetricForLabels(t, "sub2api_requests_total", requestLabels).GetCounter().GetValue()
				beforeDuration := appMetricForLabels(t, "sub2api_request_duration_seconds", durationLabels).GetHistogram()
				beforeFirstOutput := appMetricForLabels(t, "sub2api_first_output_seconds", firstOutputLabels).GetHistogram()

				usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
				userRepo := &openAIRecordUsageUserRepoStub{}
				subRepo := &openAIRecordUsageSubRepoStub{}
				firstOutputMs := 250
				if platform == PlatformOpenAI {
					svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
					svc.cfg.RunMode = mode
					require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
						Result: &OpenAIForwardResult{
							RequestID: t.Name(), Model: "gpt-5.1", Stream: true,
							Usage:    OpenAIUsage{InputTokens: 10, OutputTokens: 6},
							Duration: 1500 * time.Millisecond, FirstTokenMs: &firstOutputMs,
						},
						APIKey: &APIKey{ID: 501}, User: &User{ID: 601},
						Account: &Account{ID: 701, Platform: platform}, InboundEndpoint: endpoint,
					}))
				} else {
					svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)
					svc.cfg.RunMode = mode
					require.NoError(t, svc.RecordUsage(context.Background(), &RecordUsageInput{
						Result: &ForwardResult{
							RequestID: t.Name(), Model: "claude-sonnet-4", Stream: true,
							Usage:    ClaudeUsage{InputTokens: 10, OutputTokens: 6},
							Duration: 1500 * time.Millisecond, FirstTokenMs: &firstOutputMs,
						},
						APIKey: &APIKey{ID: 501}, User: &User{ID: 601},
						Account: &Account{ID: 701, Platform: platform}, InboundEndpoint: endpoint,
					}))
				}

				require.Equal(t, 1, usageRepo.calls)
				require.Equal(t, beforeRequests+1, appMetricForLabels(t, "sub2api_requests_total", requestLabels).GetCounter().GetValue())
				afterDuration := appMetricForLabels(t, "sub2api_request_duration_seconds", durationLabels).GetHistogram()
				require.Equal(t, beforeDuration.GetSampleCount()+1, afterDuration.GetSampleCount())
				require.InDelta(t, beforeDuration.GetSampleSum()+1.5, afterDuration.GetSampleSum(), 1e-9)
				afterFirstOutput := appMetricForLabels(t, "sub2api_first_output_seconds", firstOutputLabels).GetHistogram()
				require.Equal(t, beforeFirstOutput.GetSampleCount()+1, afterFirstOutput.GetSampleCount())
				require.InDelta(t, beforeFirstOutput.GetSampleSum()+0.25, afterFirstOutput.GetSampleSum(), 1e-9)
			})
		}
	}
}

func TestOpsRecordErrorEmitsAppMetrics(t *testing.T) {
	appmetrics.Enable()
	for _, tc := range []struct {
		name     string
		batch    bool
		count    int
		fail     bool
		disabled bool
	}{
		{name: "single", count: 1},
		{name: "batch-single", batch: true, count: 1},
		{name: "batch", batch: true, count: 2},
		{name: "single-failed", count: 1, fail: true},
		{name: "batch-single-failed", batch: true, count: 1, fail: true},
		{name: "batch-failed", batch: true, count: 2, fail: true},
		{name: "disabled", count: 1, disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestLabels := map[string]string{
				"platform": "openai", "endpoint": "responses", "request_type": "stream",
				"outcome": "error", "status_code": "504",
			}
			upstreamLabels := map[string]string{
				"platform": "openai", "model": "gpt-5.5", "category": "first_output_timeout", "upstream_status": "none",
			}
			terminationLabels := map[string]string{"platform": "openai", "reason": "first_output_timeout"}
			beforeRequests := appMetricForLabels(t, "sub2api_requests_total", requestLabels).GetCounter().GetValue()
			beforeUpstream := appMetricForLabels(t, "sub2api_upstream_errors_total", upstreamLabels).GetCounter().GetValue()
			beforeTerminations := appMetricForLabels(t, "sub2api_stream_terminations_total", terminationLabels).GetCounter().GetValue()

			var insertErr error
			if tc.fail {
				insertErr = errors.New("insert failed")
			}
			repo := &opsRepoMock{
				InsertErrorLogFn: func(context.Context, *OpsInsertErrorLogInput) (int64, error) {
					return 1, insertErr
				},
				BatchInsertErrorLogsFn: func(context.Context, []*OpsInsertErrorLogInput) (int64, error) {
					return int64(tc.count), insertErr
				},
			}
			svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			if tc.disabled {
				svc.cfg = &config.Config{}
			}
			entries := make([]*OpsInsertErrorLogInput, 0, tc.count)
			for i := 0; i < tc.count; i++ {
				entry := &OpsInsertErrorLogInput{
					Platform: PlatformOpenAI, Model: "gpt-5.5", InboundEndpoint: "/v1/responses",
					ErrorType: "gateway_error", ErrorOwner: "provider", StatusCode: 504, Stream: true,
					UpstreamErrors: []*OpsUpstreamErrorEvent{{Kind: "first_output_timeout", Message: "no first byte in 300s"}},
				}
				// The asynchronous queue stores sanitized JSON instead of the original event slice.
				require.NoError(t, SanitizeOpsUpstreamErrorsForQueue(entry))
				entries = append(entries, entry)
			}
			var err error
			if tc.batch {
				err = svc.RecordErrorBatch(context.Background(), entries)
			} else {
				err = svc.RecordError(context.Background(), entries[0])
			}
			if tc.fail {
				require.ErrorIs(t, err, insertErr)
			} else {
				require.NoError(t, err)
			}
			increment := float64(tc.count)
			if tc.fail || tc.disabled {
				increment = 0
			}
			require.Equal(t, beforeRequests+increment, appMetricForLabels(t, "sub2api_requests_total", requestLabels).GetCounter().GetValue())
			require.Equal(t, beforeUpstream+increment, appMetricForLabels(t, "sub2api_upstream_errors_total", upstreamLabels).GetCounter().GetValue())
			require.Equal(t, beforeTerminations+increment, appMetricForLabels(t, "sub2api_stream_terminations_total", terminationLabels).GetCounter().GetValue())
		})
	}
}
