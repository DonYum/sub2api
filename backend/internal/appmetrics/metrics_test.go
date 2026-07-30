package appmetrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestMetricsUseBoundedLabelsAndSeparateTTFT(t *testing.T) {
	m := New()
	ttft := 1500 * time.Millisecond
	duration := 3 * time.Second
	m.RecordUsage(UsageObservation{
		Platform:    "openai",
		Endpoint:    "/v1/responses?request_id=secret",
		RequestType: "stream",
		Duration:    &duration,
		FirstOutput: &ttft,
	})

	require.Equal(t, float64(1), testutil.ToFloat64(m.requests.WithLabelValues("openai", "responses", "stream", "success", "200")))
	metrics, err := m.registry.Gather()
	require.NoError(t, err)
	for _, family := range metrics {
		text := family.String()
		require.NotContains(t, text, "request_id")
		require.NotContains(t, text, "secret")
	}
}

func TestMetricsClassifyStreamErrorsWithoutRawErrorLabels(t *testing.T) {
	m := New()
	m.RecordError(ErrorObservation{
		Platform:       "openai",
		Endpoint:       "/responses",
		RequestType:    "stream",
		ErrorType:      "api_error with unique payload",
		ErrorOwner:     "provider",
		StatusCode:     504,
		UpstreamStatus: 504,
		UpstreamKinds:  []string{"first_output_timeout"},
		Stream:         true,
	})

	require.Equal(t, float64(1), testutil.ToFloat64(m.upstreamErrors.WithLabelValues("openai", "other", "first_output_timeout", "504")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.streamTerminations.WithLabelValues("openai", "first_output_timeout")))

	metrics, err := m.registry.Gather()
	require.NoError(t, err)
	for _, family := range metrics {
		require.False(t, strings.Contains(family.String(), "unique payload"))
	}
}

func TestMetricsClassifyCapacityErrorsWithBoundedModelLabels(t *testing.T) {
	m := New()
	m.RecordError(ErrorObservation{
		Platform:         "openai",
		Model:            "gpt-5.6-sol-2026-07-unique-customer-suffix",
		Endpoint:         "/responses",
		RequestType:      "stream",
		ErrorOwner:       "provider",
		StatusCode:       200,
		UpstreamStatus:   502,
		UpstreamMessages: []string{"Selected model is at capacity. Please try a different model."},
		ProviderTypes:    []string{"invalid_request_error"},
		Stream:           true,
	})

	require.Equal(t, float64(1), testutil.ToFloat64(m.upstreamErrors.WithLabelValues("openai", "gpt-5.6-sol", "model_capacity", "502")))
	metrics, err := m.registry.Gather()
	require.NoError(t, err)
	for _, family := range metrics {
		text := family.String()
		require.NotContains(t, text, "unique-customer-suffix")
		require.NotContains(t, text, "Selected model is at capacity")
	}
}

func TestMetricsClassifyServerOverloadedCodeAndCollapseUnknownModel(t *testing.T) {
	m := New()
	m.RecordError(ErrorObservation{
		Platform:       "openai",
		Model:          "customer-private-model-name",
		ErrorOwner:     "provider",
		StatusCode:     200,
		UpstreamStatus: 502,
		ProviderCodes:  []string{"server_is_overloaded"},
	})

	require.Equal(t, float64(1), testutil.ToFloat64(m.upstreamErrors.WithLabelValues("openai", "other", "server_overloaded", "502")))
	metrics, err := m.registry.Gather()
	require.NoError(t, err)
	for _, family := range metrics {
		require.NotContains(t, family.String(), "customer-private-model-name")
		require.NotContains(t, family.String(), "server_is_overloaded")
	}
}

func TestUnknownLabelsCollapseToBoundedValues(t *testing.T) {
	m := New()
	m.RecordUsage(UsageObservation{
		Platform:    "customer-specific-platform",
		Endpoint:    "/users/123/custom",
		RequestType: "future-mode",
	})
	require.Equal(t, float64(1), testutil.ToFloat64(m.requests.WithLabelValues("unknown", "other", "unknown", "success", "200")))
}

func TestMetricsHandlerExposesPrometheusText(t *testing.T) {
	m := New()
	m.RecordUsage(UsageObservation{Platform: "anthropic", Endpoint: "/v1/messages", RequestType: "sync"})
	recorder := httptest.NewRecorder()
	m.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/plain")
	require.Contains(t, recorder.Body.String(), `sub2api_requests_total{endpoint="messages",outcome="success",platform="anthropic",request_type="sync",status_code="200"} 1`)
}

func TestMetricsTracksInflightRequestsByPlatform(t *testing.T) {
	m := New()
	done := m.BeginInFlight("openai")
	doneAnthropic := m.BeginInFlight("anthropic")

	if got := testutil.ToFloat64(m.inflightRequests.WithLabelValues("openai")); got != 1 {
		t.Fatalf("openai inflight = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.inflightRequests.WithLabelValues("anthropic")); got != 1 {
		t.Fatalf("anthropic inflight = %v, want 1", got)
	}

	done()
	doneAnthropic()
	if got := testutil.ToFloat64(m.inflightRequests.WithLabelValues("openai")); got != 0 {
		t.Fatalf("openai inflight after completion = %v, want 0", got)
	}
	if got := testutil.ToFloat64(m.inflightRequests.WithLabelValues("anthropic")); got != 0 {
		t.Fatalf("anthropic inflight after completion = %v, want 0", got)
	}
}

func TestMetricsTracksBoundedOpenAISelectionLayersAndOutcomes(t *testing.T) {
	m := New()
	m.RecordOpenAISelection(OpenAISelectionObservation{Model: "gpt-5.6-sol-2026-07-private", Layer: "session_hash"})
	m.RecordOpenAISelection(OpenAISelectionObservation{Model: "customer-private-model", Layer: "future-layer"})
	m.RecordOpenAIResponseOutcome(OpenAIResponseOutcomeObservation{Model: "gpt-5.6-sol", Outcome: "precommit_recovered"})
	m.RecordOpenAIResponseOutcome(OpenAIResponseOutcomeObservation{Model: "customer-private-model", Outcome: "future-outcome"})

	require.Equal(t, float64(1), testutil.ToFloat64(m.openAISelections.WithLabelValues("gpt-5.6-sol", "session_hash")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.openAISelections.WithLabelValues("other", "other")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.openAIOutcomes.WithLabelValues("gpt-5.6-sol", "precommit_recovered")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.openAIOutcomes.WithLabelValues("other", "other")))
}
