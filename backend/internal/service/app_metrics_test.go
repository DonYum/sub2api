package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/appmetrics"
)

func scrapeAppMetrics(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	appmetrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

func TestRecordErrorAppMetricsFirstOutputTimeout(t *testing.T) {
	appmetrics.Enable()
	recordErrorAppMetrics(&OpsInsertErrorLogInput{
		Platform:  "anthropic",
		Model:     "claude-opus-4-8",
		ErrorType: "gateway_error",
		UpstreamErrors: []*OpsUpstreamErrorEvent{
			{Kind: "first_output_timeout", Message: "no first byte in 300s"},
		},
		ErrorOwner: "provider",
		StatusCode: 504,
		Stream:     true,
	})
	body := scrapeAppMetrics(t)
	if !strings.Contains(body, `sub2api_stream_terminations_total{platform="anthropic",reason="first_output_timeout"`) {
		t.Fatalf("stream_terminations_total missing first_output_timeout series:\n%s", body)
	}
	if !strings.Contains(body, `category="first_output_timeout"`) {
		t.Fatalf("upstream_errors_total missing first_output_timeout category:\n%s", body)
	}
}

func TestRecordErrorAppMetricsCapacityFromMessageOnly(t *testing.T) {
	appmetrics.Enable()
	msg := "selected model is at capacity"
	recordErrorAppMetrics(&OpsInsertErrorLogInput{
		Platform:             "anthropic",
		Model:                "claude-opus-4-8",
		UpstreamErrorMessage: &msg,
		ErrorOwner:           "provider",
		StatusCode:           529,
	})
	body := scrapeAppMetrics(t)
	if !strings.Contains(body, `category="model_capacity"`) {
		t.Fatalf("model_capacity not classified from UpstreamMessages alone:\n%s", body)
	}
}

func TestRecordErrorAppMetrics429(t *testing.T) {
	appmetrics.Enable()
	code := 429
	recordErrorAppMetrics(&OpsInsertErrorLogInput{
		Platform:           "anthropic",
		Model:              "claude-opus-4-8",
		UpstreamStatusCode: &code,
		StatusCode:         429,
	})
	body := scrapeAppMetrics(t)
	if !strings.Contains(body, `category="upstream_429"`) {
		t.Fatalf("upstream_429 not classified:\n%s", body)
	}
}
