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
	for _, event := range entry.UpstreamErrors {
		if event != nil {
			upstreamKinds = append(upstreamKinds, event.Kind)
		}
	}
	appmetrics.RecordError(appmetrics.ErrorObservation{
		Platform:        entry.Platform,
		Endpoint:        entry.InboundEndpoint,
		RequestType:     requestType,
		ErrorType:       entry.ErrorType,
		ErrorOwner:      entry.ErrorOwner,
		StatusCode:      entry.StatusCode,
		UpstreamStatus:  upstreamStatus,
		UpstreamKinds:   upstreamKinds,
		Stream:          entry.Stream,
		BusinessLimited: entry.IsBusinessLimited,
	})
}
