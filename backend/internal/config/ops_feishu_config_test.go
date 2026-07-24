package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateOpsFeishuAlertConfig(t *testing.T) {
	valid := OpsFeishuAlertConfig{
		Enabled:          true,
		WebhookURL:       "https://open.feishu.cn/open-apis/bot/v2/hook/test-hook",
		Secret:           "secret",
		MinSeverity:      "P1",
		RateLimitPerHour: 20,
		IncludeResolved:  true,
		TimeoutSeconds:   5,
	}
	require.NoError(t, validateOpsFeishuAlertConfig(valid))

	tests := []struct {
		name   string
		mutate func(*OpsFeishuAlertConfig)
		want   string
	}{
		{name: "missing webhook", mutate: func(cfg *OpsFeishuAlertConfig) { cfg.WebhookURL = "" }, want: "webhook_url is required"},
		{name: "insecure webhook", mutate: func(cfg *OpsFeishuAlertConfig) { cfg.WebhookURL = "http://open.feishu.cn/open-apis/bot/v2/hook/test" }, want: "must be an HTTPS Feishu webhook"},
		{name: "foreign host", mutate: func(cfg *OpsFeishuAlertConfig) { cfg.WebhookURL = "https://example.com/open-apis/bot/v2/hook/test" }, want: "host must be"},
		{name: "missing secret", mutate: func(cfg *OpsFeishuAlertConfig) { cfg.Secret = "" }, want: "secret is required"},
		{name: "bad severity", mutate: func(cfg *OpsFeishuAlertConfig) { cfg.MinSeverity = "critical" }, want: "min_severity"},
		{name: "bad rate limit", mutate: func(cfg *OpsFeishuAlertConfig) { cfg.RateLimitPerHour = 0 }, want: "rate_limit_per_hour"},
		{name: "bad timeout", mutate: func(cfg *OpsFeishuAlertConfig) { cfg.TimeoutSeconds = 31 }, want: "timeout_seconds"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)
			require.ErrorContains(t, validateOpsFeishuAlertConfig(cfg), tt.want)
		})
	}
}
