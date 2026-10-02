package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

var _ service.OneClickAccountNotesRepository = (*accountRepository)(nil)

// Keep full notes out of the materialized candidate set. The outer window
// first measures the complete bounded state, and the correlated CASE subquery
// reads notes only for a normal result. Oversized state therefore produces one
// small sentinel instead of streaming a response that can be many GiB.
const listOneClickAccountNoteTargetsQuery = `
	WITH candidates AS MATERIALIZED (
		SELECT id, name, COALESCE(octet_length(notes), 0) AS notes_bytes
		FROM accounts
		WHERE deleted_at IS NULL
			AND translate(name, 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', 'abcdefghijklmnopqrstuvwxyz') = ANY($1::text[])
		ORDER BY id
		LIMIT $2
	), measured AS (
		SELECT id, name, notes_bytes,
			COUNT(*) OVER () >= $2 OR COALESCE(SUM(notes_bytes) OVER (), 0) > $3 AS state_too_large,
			ROW_NUMBER() OVER (ORDER BY id) AS candidate_row
		FROM candidates
	)
	SELECT
		CASE WHEN state_too_large THEN 0 ELSE id END AS id,
		CASE WHEN state_too_large THEN '' ELSE name END AS name,
		CASE
			WHEN state_too_large THEN NULL
			ELSE (
				SELECT account.notes
				FROM accounts AS account
				WHERE account.id = measured.id
			)
		END AS notes,
		CASE WHEN state_too_large THEN 0 ELSE notes_bytes END AS notes_bytes,
		state_too_large
	FROM measured
	WHERE NOT state_too_large OR candidate_row = 1
	ORDER BY measured.id
`

const setOneClickAccountNotesLockTimeoutQuery = `SET LOCAL lock_timeout = '1s'`

const lockOneClickAccountNotesTableQuery = `LOCK TABLE accounts IN EXCLUSIVE MODE`

const lockOneClickAccountNoteTargetsQuery = `
	WITH candidates AS MATERIALIZED (
		SELECT id, name, COALESCE(octet_length(notes), 0) AS notes_bytes
		FROM accounts
		WHERE deleted_at IS NULL
			AND translate(name, 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', 'abcdefghijklmnopqrstuvwxyz') = ANY($1::text[])
		ORDER BY id
		LIMIT $2
		FOR NO KEY UPDATE
	), measured AS (
		SELECT id, name, notes_bytes,
			COUNT(*) OVER () >= $2 OR COALESCE(SUM(notes_bytes) OVER (), 0) > $3 AS state_too_large,
			ROW_NUMBER() OVER (ORDER BY id) AS candidate_row
		FROM candidates
	)
	SELECT
		CASE WHEN state_too_large THEN 0 ELSE id END AS id,
		CASE WHEN state_too_large THEN '' ELSE name END AS name,
		CASE
			WHEN state_too_large THEN NULL
			ELSE (
				SELECT account.notes
				FROM accounts AS account
				WHERE account.id = measured.id
			)
		END AS notes,
		CASE WHEN state_too_large THEN 0 ELSE notes_bytes END AS notes_bytes,
		state_too_large
	FROM measured
	WHERE NOT state_too_large OR candidate_row = 1
	ORDER BY measured.id
`

const applyOneClickAccountNotesQuery = `
	WITH input AS MATERIALIZED (
		SELECT id, notes
		FROM jsonb_to_recordset($1::jsonb) AS item(id bigint, notes text)
	)
	UPDATE accounts AS account
	SET notes = input.notes,
		updated_at = NOW()
	FROM input
	WHERE account.id = input.id
		AND account.deleted_at IS NULL
`

func (r *accountRepository) ListOneClickAccountNoteTargets(
	ctx context.Context,
	matchNames []string,
) ([]service.OneClickAccountNoteTarget, error) {
	if r == nil || r.sql == nil {
		return nil, service.ErrOneClickAccountNotesRepositoryUnavailable
	}
	names, err := normalizeOneClickAccountNoteMatchNames(matchNames)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return []service.OneClickAccountNoteTarget{}, nil
	}
	queryer := r.sql
	if tx := dbent.TxFromContext(ctx); tx != nil {
		queryer = tx.Client()
	}
	rows, err := queryer.QueryContext(
		ctx,
		listOneClickAccountNoteTargetsQuery,
		pq.Array(names),
		service.OneClickAccountNotesMaxMatchedAccounts+1,
		service.OneClickAccountNotesMaxTargetStateBytes,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return readOneClickAccountNoteTargets(rows)
}

