package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMetricsConfigRequiresLoopbackListener(t *testing.T) {
	t.Run("accept IPv4 loopback", func(t *testing.T) {
		require.NoError(t, validateMetricsConfig(MetricsConfig{Enabled: true, ListenAddr: "127.0.0.1:19090"}))
	})

	t.Run("accept IPv6 loopback", func(t *testing.T) {
		require.NoError(t, validateMetricsConfig(MetricsConfig{Enabled: true, ListenAddr: "[::1]:19090"}))
	})

	for _, addr := range []string{"0.0.0.0:19090", "10.12.0.64:19090", "localhost:19090", "bad-address"} {
		t.Run(addr, func(t *testing.T) {
			require.Error(t, validateMetricsConfig(MetricsConfig{Enabled: true, ListenAddr: addr}))
		})
	}

	require.NoError(t, validateMetricsConfig(MetricsConfig{}))
}
