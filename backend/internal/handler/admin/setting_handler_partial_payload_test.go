//go:build unit

package admin

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/stretchr/testify/require"
)

type turnstileValidationVerifierStub struct {
	secret string
	calls  int
}

func (s *turnstileValidationVerifierStub) VerifyToken(_ context.Context, secretKey, _, _ string) (*service.TurnstileVerifyResponse, error) {
	s.secret = secretKey
	s.calls++
	return &service.TurnstileVerifyResponse{ErrorCodes: []string{"invalid-input-secret"}}, nil
}

type aliyunCredentialVerifierStub struct {
	calls int
	cred  service.AliyunCaptchaCredentials
	err   error
}

func (s *aliyunCredentialVerifierStub) VerifyCaptcha(_ context.Context, cred service.AliyunCaptchaCredentials, _ string) (*service.AliyunCaptchaVerifyResult, error) {
	s.calls++
	s.cred = cred
	return &service.AliyunCaptchaVerifyResult{}, s.err
}

// Saving settings is a whole-document PUT. A client that sends only the field it
// cares about must not reset everything else: a payload as small as
// `{"risk_control_enabled":true}` used to clear site_name, after which
// getStringOrDefault rendered the empty value as the built-in default and the
// login page silently changed name.

func TestUpdateSettingsPartialPayloadKeepsUnsentKeys(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeySiteName:           "Example Gateway",
		service.SettingKeySiteSubtitle:       "Example Gateway Platform",
		service.SettingKeySMTPHost:           "smtp.example.com",
		service.SettingKeySMTPFrom:           "noreply@example.com",
		service.SettingKeyTurnstileEnabled:   "true",
		service.SettingKeyTurnstileSiteKey:   "stored-site-key",
		service.SettingKeyTurnstileSecretKey: "stored-secret-key",
	})

	rec := doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, "true", repo.values[service.SettingKeyRiskControlEnabled],
		"the field the caller actually sent must be written")

	require.Equal(t, "Example Gateway", repo.values[service.SettingKeySiteName])
	require.Equal(t, "Example Gateway Platform", repo.values[service.SettingKeySiteSubtitle])
	require.Equal(t, "smtp.example.com", repo.values[service.SettingKeySMTPHost])
	require.Equal(t, "noreply@example.com", repo.values[service.SettingKeySMTPFrom])
	require.Equal(t, "true", repo.values[service.SettingKeyTurnstileEnabled])
}

func TestUpdateSettingsPartialPayloadKeepsOpsMonitoringRuntimeState(t *testing.T) {
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyOpsMonitoringEnabled: "true",
	})
	opsService := &service.OpsService{}
	opsService.SetMonitoringEnabled(true)
	h.opsService = opsService

	rec := doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, opsService.IsMonitoringEnabled(context.Background()))
}

// A full payload keeps whole-document semantics: fields explicitly set to their
// zero value are still cleared.
func TestUpdateSettingsFullPayloadStillClearsSentEmptyFields(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeySiteName: "Example Gateway",
	})

	rec := doUpdateSettings(t, h, map[string]any{"site_name": ""}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, "", repo.values[service.SettingKeySiteName],
		"an explicitly sent empty value is a deliberate clear, not an omission")
}

// smtp_from_email is the one request field whose JSON name differs from its
// setting key; the alias keeps it from being treated as always-omitted.
func TestUpdateSettingsSMTPFromAliasIsWritable(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeySMTPFrom: "old@example.com",
	})

	rec := doUpdateSettings(t, h, map[string]any{"smtp_from_email": "new@example.com"}, nil)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, "new@example.com", repo.values[service.SettingKeySMTPFrom])
}