func (r *accountRepository) ApplyOneClickAccountNotes(
	ctx context.Context,
	matchNames []string,
	updates []service.OneClickAccountNoteUpdate,
) error {
	if r == nil || r.client == nil {
		return service.ErrOneClickAccountNotesRepositoryUnavailable
	}

	names, expected, err := validateOneClickAccountNotesPlan(matchNames, updates)
	if err != nil {
		return err
	}

	contextTx := dbent.TxFromContext(ctx)
	client := r.client
	var tx *dbent.Tx
	if contextTx != nil {
		client = contextTx.Client()
	} else {
		tx, err = r.client.Tx(ctx)
		if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
			return err
		}
		if tx != nil {
			defer func() { _ = tx.Rollback() }()
			ctx = dbent.NewTxContext(ctx, tx)
			client = tx.Client()
		}
	}

	// The row query below cannot lock absent matches. EXCLUSIVE MODE blocks both
	// account DML and new SELECT FOR UPDATE/NO KEY UPDATE statements, so a new or
	// renamed same-name account cannot appear and a row-lock/table-lock upgrade
	// cycle cannot form while this transaction validates and writes its plan.
	// Plain account reads remain available. The infrequent admin import holds the
	// lock only for its projection comparison plus one batch UPDATE.
	// SET LOCAL keeps the timeout scoped to this apply transaction.
	if _, err := client.ExecContext(ctx, setOneClickAccountNotesLockTimeoutQuery); err != nil {
		return err
	}
	if _, err := client.ExecContext(ctx, lockOneClickAccountNotesTableQuery); err != nil {
		return translateOneClickAccountNotesTableLockError(err)
	}

	current, err := lockOneClickAccountNoteTargets(ctx, client, names)
	if err != nil {
		return err
	}
	if !sameOneClickAccountNoteTargets(current, expected) {
		return service.ErrOneClickAccountNotesPreviewStale
	}

	changed := make([]oneClickAccountNoteMutation, 0, len(expected))
	for _, update := range expected {
		if update.ExpectedNotes != nil && *update.ExpectedNotes == update.Notes {
			continue
		}
		changed = append(changed, oneClickAccountNoteMutation{ID: update.AccountID, Notes: update.Notes})
	}
	if len(changed) > 0 {
		payload, marshalErr := json.Marshal(changed)
		if marshalErr != nil {
			return service.ErrOneClickAccountNotesPlanInvalid.WithCause(marshalErr)
		}
		if len(payload) > service.OneClickAccountNotesMaxEncodedMutationBytes {
			return service.ErrOneClickAccountNotesPlanTooLarge
		}
		result, execErr := client.ExecContext(ctx, applyOneClickAccountNotesQuery, string(payload))
		if execErr != nil {
			return execErr
		}
		affected, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return rowsErr
		}
		if affected != int64(len(changed)) {
			return service.ErrOneClickAccountNotesPreviewStale
		}
	}

	if tx != nil {
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func translateOneClickAccountNotesTableLockError(err error) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr != nil && pqErr.Code == pq.ErrorCode("55P03") {
		return service.ErrOneClickAccountNotesBusy.WithCause(err)
	}
	return err
}

type oneClickAccountNoteMutation struct {
	ID    int64  `json:"id"`
	Notes string `json:"notes"`
}

