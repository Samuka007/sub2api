//go:build unit

package service

import (
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
