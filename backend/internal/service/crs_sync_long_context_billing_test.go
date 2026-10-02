//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type crsLongContextAccountRepo struct {
	AccountRepository
	accounts       map[string]*Account
	nextID         int64
	notesIntents   []bool
	fallbackWrites int
}

type crsOpenAILongContextSource struct {
	collection  string
	credentials map[string]any
	extra       map[string]any
}

func newCRSLongContextAccountRepo(existing ...*Account) *crsLongContextAccountRepo {
	repo := &crsLongContextAccountRepo{accounts: make(map[string]*Account)}
	for _, account := range existing {
		if account == nil {
			continue
		}
		crsID, _ := account.Extra["crs_account_id"].(string)
		repo.accounts[crsID] = account
		if account.ID > repo.nextID {
			repo.nextID = account.ID
		}
	}
	return repo
}

func (r *crsLongContextAccountRepo) Create(_ context.Context, account *Account) error {
	r.nextID++
	account.ID = r.nextID
	crsID, _ := account.Extra["crs_account_id"].(string)
	r.accounts[crsID] = account
	return nil
}

func (r *crsLongContextAccountRepo) Update(_ context.Context, account *Account) error {
	r.fallbackWrites++
	crsID, _ := account.Extra["crs_account_id"].(string)
	r.accounts[crsID] = account
	return nil
}

func (r *crsLongContextAccountRepo) UpdateWithNotesIntent(
	_ context.Context,
	account *Account,
	updateNotes bool,
) error {
	r.notesIntents = append(r.notesIntents, updateNotes)
	crsID, _ := account.Extra["crs_account_id"].(string)
	r.accounts[crsID] = account
	return nil
}

func (r *crsLongContextAccountRepo) GetByCRSAccountID(_ context.Context, crsID string) (*Account, error) {
	return r.accounts[crsID], nil
}

func (r *crsLongContextAccountRepo) ListShadowsByParent(_ context.Context, _ int64) ([]*Account, error) {
	return nil, nil
}

