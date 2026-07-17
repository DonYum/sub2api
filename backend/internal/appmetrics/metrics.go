package appmetrics

import (
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type UsageObservation struct {
	Platform    string
	Endpoint    string
	RequestType string
	Duration    *time.Duration
	FirstOutput *time.Duration
}

type ErrorObservation struct {
	Platform        string
	Endpoint        string
	RequestType     string
	ErrorType       string
	ErrorOwner      string
	StatusCode      int
	UpstreamStatus  int
	UpstreamKinds   []string
	Stream          bool
	BusinessLimited bool
}

type Metrics struct {
	registry           *prometheus.Registry
	requests           *prometheus.CounterVec
	upstreamErrors     *prometheus.CounterVec
	streamTerminations *prometheus.CounterVec
	requestDuration    *prometheus.HistogramVec
	firstOutput        *prometheus.HistogramVec
	inflightRequests   *prometheus.GaugeVec
}

func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sub2api_requests_total",
			Help: "Completed gateway requests by bounded outcome labels.",
		}, []string{"platform", "endpoint", "request_type", "outcome", "status_code"}),
		upstreamErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sub2api_upstream_errors_total",
			Help: "Provider-owned and upstream HTTP errors by bounded category.",
		}, []string{"platform", "category", "upstream_status"}),
		streamTerminations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sub2api_stream_terminations_total",
			Help: "Observed abnormal stream terminations by bounded reason.",
		}, []string{"platform", "reason"}),
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "sub2api_request_duration_seconds",
			Help:    "End-to-end duration of successfully accounted gateway requests.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60, 120, 180, 300, 600, 1200},
		}, []string{"platform", "endpoint", "request_type"}),
		firstOutput: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "sub2api_first_output_seconds",
			Help:    "Time to first model output for successfully accounted gateway requests.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60, 120, 180, 300},
		}, []string{"platform", "endpoint"}),
		inflightRequests: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "sub2api_inflight_requests",
			Help: "Current gateway requests still in progress by platform.",
		}, []string{"platform"}),
	}
	m.registry.MustRegister(
		prometheus.NewGoCollector(),
		prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
		m.requests,
		m.upstreamErrors,
		m.streamTerminations,
		m.requestDuration,
		m.firstOutput,
		m.inflightRequests,
	)
	return m
}

var defaultMetrics = New()
var defaultEnabled atomic.Bool

func Enable() {
	defaultEnabled.Store(true)
}

func Handler() http.Handler {
	return defaultMetrics.Handler()
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func RecordUsage(obs UsageObservation) {
	if !defaultEnabled.Load() {
		return
	}
	defaultMetrics.RecordUsage(obs)
}

func RecordError(obs ErrorObservation) {
	if !defaultEnabled.Load() {
		return
	}
	defaultMetrics.RecordError(obs)
}

// BeginInFlight records a request that remains active until the returned function is called.
func BeginInFlight(platform string) func() {
	if !defaultEnabled.Load() {
		return func() {}
	}
	return defaultMetrics.BeginInFlight(platform)
}

func (m *Metrics) BeginInFlight(platform string) func() {
	if m == nil {
		return func() {}
	}
	metric := m.inflightRequests.WithLabelValues(normalizePlatform(platform))
	metric.Inc()
	return metric.Dec
}

func (m *Metrics) RecordUsage(obs UsageObservation) {
	if m == nil {
		return
	}
	platform := normalizePlatform(obs.Platform)
	endpoint := normalizeEndpoint(obs.Endpoint)
	requestType := normalizeRequestType(obs.RequestType)
	m.requests.WithLabelValues(platform, endpoint, requestType, "success", "200").Inc()
	if obs.Duration != nil && *obs.Duration >= 0 {
		m.requestDuration.WithLabelValues(platform, endpoint, requestType).Observe(obs.Duration.Seconds())
	}
	if obs.FirstOutput != nil && *obs.FirstOutput >= 0 {
		m.firstOutput.WithLabelValues(platform, endpoint).Observe(obs.FirstOutput.Seconds())
	}
}

func (m *Metrics) RecordError(obs ErrorObservation) {
	if m == nil {
		return
	}
	platform := normalizePlatform(obs.Platform)
	category, streamReason := classifyError(obs)
	statusCode := normalizeStatus(obs.StatusCode)
	m.requests.WithLabelValues(
		platform,
		normalizeEndpoint(obs.Endpoint),
		normalizeRequestType(obs.RequestType),
		"error",
		statusCode,
	).Inc()

	if strings.EqualFold(strings.TrimSpace(obs.ErrorOwner), "provider") || obs.UpstreamStatus > 0 {
		m.upstreamErrors.WithLabelValues(platform, category, normalizeStatus(obs.UpstreamStatus)).Inc()
	}
	if obs.Stream && streamReason != "" {
		m.streamTerminations.WithLabelValues(platform, streamReason).Inc()
	}
}

func normalizePlatform(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "anthropic", "openai", "gemini", "antigravity", "grok":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "unknown"
	}
}

func normalizeEndpoint(value string) string {
	path := strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.Contains(path, "count_tokens") || strings.Contains(path, "input_tokens"):
		return "count_tokens"
	case strings.Contains(path, "chat/completions"):
		return "chat_completions"
	case strings.Contains(path, "images"):
		return "images"
	case strings.Contains(path, "responses"):
		return "responses"
	case strings.Contains(path, "messages"):
		return "messages"
	default:
		return "other"
	}
}

func normalizeRequestType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "sync", "stream", "ws_v2", "cyber":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "unknown"
	}
}

func normalizeStatus(value int) string {
	if value < 100 || value > 599 {
		return "none"
	}
	return strconv.Itoa(value)
}

func classifyError(obs ErrorObservation) (category string, streamReason string) {
	for _, raw := range obs.UpstreamKinds {
		kind := strings.ToLower(strings.TrimSpace(raw))
		switch kind {
		case "first_output_timeout":
			return "first_output_timeout", "first_output_timeout"
		case "stream_interval_timeout", "stream_timeout":
			return "stream_timeout", "stream_timeout"
		case "upstream_disconnected", "stream_read_error":
			return "upstream_disconnect", "upstream_disconnect"
		case "client_cancelled", "client_disconnected":
			return "client_cancelled", "client_cancelled"
		}
	}

	errorType := strings.ToLower(strings.TrimSpace(obs.ErrorType))
	switch {
	case strings.Contains(errorType, "first_output_timeout"):
		return "first_output_timeout", "first_output_timeout"
	case strings.Contains(errorType, "stream_timeout") || strings.Contains(errorType, "stream_interval_timeout"):
		return "stream_timeout", "stream_timeout"
	case strings.Contains(errorType, "disconnect") || strings.Contains(errorType, "stream_read"):
		return "upstream_disconnect", "upstream_disconnect"
	case strings.Contains(errorType, "client_cancel"):
		return "client_cancelled", "client_cancelled"
	case obs.BusinessLimited:
		return "business_limited", ""
	case obs.UpstreamStatus == http.StatusTooManyRequests:
		return "upstream_429", ""
	case obs.StatusCode == http.StatusTooManyRequests:
		return "rate_limited", ""
	case obs.UpstreamStatus >= 500:
		return "upstream_5xx", ""
	case obs.StatusCode == http.StatusGatewayTimeout:
		return "gateway_timeout", "stream_timeout"
	case obs.StatusCode >= 500:
		return "gateway_5xx", ""
	default:
		return "other", ""
	}
}
