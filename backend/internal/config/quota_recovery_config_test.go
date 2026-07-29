//go:build unit

package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestQuotaRecoveryDefaults(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	setDefaults()

	var cfg Config
	require.NoError(t, viper.Unmarshal(&cfg))
	require.False(t, cfg.QuotaRecovery.Enabled)
	require.Equal(t, 86400, cfg.QuotaRecovery.IntervalSeconds)
	require.Equal(t, 50, cfg.QuotaRecovery.BatchSize)
	require.Equal(t, 3, cfg.QuotaRecovery.Concurrency)
	require.Equal(t, 75, cfg.QuotaRecovery.TimeoutSeconds)
	require.Equal(t, 10, cfg.QuotaRecovery.JitterSeconds)
}

func TestQuotaRecoveryRequiresTwoDatabaseConnections(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	cfg.RunMode = RunModeStandard
	cfg.QuotaRecovery.Enabled = true
	cfg.Database.MaxOpenConns = 1
	cfg.Database.MaxIdleConns = 1

	err = cfg.Validate()
	require.EqualError(t, err, "quota_recovery requires database.max_open_conns >= 2")
}

func TestQuotaRecoveryAllowsSupportedRunModes(t *testing.T) {
	tests := []struct {
		name    string
		runMode string
	}{
		{name: "standard", runMode: RunModeStandard},
		{name: "simple", runMode: RunModeSimple},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			cfg, err := Load()
			require.NoError(t, err)
			cfg.RunMode = tt.runMode
			cfg.QuotaRecovery.Enabled = true

			require.NoError(t, cfg.Validate())
		})
	}
}

func TestQuotaRecoveryEnvironmentOverrides(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("RUN_MODE", RunModeStandard)
	t.Setenv("QUOTA_RECOVERY_ENABLED", "true")
	t.Setenv("QUOTA_RECOVERY_INTERVAL_SECONDS", "86400")
	t.Setenv("QUOTA_RECOVERY_BATCH_SIZE", "25")
	t.Setenv("QUOTA_RECOVERY_CONCURRENCY", "2")
	t.Setenv("QUOTA_RECOVERY_TIMEOUT_SECONDS", "15")
	t.Setenv("QUOTA_RECOVERY_JITTER_SECONDS", "5")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, RunModeStandard, cfg.RunMode)
	require.True(t, cfg.QuotaRecovery.Enabled)
	require.Equal(t, 86400, cfg.QuotaRecovery.IntervalSeconds)
	require.Equal(t, 25, cfg.QuotaRecovery.BatchSize)
	require.Equal(t, 2, cfg.QuotaRecovery.Concurrency)
	require.Equal(t, 15, cfg.QuotaRecovery.TimeoutSeconds)
	require.Equal(t, 5, cfg.QuotaRecovery.JitterSeconds)
}