func validateOneClickAccountNotesPlan(
	matchNames []string,
	updates []service.OneClickAccountNoteUpdate,
) ([]string, []service.OneClickAccountNoteUpdate, error) {
	names, err := normalizeOneClickAccountNoteMatchNames(matchNames)
	if err != nil {
		return nil, nil, err
	}
	if len(updates) > service.OneClickAccountNotesMaxMatchedAccounts {
		return nil, nil, service.ErrOneClickAccountNotesPlanTooLarge
	}
	nameSet := make(map[string]struct{}, len(names))
	for _, name := range names {
		nameSet[name] = struct{}{}
	}

	expected := append([]service.OneClickAccountNoteUpdate(nil), updates...)
	sort.Slice(expected, func(i, j int) bool { return expected[i].AccountID < expected[j].AccountID })
	seenIDs := make(map[int64]struct{}, len(expected))
	targetStateBytes := 0
	mutationBytes := 0
	for _, update := range expected {
		if update.AccountID <= 0 || update.ExpectedName == "" || update.Notes == "" {
			return nil, nil, service.ErrOneClickAccountNotesPlanInvalid
		}
		if _, ok := nameSet[foldAccountNoteImportASCII(update.ExpectedName)]; !ok {
			return nil, nil, service.ErrOneClickAccountNotesPlanInvalid
		}
		if _, duplicate := seenIDs[update.AccountID]; duplicate {
			return nil, nil, service.ErrOneClickAccountNotesPlanInvalid
		}
		seenIDs[update.AccountID] = struct{}{}
		if update.ExpectedNotes != nil {
			if len(*update.ExpectedNotes) > service.OneClickAccountNotesMaxTargetStateBytes-targetStateBytes {
				return nil, nil, service.ErrOneClickAccountNotesPlanTooLarge
			}
			targetStateBytes += len(*update.ExpectedNotes)
		}
		if update.ExpectedNotes == nil || *update.ExpectedNotes != update.Notes {
			projectedBytes := len(update.Notes) + 32
			if projectedBytes > service.OneClickAccountNotesMaxMutationBytes-mutationBytes {
				return nil, nil, service.ErrOneClickAccountNotesPlanTooLarge
			}
			mutationBytes += projectedBytes
		}
	}
	return names, expected, nil
}

func normalizeOneClickAccountNoteMatchNames(matchNames []string) ([]string, error) {
	names := append([]string(nil), matchNames...)
	sort.Strings(names)
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" || name != foldAccountNoteImportASCII(name) {
			return nil, service.ErrOneClickAccountNotesPlanInvalid
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, service.ErrOneClickAccountNotesPlanInvalid
		}
		seen[name] = struct{}{}
	}
	return names, nil
}

func lockOneClickAccountNoteTargets(
	ctx context.Context,
	client *dbent.Client,
	matchNames []string,
) ([]service.OneClickAccountNoteTarget, error) {
	rows, err := client.QueryContext(
		ctx,
		lockOneClickAccountNoteTargetsQuery,
		pq.Array(matchNames),
		service.OneClickAccountNotesMaxMatchedAccounts+1,
		service.OneClickAccountNotesMaxTargetStateBytes,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return readOneClickAccountNoteTargets(rows)
}

func readOneClickAccountNoteTargets(rows *sql.Rows) ([]service.OneClickAccountNoteTarget, error) {
	targets := make([]service.OneClickAccountNoteTarget, 0)
	totalNotesBytes := int64(0)
	for rows.Next() {
		var target service.OneClickAccountNoteTarget
		var notes sql.NullString
		var notesBytes int64
		var stateTooLarge bool
		if err := rows.Scan(&target.ID, &target.Name, &notes, &notesBytes, &stateTooLarge); err != nil {
			return nil, err
		}
		if stateTooLarge {
			return nil, service.ErrOneClickAccountNotesPlanTooLarge
		}
		if len(targets) >= service.OneClickAccountNotesMaxMatchedAccounts ||
			notesBytes < 0 ||
			notesBytes > int64(service.OneClickAccountNotesMaxTargetStateBytes)-totalNotesBytes {
			return nil, service.ErrOneClickAccountNotesPlanTooLarge
		}
		totalNotesBytes += notesBytes
		if notes.Valid {
			target.Notes = &notes.String
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return targets, nil
}

func sameOneClickAccountNoteTargets(
	current []service.OneClickAccountNoteTarget,
	expected []service.OneClickAccountNoteUpdate,
) bool {
	if len(current) != len(expected) {
		return false
	}
	for index := range current {
		if current[index].ID != expected[index].AccountID || current[index].Name != expected[index].ExpectedName {
			return false
		}
		if !sameOptionalAccountNote(current[index].Notes, expected[index].ExpectedNotes) {
			return false
		}
	}
	return true
}

func sameOptionalAccountNote(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func foldAccountNoteImportASCII(value string) string {
	buffer := []byte(value)
	for index, char := range buffer {
		if char >= 'A' && char <= 'Z' {
			buffer[index] = char + ('a' - 'A')
		}
	}
	return string(buffer)
}
