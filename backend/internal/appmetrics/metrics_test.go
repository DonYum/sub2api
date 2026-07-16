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

	require.Equal(t, float64(1), testutil.ToFloat64(m.upstreamErrors.WithLabelValues("openai", "first_output_timeout", "504")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.streamTerminations.WithLabelValues("openai", "first_output_timeout")))

	metrics, err := m.registry.Gather()
	require.NoError(t, err)
	for _, family := range metrics {
		require.False(t, strings.Contains(family.String(), "unique payload"))
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
