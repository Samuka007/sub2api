package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestMetricsConfigDefaultsAndEnvironment(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, MetricsConfig{Host: "127.0.0.1", Port: 9091, Path: "/metrics"}, cfg.Metrics)

	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("JWT_SECRET", "01234567890123456789012345678901")
	t.Setenv("METRICS_ENABLED", "true")
	t.Setenv("METRICS_HOST", "0.0.0.0")
	t.Setenv("METRICS_PORT", "9191")
	t.Setenv("METRICS_PATH", "/internal/metrics")
	cfg, err = Load()
	require.NoError(t, err)
	require.Equal(t, MetricsConfig{Enabled: true, Host: "0.0.0.0", Port: 9191, Path: "/internal/metrics"}, cfg.Metrics)
}

func TestMetricsConfigRejectsInvalidEnabledListener(t *testing.T) {
	for name, configure := range map[string]func(*Config){
		"empty host":     func(c *Config) { c.Metrics.Host = "" },
		"invalid port":   func(c *Config) { c.Metrics.Port = 65536 },
		"relative path":  func(c *Config) { c.Metrics.Path = "metrics" },
		"root path":      func(c *Config) { c.Metrics.Path = "/" },
		"trailing slash": func(c *Config) { c.Metrics.Path = "/metrics/" },
		"query":          func(c *Config) { c.Metrics.Path = "/metrics?x=1" },
		"fragment":       func(c *Config) { c.Metrics.Path = "/metrics#x" },
	} {
		t.Run(name, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			cfg, err := Load()
			require.NoError(t, err)
			cfg.Metrics.Enabled = true
			configure(cfg)
			require.Error(t, cfg.Validate())
		})
	}
}
