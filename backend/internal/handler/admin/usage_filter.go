package admin

import (
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func parseReasoningEffortFilter(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	normalized := service.NormalizeMaxReasoningEffort(raw)
	if normalized == "" {
		return "", fmt.Errorf("invalid reasoning_effort")
	}
	return normalized, nil
}