func TestUpdateSettingsRejectsTwoCaptchaProviders(t *testing.T) {
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyTurnstileEnabled:   "true",
		service.SettingKeyTurnstileSiteKey:   "site-key",
		service.SettingKeyTurnstileSecretKey: "turnstile-secret",
	})

	rec := doUpdateSettings(t, h, map[string]any{
		"turnstile_enabled":                true,
		"turnstile_site_key":               "site-key",
		"turnstile_secret_key":             "turnstile-secret",
		"tencent_captcha_enabled":          true,
		"tencent_captcha_app_id":           "123456789",
		"tencent_captcha_app_secret_key":   "app-secret",
		"tencent_captcha_cloud_secret_id":  "cloud-secret-id",
		"tencent_captcha_cloud_secret_key": "cloud-secret-key",
	}, nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "cannot be enabled at the same time")
}

func TestUpdateSettingsRejectsInvalidAliyunCaptchaRegion(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyAliyunCaptchaRegion: service.AliyunCaptchaRegionSGP,
	})

	rec := doUpdateSettings(t, h, map[string]any{
		"aliyun_captcha_region": "sg",
	}, nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "must be cn or sgp")
	require.Equal(t, service.AliyunCaptchaRegionSGP, repo.values[service.SettingKeyAliyunCaptchaRegion])
}

func TestUpdateSettingsAliyunCaptchaRegionsAndCredentialValidation(t *testing.T) {
	for _, region := range []string{service.AliyunCaptchaRegionCN, service.AliyunCaptchaRegionSGP} {
		t.Run(region, func(t *testing.T) {
			h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
			verifier := &aliyunCredentialVerifierStub{}
			h.aliyunCaptchaService = service.NewAliyunCaptchaService(h.settingService, verifier)
			rec := doUpdateSettings(t, h, map[string]any{
				"aliyun_captcha_enabled":           true,
				"aliyun_captcha_access_key_id":     "access-id",
				"aliyun_captcha_access_key_secret": "access-secret",
				"aliyun_captcha_scene_id":          "scene-id",
				"aliyun_captcha_prefix":            "prefix",
				"aliyun_captcha_region":            region,
			}, nil)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, 1, verifier.calls)
			require.Equal(t, "access-id", verifier.cred.AccessKeyID)
			require.Equal(t, "access-secret", verifier.cred.AccessKeySecret)
			require.Equal(t, "scene-id", verifier.cred.SceneID)
			require.Equal(t, region, repo.values[service.SettingKeyAliyunCaptchaRegion])
		})
	}
}

func TestUpdateSettingsRetainsStoredAliyunCaptchaSecretWhenInputEmpty(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyAliyunCaptchaEnabled:         "true",
		service.SettingKeyAliyunCaptchaAccessKeyID:     "stored-id",
		service.SettingKeyAliyunCaptchaAccessKeySecret: "stored-secret",
		service.SettingKeyAliyunCaptchaSceneID:         "stored-scene",
		service.SettingKeyAliyunCaptchaPrefix:          "stored-prefix",
		service.SettingKeyAliyunCaptchaRegion:          service.AliyunCaptchaRegionCN,
	})
	rec := doUpdateSettings(t, h, map[string]any{
		"aliyun_captcha_access_key_secret": "",
		"aliyun_captcha_prefix":            "new-prefix",
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "stored-secret", repo.values[service.SettingKeyAliyunCaptchaAccessKeySecret])
	require.Equal(t, "new-prefix", repo.values[service.SettingKeyAliyunCaptchaPrefix])
}

func TestUpdateSettingsAliyunCaptchaCredentialFailureDoesNotPersist(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeySiteName: "unchanged",
	})
	verifier := &aliyunCredentialVerifierStub{err: &service.AliyunCaptchaAPIError{Code: "InvalidAccessKeyId.NotFound", Message: "invalid"}}
	h.aliyunCaptchaService = service.NewAliyunCaptchaService(h.settingService, verifier)
	rec := doUpdateSettings(t, h, map[string]any{
		"aliyun_captcha_enabled":           true,
		"aliyun_captcha_access_key_id":     "bad-id",
		"aliyun_captcha_access_key_secret": "bad-secret",
		"aliyun_captcha_scene_id":          "scene-id",
		"aliyun_captcha_prefix":            "prefix",
		"aliyun_captcha_region":            service.AliyunCaptchaRegionCN,
	}, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, 1, verifier.calls)
	require.Equal(t, "unchanged", repo.values[service.SettingKeySiteName])
	_, persisted := repo.values[service.SettingKeyAliyunCaptchaAccessKeyID]
	require.False(t, persisted)
}

