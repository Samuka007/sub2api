package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type settingRepository struct {
	client *ent.Client
}

func NewSettingRepository(client *ent.Client) service.SettingRepository {
	return &settingRepository{client: client}
}

func (r *settingRepository) Get(ctx context.Context, key string) (*service.Setting, error) {
	m, err := r.client.Setting.Query().Where(setting.KeyEQ(key)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, service.ErrSettingNotFound
		}
		return nil, err
	}
	return &service.Setting{
		ID:        m.ID,
		Key:       m.Key,
		Value:     m.Value,
		UpdatedAt: m.UpdatedAt,
	}, nil
}

func (r *settingRepository) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (r *settingRepository) Set(ctx context.Context, key, value string) error {
	now := time.Now()
	return r.client.Setting.
		Create().
		SetKey(key).
		SetValue(value).
		SetUpdatedAt(now).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
}

// CompareAndSet atomically replaces a setting only when its raw value still
// matches the value read by the caller. It also handles first creation.
func (r *settingRepository) CompareAndSet(ctx context.Context, key, oldValue, newValue string) (bool, error) {
	now := time.Now()
	updated, err := r.client.Setting.Update().
		Where(setting.KeyEQ(key), setting.ValueEQ(oldValue)).
		SetValue(newValue).
		SetUpdatedAt(now).
		Save(ctx)
	if err != nil {
		return false, err
	}
	if updated == 1 {
		return true, nil
	}
	if oldValue != "" {
		return false, nil
	}
	err = r.client.Setting.Create().
		SetKey(key).
		SetValue(newValue).
		SetUpdatedAt(now).
		Exec(ctx)
	if err == nil {
		return true, nil
	}
	if ent.IsConstraintError(err) {
		return false, nil
	}
	return false, err
}

// AdvanceOpenAICodexSyncedVersion atomically checks the auto-sync switch and
// advances the synced version under row locks. When disabling auto-sync wins
// the settings-row lock first, no later sync write can commit after it.
func (r *settingRepository) AdvanceOpenAICodexSyncedVersion(ctx context.Context, latest string) (previous string, updated bool, err error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return "", false, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	now := time.Now()
	defaults := [...]struct{ key, value string }{
		{service.SettingKeyOpenAICodexVersionAutoSyncEnabled, "true"},
		{service.SettingKeyOpenAICodexClientVersionSynced, ""},
	}
	for _, item := range defaults {
		if createErr := tx.Setting.Create().
			SetKey(item.key).
			SetValue(item.value).
			SetUpdatedAt(now).
			OnConflictColumns(setting.FieldKey).
			DoNothing().
			Exec(ctx); createErr != nil && !errors.Is(createErr, sql.ErrNoRows) {
			// PostgreSQL returns no row when ON CONFLICT DO NOTHING wins.
			// The existing setting is the successful initialization result.
			return "", false, createErr
		}
	}

	autoSync, queryErr := tx.Setting.Query().
		Where(setting.KeyEQ(service.SettingKeyOpenAICodexVersionAutoSyncEnabled)).
		ForUpdate().
		Only(ctx)
	if queryErr != nil {
		return "", false, queryErr
	}
	if autoSync.Value != "" && autoSync.Value != "true" {
		if err = tx.Commit(); err != nil {
			return "", false, err
		}
		return "", false, nil
	}

	current, queryErr := tx.Setting.Query().
		Where(setting.KeyEQ(service.SettingKeyOpenAICodexClientVersionSynced)).
		ForUpdate().
		Only(ctx)
	if queryErr != nil {
		return "", false, queryErr
	}
	previous = current.Value
	if normalized := service.NormalizeCodexClientVersion(previous); normalized != "" && service.CompareVersions(latest, normalized) <= 0 {
		if err = tx.Commit(); err != nil {
			return "", false, err
		}
		return previous, false, nil
	}

	err = tx.Setting.UpdateOneID(current.ID).
		SetValue(latest).
		SetUpdatedAt(now).
		Exec(ctx)
	if err != nil {
		return "", false, err
	}
	if err = tx.Commit(); err != nil {
		return "", false, err
	}
	return previous, true, nil
}

func (r *settingRepository) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	if len(keys) == 0 {
		return map[string]string{}, nil
	}
	settings, err := r.client.Setting.Query().Where(setting.KeyIn(keys...)).All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

func (r *settingRepository) SetMultiple(ctx context.Context, settings map[string]string) error {
	if len(settings) == 0 {
		return nil
	}

	now := time.Now()
	builders := make([]*ent.SettingCreate, 0, len(settings))
	for key, value := range settings {
		builders = append(builders, r.client.Setting.Create().SetKey(key).SetValue(value).SetUpdatedAt(now))
	}
	return r.client.Setting.
		CreateBulk(builders...).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
}

func (r *settingRepository) GetAll(ctx context.Context) (map[string]string, error) {
	settings, err := r.client.Setting.Query().All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

func (r *settingRepository) Delete(ctx context.Context, key string) error {
	_, err := r.client.Setting.Delete().Where(setting.KeyEQ(key)).Exec(ctx)
	return err
}
