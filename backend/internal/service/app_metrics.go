package service

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/appmetrics"
)

func recordUsageAppMetrics(log *UsageLog, platform string) {
	if log == nil {
		return
	}
	var duration *time.Duration
	if log.DurationMs != nil {
		value := time.Duration(*log.DurationMs) * time.Millisecond
		duration = &value
	}
	var firstOutput *time.Duration
	if log.FirstTokenMs != nil {
		value := time.Duration(*log.FirstTokenMs) * time.Millisecond
		firstOutput = &value
	}
	endpoint := ""
	if log.InboundEndpoint != nil {
		endpoint = *log.InboundEndpoint
	}
	appmetrics.RecordUsage(appmetrics.UsageObservation{
		Platform:    platform,
		Endpoint:    endpoint,
		RequestType: log.EffectiveRequestType().String(),
		Duration:    duration,
		FirstOutput: firstOutput,
	})
}

func recordErrorAppMetrics(entry *OpsInsertErrorLogInput) {
	if entry == nil {
		return
	}
	upstreamStatus := 0
	if entry.UpstreamStatusCode != nil {
		upstreamStatus = *entry.UpstreamStatusCode
	}
	requestType := RequestTypeFromLegacy(entry.Stream, false).String()
	if entry.RequestType != nil {
		requestType = RequestTypeFromInt16(*entry.RequestType).String()
	}
	upstreamKinds := make([]string, 0, len(entry.UpstreamErrors))
	upstreamMessages := make([]string, 0, len(entry.UpstreamErrors)+1)
	providerCodes := make([]string, 0, len(entry.UpstreamErrors))
	providerTypes := make([]string, 0, len(entry.UpstreamErrors))
	if entry.UpstreamErrorMessage != nil {
		upstreamMessages = append(upstreamMessages, *entry.UpstreamErrorMessage)
	}
	for _, event := range entry.UpstreamErrors {
		if event != nil {
			upstreamKinds = append(upstreamKinds, event.Kind)
			upstreamMessages = append(upstreamMessages, event.Message)
			providerCodes = append(providerCodes, event.ProviderErrorCode)
			providerTypes = append(providerTypes, event.ProviderErrorType)
		}
	}
	model := entry.RequestedModel
	if model == "" {
		model = entry.Model
	}
	if model == "" {
		model = entry.UpstreamModel
	}
	appmetrics.RecordError(appmetrics.ErrorObservation{
		Platform:         entry.Platform,
		Model:            model,
		Endpoint:         entry.InboundEndpoint,
		RequestType:      requestType,
		ErrorType:        entry.ErrorType,
		ErrorOwner:       entry.ErrorOwner,
		StatusCode:       entry.StatusCode,
		UpstreamStatus:   upstreamStatus,
		UpstreamKinds:    upstreamKinds,
		UpstreamMessages: upstreamMessages,
		ProviderCodes:    providerCodes,
		ProviderTypes:    providerTypes,
		Stream:           entry.Stream,
		BusinessLimited:  entry.IsBusinessLimited,
	})
}