func TestUpdateSettingsRejectsAliyunCaptchaWithAnotherProvider(t *testing.T) {
	for _, conflictingField := range []string{"turnstile_enabled", "tencent_captcha_enabled"} {
		t.Run(conflictingField, func(t *testing.T) {
			h, _ := newStepUpSwitchTestHandler(t, map[string]string{})
			rec := doUpdateSettings(t, h, map[string]any{
				"aliyun_captcha_enabled": true,
				conflictingField:         true,
			}, nil)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), "cannot be enabled at the same time")
		})
	}
}

func TestUpdateSettingsRequiresFourTencentCaptchaCredentialsWhenEnabled(t *testing.T) {
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{})

	rec := doUpdateSettings(t, h, map[string]any{
		"tencent_captcha_enabled": true,
		"tencent_captcha_app_id":  "123456789",
	}, nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "AppSecretKey")
}

func TestUpdateSettingsRetainsStoredTencentCaptchaCredentialsWhenInputsEmpty(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyTencentCaptchaAppSecretKey:   "stored-app-secret",
		service.SettingKeyTencentCaptchaCloudSecretID:  "stored-cloud-secret-id",
		service.SettingKeyTencentCaptchaCloudSecretKey: "stored-cloud-secret-key",
	})

	rec := doUpdateSettings(t, h, map[string]any{
		"tencent_captcha_enabled":          true,
		"tencent_captcha_app_id":           "123456789",
		"tencent_captcha_app_secret_key":   "",
		"tencent_captcha_cloud_secret_id":  "",
		"tencent_captcha_cloud_secret_key": "",
	}, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "stored-app-secret", repo.values[service.SettingKeyTencentCaptchaAppSecretKey])
	require.Equal(t, "stored-cloud-secret-id", repo.values[service.SettingKeyTencentCaptchaCloudSecretID])
	require.Equal(t, "stored-cloud-secret-key", repo.values[service.SettingKeyTencentCaptchaCloudSecretKey])
}

func TestUpdateSettingsValidatesTencentCaptchaAppIDWhenEnabledFlagIsOmitted(t *testing.T) {
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyTencentCaptchaEnabled:        "true",
		service.SettingKeyTencentCaptchaAppID:          "123456789",
		service.SettingKeyTencentCaptchaAppSecretKey:   "stored-app-secret",
		service.SettingKeyTencentCaptchaCloudSecretID:  "stored-cloud-secret-id",
		service.SettingKeyTencentCaptchaCloudSecretKey: "stored-cloud-secret-key",
	})

	rec := doUpdateSettings(t, h, map[string]any{
		"tencent_captcha_app_id": "not-a-number",
	}, nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "positive integer")
}

func TestUpdateSettingsPartialTurnstileSecretStillValidatesEffectiveEnabledProvider(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyTurnstileEnabled:   "true",
		service.SettingKeyTurnstileSiteKey:   "stored-site-key",
		service.SettingKeyTurnstileSecretKey: "stored-secret-key",
	})
	verifier := &turnstileValidationVerifierStub{}
	h.turnstileService = service.NewTurnstileService(h.settingService, verifier)

	rec := doUpdateSettings(t, h, map[string]any{
		"turnstile_secret_key": "invalid-new-secret",
	}, nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, 1, verifier.calls)
	require.Equal(t, "invalid-new-secret", verifier.secret)
	require.Equal(t, "stored-secret-key", repo.values[service.SettingKeyTurnstileSecretKey])
}
