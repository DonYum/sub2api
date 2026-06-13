package dto

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLogFromService_IncludesOpenAIWSMode(t *testing.T) {
	t.Parallel()

	wsLog := &service.UsageLog{
		RequestID:    "req_1",
		Model:        "gpt-5.3-codex",
		OpenAIWSMode: true,
	}
	httpLog := &service.UsageLog{
		RequestID:    "resp_1",
		Model:        "gpt-5.3-codex",
		OpenAIWSMode: false,
	}

	require.True(t, UsageLogFromService(wsLog).OpenAIWSMode)
	require.False(t, UsageLogFromService(httpLog).OpenAIWSMode)
	require.True(t, UsageLogFromServiceAdmin(wsLog).OpenAIWSMode)
	require.False(t, UsageLogFromServiceAdmin(httpLog).OpenAIWSMode)
}

func TestUsageLogFromService_PrefersRequestTypeForLegacyFields(t *testing.T) {
	t.Parallel()

	log := &service.UsageLog{
		RequestID:    "req_2",
		Model:        "gpt-5.3-codex",
		RequestType:  service.RequestTypeWSV2,
		Stream:       false,
		OpenAIWSMode: false,
	}

	userDTO := UsageLogFromService(log)
	adminDTO := UsageLogFromServiceAdmin(log)

	require.Equal(t, "ws_v2", userDTO.RequestType)
	require.True(t, userDTO.Stream)
	require.True(t, userDTO.OpenAIWSMode)
	require.Equal(t, "ws_v2", adminDTO.RequestType)
	require.True(t, adminDTO.Stream)
	require.True(t, adminDTO.OpenAIWSMode)
}

func TestUsageCleanupTaskFromService_RequestTypeMapping(t *testing.T) {
	t.Parallel()

	requestType := int16(service.RequestTypeStream)
	task := &service.UsageCleanupTask{
		ID:     1,
		Status: service.UsageCleanupStatusPending,
		Filters: service.UsageCleanupFilters{
			RequestType: &requestType,
		},
	}

	dtoTask := UsageCleanupTaskFromService(task)
	require.NotNil(t, dtoTask)
	require.NotNil(t, dtoTask.Filters.RequestType)
	require.Equal(t, "stream", *dtoTask.Filters.RequestType)
}

func TestRequestTypeStringPtrNil(t *testing.T) {
	t.Parallel()
	require.Nil(t, requestTypeStringPtr(nil))
}

func TestUsageLogFromService_IncludesServiceTierForUserAndAdmin(t *testing.T) {
	t.Parallel()

	serviceTier := "priority"
	inboundEndpoint := "/v1/chat/completions"
	upstreamEndpoint := "/v1/responses"
	log := &service.UsageLog{
		RequestID:             "req_3",
		Model:                 "gpt-5.4",
		ServiceTier:           &serviceTier,
		InboundEndpoint:       &inboundEndpoint,
		UpstreamEndpoint:      &upstreamEndpoint,
		AccountRateMultiplier: f64Ptr(1.5),
	}

	userDTO := UsageLogFromService(log)
	adminDTO := UsageLogFromServiceAdmin(log)

	require.NotNil(t, userDTO.ServiceTier)
	require.Equal(t, serviceTier, *userDTO.ServiceTier)
	require.NotNil(t, userDTO.InboundEndpoint)
	require.Equal(t, inboundEndpoint, *userDTO.InboundEndpoint)
	require.NotNil(t, userDTO.UpstreamEndpoint)
	require.Equal(t, upstreamEndpoint, *userDTO.UpstreamEndpoint)
	require.NotNil(t, adminDTO.ServiceTier)
	require.Equal(t, serviceTier, *adminDTO.ServiceTier)
	require.NotNil(t, adminDTO.InboundEndpoint)
	require.Equal(t, inboundEndpoint, *adminDTO.InboundEndpoint)
	require.NotNil(t, adminDTO.UpstreamEndpoint)
	require.Equal(t, upstreamEndpoint, *adminDTO.UpstreamEndpoint)
	require.NotNil(t, adminDTO.AccountRateMultiplier)
	require.InDelta(t, 1.5, *adminDTO.AccountRateMultiplier, 1e-12)
}

