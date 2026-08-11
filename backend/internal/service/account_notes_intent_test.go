package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type accountNotesIntentRepositoryStub struct {
	AccountRepository

	account        *Account
	listedShadows  []*Account
	notesIntents   []bool
	fallbackWrites int
}

func cloneAccountForNotesIntentTest(source *Account) *Account {
	if source == nil {
		return nil
	}
	cloned := *source
	cloned.Notes = cloneAccountValuePointer(source.Notes)
	cloned.ProxyID = cloneAccountValuePointer(source.ProxyID)
	cloned.ParentAccountID = cloneAccountValuePointer(source.ParentAccountID)
	cloned.Credentials = shallowCopyMap(source.Credentials)
	return &cloned
}

func (r *accountNotesIntentRepositoryStub) GetByID(context.Context, int64) (*Account, error) {
	return cloneAccountForNotesIntentTest(r.account), nil
}

func (r *accountNotesIntentRepositoryStub) Update(_ context.Context, account *Account) error {
	r.fallbackWrites++
	r.account = cloneAccountForNotesIntentTest(account)
	return nil
}

func (r *accountNotesIntentRepositoryStub) UpdateWithNotesIntent(
	_ context.Context,
	account *Account,
	updateNotes bool,
) error {
	r.notesIntents = append(r.notesIntents, updateNotes)
	currentNotes := cloneAccountValuePointer(r.account.Notes)
	r.account = cloneAccountForNotesIntentTest(account)
	if !updateNotes {
		r.account.Notes = currentNotes
	}
	return nil
}

func (r *accountNotesIntentRepositoryStub) ListShadowsByParent(
	_ context.Context,
	parentID int64,
) ([]*Account, error) {
	shadows := make([]*Account, 0, len(r.listedShadows))
	for _, shadow := range r.listedShadows {
		if shadow.ParentAccountID == nil || *shadow.ParentAccountID != parentID {
			continue
		}
		shadows = append(shadows, cloneAccountForNotesIntentTest(shadow))
	}
	return shadows, nil
}

func TestAccountServiceRoutesNotesFieldIntentToRepository(t *testing.T) {
	initialNotes := "existing note"
	repo := &accountNotesIntentRepositoryStub{account: &Account{
		ID:          42,
		Name:        "intent@example.com",
		Notes:       &initialNotes,
		Status:      StatusActive,
		Credentials: map[string]any{"token": "synthetic"},
	}}
	accountService := NewAccountService(repo, nil)

	priority := 75
	updated, err := accountService.Update(context.Background(), repo.account.ID, UpdateAccountRequest{Priority: &priority})
	require.NoError(t, err)
	require.Equal(t, initialNotes, *updated.Notes)

	replacementNotes := "explicit replacement"
	updated, err = accountService.Update(context.Background(), repo.account.ID, UpdateAccountRequest{Notes: &replacementNotes})
	require.NoError(t, err)
	require.Equal(t, replacementNotes, *updated.Notes)

	require.NoError(t, accountService.UpdateStatus(context.Background(), repo.account.ID, StatusError, "synthetic error"))
	account, err := repo.GetByID(context.Background(), repo.account.ID)
	require.NoError(t, err)
	require.NoError(t, persistAccountCredentials(
		context.Background(),
		repo,
		account,
		map[string]any{"token": "refreshed-synthetic"},
	))

	require.Equal(t, []bool{false, true, false, false}, repo.notesIntents)
	require.Zero(t, repo.fallbackWrites)
	require.Equal(t, replacementNotes, *repo.account.Notes)
}

func TestPropagateAccountProxyToShadowsPreservesConcurrentNotes(t *testing.T) {
	parentID := int64(41)
	shadowID := int64(42)
	oldProxyID := int64(11)
	newProxyID := int64(22)
	staleNotes := "stale note from shadow listing"
	currentNotes := "note committed by one-click import"
	repo := &accountNotesIntentRepositoryStub{
		account: &Account{
			ID:              shadowID,
			ParentAccountID: &parentID,
			QuotaDimension:  QuotaDimensionSpark,
			ProxyID:         &oldProxyID,
			Notes:           &currentNotes,
		},
		listedShadows: []*Account{{
			ID:              shadowID,
			ParentAccountID: &parentID,
			QuotaDimension:  QuotaDimensionSpark,
			ProxyID:         &oldProxyID,
			Notes:           &staleNotes,
		}},
	}

	require.NoError(t, propagateAccountProxyToShadows(
		context.Background(),
		repo,
		parentID,
		&newProxyID,
	))

	require.Equal(t, []bool{false}, repo.notesIntents)
	require.Zero(t, repo.fallbackWrites, "proxy propagation must not use the legacy full-snapshot update")
	require.Equal(t, newProxyID, *repo.account.ProxyID)
	require.Equal(t, currentNotes, *repo.account.Notes)
}
