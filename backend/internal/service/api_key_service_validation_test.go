//go:build unit

package service

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPIKeyServiceValidateCustomKeyLengthBounds(t *testing.T) {
	svc := &APIKeyService{}

	require.ErrorIs(t, svc.ValidateCustomKey(strings.Repeat("x", 15)), ErrAPIKeyTooShort)
	require.NoError(t, svc.ValidateCustomKey(strings.Repeat("x", 16)))
	require.NoError(t, svc.ValidateCustomKey(strings.Repeat("x", MaxAPIKeyCredentialBytes)))
	require.ErrorIs(t, svc.ValidateCustomKey(strings.Repeat("x", MaxAPIKeyCredentialBytes+1)), ErrAPIKeyTooLong)
}

func TestValidateCreateAPIKeyRequestNumericLimits(t *testing.T) {
	positiveExpiry := 1
	require.NoError(t, validateCreateAPIKeyRequest(CreateAPIKeyRequest{
		Quota: 1e100, RateLimit5h: 1e100, ExpiresInDays: &positiveExpiry,
	}))
	require.NoError(t, validateCreateAPIKeyRequest(CreateAPIKeyRequest{}))

	invalidExpiry := 0
	tests := []CreateAPIKeyRequest{
		{Quota: -1},
		{Quota: math.NaN()},
		{Quota: math.Inf(1)},
		{RateLimit5h: -1},
		{RateLimit1d: math.NaN()},
		{RateLimit7d: math.Inf(-1)},
		{ExpiresInDays: &invalidExpiry},
	}
	for _, req := range tests {
		require.Error(t, validateCreateAPIKeyRequest(req))
	}
}

func TestValidateUpdateAPIKeyRequestNumericLimits(t *testing.T) {
	zero, large, negative, nan, inf := 0.0, 1e100, -1.0, math.NaN(), math.Inf(1)
	require.NoError(t, validateUpdateAPIKeyRequest(UpdateAPIKeyRequest{Quota: &zero, RateLimit7d: &large}))

	for _, req := range []UpdateAPIKeyRequest{
		{Quota: &negative},
		{RateLimit5h: &nan},
		{RateLimit1d: &inf},
		{RateLimit7d: &negative},
	} {
		require.Error(t, validateUpdateAPIKeyRequest(req))
	}
}