func TestEnrichKiroCostEstimates_UsesBillingPricing(t *testing.T) {
	t.Parallel()

	creditsCost := 3.474350248457712
	log := &service.UsageLog{
		Model:               "claude-opus-4.7",
		InputTokens:         837755,
		OutputTokens:        888,
		UpstreamKiroCredits: &creditsCost,
		TotalCost:           creditsCost,
	}
	dtoLog := UsageLogFromService(log)

	billingService := service.NewBillingService(&config.Config{}, nil)
	EnrichKiroCostEstimates(context.Background(), dtoLog, log, billingService, nil)

	require.NotNil(t, dtoLog.KiroListPriceCostEstimate)
	require.NotNil(t, dtoLog.KiroSavingsCostEstimate)
	require.NotNil(t, dtoLog.KiroDiscountRateEstimate)
	expectedListPrice := float64(log.InputTokens)*5e-6 + float64(log.OutputTokens)*25e-6
	require.InDelta(t, expectedListPrice, *dtoLog.KiroListPriceCostEstimate, 1e-12)
	require.InDelta(t, expectedListPrice-creditsCost, *dtoLog.KiroSavingsCostEstimate, 1e-12)
	require.InDelta(t, (expectedListPrice-creditsCost)/expectedListPrice, *dtoLog.KiroDiscountRateEstimate, 1e-12)
}

func TestUsageLogFromService_UsesRequestedModelAndKeepsUpstreamAdminOnly(t *testing.T) {
	t.Parallel()

	upstreamModel := "claude-sonnet-4-20250514"
	log := &service.UsageLog{
		RequestID:      "req_4",
		Model:          upstreamModel,
		RequestedModel: "claude-sonnet-4",
		UpstreamModel:  &upstreamModel,
	}

	userDTO := UsageLogFromService(log)
	adminDTO := UsageLogFromServiceAdmin(log)

	require.Equal(t, "claude-sonnet-4", userDTO.Model)
	require.Equal(t, "claude-sonnet-4", adminDTO.Model)

	userJSON, err := json.Marshal(userDTO)
	require.NoError(t, err)
	require.NotContains(t, string(userJSON), "upstream_model")

	adminJSON, err := json.Marshal(adminDTO)
	require.NoError(t, err)
	require.Contains(t, string(adminJSON), `"upstream_model":"claude-sonnet-4-20250514"`)
}

func TestUsageLogFromService_FallsBackToLegacyModelWhenRequestedModelMissing(t *testing.T) {
	t.Parallel()

	log := &service.UsageLog{
		RequestID: "req_legacy",
		Model:     "claude-3",
	}

	userDTO := UsageLogFromService(log)
	adminDTO := UsageLogFromServiceAdmin(log)

	require.Equal(t, "claude-3", userDTO.Model)
	require.Equal(t, "claude-3", adminDTO.Model)
}

func TestUsageLogFromService_IncludesImageBillingMetadataForUserAndAdmin(t *testing.T) {
	t.Parallel()

	imageSize := "4K"
	inputSize := "1024x1024"
	outputSize := "3840x2160"
	source := "output"
	log := &service.UsageLog{
		RequestID:          "req_image_metadata",
		Model:              "gpt-image-2",
		ImageCount:         2,
		ImageSize:          &imageSize,
		ImageInputSize:     &inputSize,
		ImageOutputSize:    &outputSize,
		ImageSizeSource:    &source,
		ImageSizeBreakdown: map[string]int{"4K": 2},
	}

	userDTO := UsageLogFromService(log)
	adminDTO := UsageLogFromServiceAdmin(log)

	for _, got := range []*UsageLog{userDTO, &adminDTO.UsageLog} {
		require.Equal(t, 2, got.ImageCount)
		require.NotNil(t, got.ImageSize)
		require.Equal(t, imageSize, *got.ImageSize)
		require.NotNil(t, got.ImageInputSize)
		require.Equal(t, inputSize, *got.ImageInputSize)
		require.NotNil(t, got.ImageOutputSize)
		require.Equal(t, outputSize, *got.ImageOutputSize)
		require.NotNil(t, got.ImageSizeSource)
		require.Equal(t, source, *got.ImageSizeSource)
		require.Equal(t, map[string]int{"4K": 2}, got.ImageSizeBreakdown)
	}
}

func TestUsageLogFromService_PreservesHistoricalMissingImageSize(t *testing.T) {
	t.Parallel()

	log := &service.UsageLog{
		RequestID:  "req_legacy_image_missing_size",
		Model:      "gpt-image-2",
		ImageCount: 1,
		ImageSize:  nil,
	}

	dto := UsageLogFromService(log)
	require.Equal(t, 1, dto.ImageCount)
	require.Nil(t, dto.ImageSize)
	require.Nil(t, dto.ImageInputSize)
	require.Nil(t, dto.ImageOutputSize)
	require.Nil(t, dto.ImageSizeSource)
	require.Nil(t, dto.ImageSizeBreakdown)

	body, err := json.Marshal(dto)
	require.NoError(t, err)
	require.Contains(t, string(body), `"image_size":null`)
	require.NotContains(t, string(body), `"image_size":"2K"`)
}

func f64Ptr(value float64) *float64 {
	return &value
}