func TestCRSSyncOpenAILongContextBilling(t *testing.T) {
	tests := []struct {
		name          string
		collection    string
		credentials   map[string]any
		sourceExtra   map[string]any
		existingExtra map[string]any
		wantAction    string
		wantEnabled   bool
	}{
		{name: "OAuth create defaults missing value disabled", collection: "openaiOAuthAccounts", credentials: map[string]any{"access_token": "oauth-token"}, wantAction: "created"},
		{name: "OAuth create preserves source true", collection: "openaiOAuthAccounts", credentials: map[string]any{"access_token": "oauth-token"}, sourceExtra: map[string]any{openAILongContextBillingEnabledKey: true}, wantAction: "created", wantEnabled: true},
		{name: "OAuth create preserves source false", collection: "openaiOAuthAccounts", credentials: map[string]any{"access_token": "oauth-token"}, sourceExtra: map[string]any{openAILongContextBillingEnabledKey: false}, wantAction: "created"},
		{name: "OAuth update defaults missing value disabled", collection: "openaiOAuthAccounts", credentials: map[string]any{"access_token": "oauth-token"}, existingExtra: map[string]any{"existing": true}, wantAction: "updated"},
		{name: "OAuth update preserves existing true when source omits value", collection: "openaiOAuthAccounts", credentials: map[string]any{"access_token": "oauth-token"}, existingExtra: map[string]any{openAILongContextBillingEnabledKey: true}, wantAction: "updated", wantEnabled: true},
		{name: "OAuth update preserves existing false when source omits value", collection: "openaiOAuthAccounts", credentials: map[string]any{"access_token": "oauth-token"}, existingExtra: map[string]any{openAILongContextBillingEnabledKey: false}, wantAction: "updated"},
		{name: "OAuth update preserves source true over existing false", collection: "openaiOAuthAccounts", credentials: map[string]any{"access_token": "oauth-token"}, sourceExtra: map[string]any{openAILongContextBillingEnabledKey: true}, existingExtra: map[string]any{openAILongContextBillingEnabledKey: false}, wantAction: "updated", wantEnabled: true},
		{name: "OAuth update preserves source false over existing true", collection: "openaiOAuthAccounts", credentials: map[string]any{"access_token": "oauth-token"}, sourceExtra: map[string]any{openAILongContextBillingEnabledKey: false}, existingExtra: map[string]any{openAILongContextBillingEnabledKey: true}, wantAction: "updated"},
		{name: "OAuth rejects malformed source value", collection: "openaiOAuthAccounts", credentials: map[string]any{"access_token": "oauth-token"}, sourceExtra: map[string]any{openAILongContextBillingEnabledKey: "false"}, wantAction: "failed"},
		{name: "OAuth rejects malformed existing value", collection: "openaiOAuthAccounts", credentials: map[string]any{"access_token": "oauth-token"}, existingExtra: map[string]any{openAILongContextBillingEnabledKey: "false"}, wantAction: "failed"},
		{name: "OAuth update rejects malformed source value", collection: "openaiOAuthAccounts", credentials: map[string]any{"access_token": "oauth-token"}, sourceExtra: map[string]any{openAILongContextBillingEnabledKey: "false"}, existingExtra: map[string]any{openAILongContextBillingEnabledKey: true}, wantAction: "failed"},
		{name: "API key create defaults missing value disabled", collection: "openaiResponsesAccounts", credentials: map[string]any{"api_key": "sk-test"}, wantAction: "created"},
		{name: "API key create preserves source true", collection: "openaiResponsesAccounts", credentials: map[string]any{"api_key": "sk-test"}, sourceExtra: map[string]any{openAILongContextBillingEnabledKey: true}, wantAction: "created", wantEnabled: true},
		{name: "API key create preserves source false", collection: "openaiResponsesAccounts", credentials: map[string]any{"api_key": "sk-test"}, sourceExtra: map[string]any{openAILongContextBillingEnabledKey: false}, wantAction: "created"},
		{name: "API key update defaults missing value disabled", collection: "openaiResponsesAccounts", credentials: map[string]any{"api_key": "sk-test"}, existingExtra: map[string]any{"existing": true}, wantAction: "updated"},
		{name: "API key update preserves existing true when source omits value", collection: "openaiResponsesAccounts", credentials: map[string]any{"api_key": "sk-test"}, existingExtra: map[string]any{openAILongContextBillingEnabledKey: true}, wantAction: "updated", wantEnabled: true},
		{name: "API key update preserves existing false when source omits value", collection: "openaiResponsesAccounts", credentials: map[string]any{"api_key": "sk-test"}, existingExtra: map[string]any{openAILongContextBillingEnabledKey: false}, wantAction: "updated"},
		{name: "API key update preserves source true over existing false", collection: "openaiResponsesAccounts", credentials: map[string]any{"api_key": "sk-test"}, sourceExtra: map[string]any{openAILongContextBillingEnabledKey: true}, existingExtra: map[string]any{openAILongContextBillingEnabledKey: false}, wantAction: "updated", wantEnabled: true},
		{name: "API key update preserves source false over existing true", collection: "openaiResponsesAccounts", credentials: map[string]any{"api_key": "sk-test"}, sourceExtra: map[string]any{openAILongContextBillingEnabledKey: false}, existingExtra: map[string]any{openAILongContextBillingEnabledKey: true}, wantAction: "updated"},
		{name: "API key rejects malformed source value", collection: "openaiResponsesAccounts", credentials: map[string]any{"api_key": "sk-test"}, sourceExtra: map[string]any{openAILongContextBillingEnabledKey: "false"}, wantAction: "failed"},
		{name: "API key rejects malformed existing value", collection: "openaiResponsesAccounts", credentials: map[string]any{"api_key": "sk-test"}, existingExtra: map[string]any{openAILongContextBillingEnabledKey: "false"}, wantAction: "failed"},
		{name: "API key update rejects malformed source value", collection: "openaiResponsesAccounts", credentials: map[string]any{"api_key": "sk-test"}, sourceExtra: map[string]any{openAILongContextBillingEnabledKey: "false"}, existingExtra: map[string]any{openAILongContextBillingEnabledKey: true}, wantAction: "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const crsID = "crs-openai-1"
			const existingNotes = "existing local note"
			var existing *Account
			if tt.existingExtra != nil {
				existingExtra := mergeMap(tt.existingExtra, map[string]any{"crs_account_id": crsID})
				accountType := AccountTypeOAuth
				if tt.collection == "openaiResponsesAccounts" {
					accountType = AccountTypeAPIKey
				}
				notes := existingNotes
				existing = &Account{ID: 41, Notes: &notes, Platform: PlatformOpenAI, Type: accountType, Extra: existingExtra}
			}
			repo := newCRSLongContextAccountRepo(existing)
			result := runCRSOpenAILongContextSync(t, repo, crsOpenAILongContextSource{
				collection:  tt.collection,
				credentials: tt.credentials,
				extra:       tt.sourceExtra,
			})

			require.Len(t, result.Items, 1)
			require.Equal(t, tt.wantAction, result.Items[0].Action)
			if tt.wantAction == "failed" {
				require.Contains(t, result.Items[0].Error, "openai_long_context_billing_enabled must be a boolean")
				require.Empty(t, repo.notesIntents)
				require.Zero(t, repo.fallbackWrites)
				return
			}
			if tt.wantAction == "updated" {
				require.Equal(t, []bool{false}, repo.notesIntents)
				require.Zero(t, repo.fallbackWrites)
				require.Equal(t, existingNotes, *repo.accounts[crsID].Notes)
			} else {
				require.Empty(t, repo.notesIntents)
			}
			stored, ok := repo.accounts[crsID].Extra[openAILongContextBillingEnabledKey]
			require.True(t, ok)
			require.Equal(t, tt.wantEnabled, stored)
		})
	}
}

