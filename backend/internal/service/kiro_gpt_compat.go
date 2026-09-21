package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

func stripKiroGPTUnsupportedAnthropicFields(req *apicompat.AnthropicRequest, mappedModel string) {
	if req == nil || !isKiroGPTLunaModel(mappedModel) {
		return
	}
	req.OutputConfig = nil
	req.Thinking = nil
}

func isKiroGPTLunaModel(model string) bool {
	return strings.EqualFold(strings.TrimSpace(model), "gpt-5.6-luna")
}