func TestCRSSyncUpdatesEveryExistingAccountWithoutNotesIntent(t *testing.T) {
	existingSpecs := []struct {
		id       int64
		crsID    string
		platform string
		typeName string
	}{
		{id: 101, crsID: "claude-oauth", platform: PlatformAnthropic, typeName: AccountTypeOAuth},
		{id: 102, crsID: "claude-console", platform: PlatformAnthropic, typeName: AccountTypeAPIKey},
		{id: 103, crsID: "openai-oauth", platform: PlatformOpenAI, typeName: AccountTypeOAuth},
		{id: 104, crsID: "openai-key", platform: PlatformOpenAI, typeName: AccountTypeAPIKey},
		{id: 105, crsID: "gemini-oauth", platform: PlatformGemini, typeName: AccountTypeOAuth},
		{id: 106, crsID: "gemini-key", platform: PlatformGemini, typeName: AccountTypeAPIKey},
	}
	existing := make([]*Account, 0, len(existingSpecs))
	wantNotes := make(map[string]string, len(existingSpecs))
	for _, spec := range existingSpecs {
		notes := "local note for " + spec.crsID
		wantNotes[spec.crsID] = notes
		existing = append(existing, &Account{
			ID:          spec.id,
			Name:        spec.crsID,
			Notes:       &notes,
			Platform:    spec.platform,
			Type:        spec.typeName,
			Credentials: map[string]any{},
			Extra:       map[string]any{"crs_account_id": spec.crsID},
			Status:      StatusActive,
		})
	}
	repo := newCRSLongContextAccountRepo(existing...)

	exported := crsExportResponse{Success: true}
	exported.Data.ClaudeAccounts = []crsClaudeAccount{{
		Kind: "claude", ID: "claude-oauth", Name: "Claude OAuth", AuthType: AccountTypeOAuth,
		IsActive: true, Schedulable: true, Credentials: map[string]any{"access_token": "synthetic-access-token"},
	}}
	exported.Data.ClaudeConsoleAccounts = []crsConsoleAccount{{
		Kind: "claude-console", ID: "claude-console", Name: "Claude Console",
		IsActive: true, Schedulable: true, Credentials: map[string]any{"api_key": "synthetic-api-key"},
	}}
	exported.Data.OpenAIOAuthAccounts = []crsOpenAIOAuthAccount{{
		Kind: "openai-oauth", ID: "openai-oauth", Name: "OpenAI OAuth",
		IsActive: true, Schedulable: true, Credentials: map[string]any{"access_token": "synthetic-access-token"},
	}}
	exported.Data.OpenAIResponsesAccounts = []crsOpenAIResponsesAccount{{
		Kind: "openai-key", ID: "openai-key", Name: "OpenAI Key",
		IsActive: true, Schedulable: true, Credentials: map[string]any{"api_key": "synthetic-api-key"},
	}}
	exported.Data.GeminiOAuthAccounts = []crsGeminiOAuthAccount{{
		Kind: "gemini-oauth", ID: "gemini-oauth", Name: "Gemini OAuth",
		IsActive: true, Schedulable: true, Credentials: map[string]any{"refresh_token": "synthetic-refresh-token"},
	}}
	exported.Data.GeminiAPIKeyAccounts = []crsGeminiAPIKeyAccount{{
		Kind: "gemini-key", ID: "gemini-key", Name: "Gemini Key",
		IsActive: true, Schedulable: true, Credentials: map[string]any{"api_key": "synthetic-api-key"},
	}}

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/web/auth/login" {
			_, _ = response.Write([]byte(`{"success":true,"token":"synthetic-admin-token"}`))
			return
		}
		require.Equal(t, "/admin/sync/export-accounts", request.URL.Path)
		require.NoError(t, json.NewEncoder(response).Encode(exported))
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	result, err := NewCRSSyncService(repo, nil, nil, nil, nil, cfg).SyncFromCRS(
		context.Background(),
		SyncFromCRSInput{BaseURL: server.URL, Username: "synthetic", Password: "synthetic"},
	)
	require.NoError(t, err)
	require.Equal(t, len(existingSpecs), result.Updated)
	require.Zero(t, result.Failed)
	require.Equal(t, make([]bool, len(existingSpecs)), repo.notesIntents)
	require.Zero(t, repo.fallbackWrites)
	for _, spec := range existingSpecs {
		require.Equal(t, wantNotes[spec.crsID], *repo.accounts[spec.crsID].Notes)
	}
}

func runCRSOpenAILongContextSync(t *testing.T, repo AccountRepository, source crsOpenAILongContextSource) *SyncFromCRSResult {
	t.Helper()
	account := map[string]any{
		"kind":        "openai",
		"id":          "crs-openai-1",
		"name":        "OpenAI CRS",
		"isActive":    true,
		"schedulable": true,
		"credentials": source.credentials,
	}
	if source.extra != nil {
		account["extra"] = source.extra
	}

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/web/auth/login" {
			_, _ = response.Write([]byte(`{"success":true,"token":"admin-token"}`))
			return
		}
		require.Equal(t, "/admin/sync/export-accounts", request.URL.Path)
		require.NoError(t, json.NewEncoder(response).Encode(map[string]any{
			"success": true,
			"data":    map[string]any{source.collection: []any{account}},
		}))
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	service := NewCRSSyncService(repo, nil, nil, nil, nil, cfg)
	result, err := service.SyncFromCRS(context.Background(), SyncFromCRSInput{
		BaseURL:  server.URL,
		Username: "admin",
		Password: "password",
	})
	require.NoError(t, err)
	return result
}
