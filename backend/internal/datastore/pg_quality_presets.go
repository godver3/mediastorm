package datastore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"novastream/services/qualityprofiles"
)

// maxCachedSnapshots bounds the snapshot cache per source.
const maxCachedSnapshots = 20

type pgQualityPresetRepo struct{ pool DB }

// QualityPresets returns the quality preset store.
func (ds *DataStore) QualityPresets() qualityprofiles.Store {
	return &pgQualityPresetRepo{pool: ds.pool}
}

func (r *pgQualityPresetRepo) ListPresets(ctx context.Context) ([]*qualityprofiles.Preset, error) {
	rows, err := r.pool.Query(ctx, `SELECT data FROM quality_presets ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list quality presets: %w", err)
	}
	defer rows.Close()
	var out []*qualityprofiles.Preset
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var p qualityprofiles.Preset
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("decode quality preset: %w", err)
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

func (r *pgQualityPresetRepo) GetPreset(ctx context.Context, id string) (*qualityprofiles.Preset, error) {
	var data []byte
	err := r.pool.QueryRow(ctx, `SELECT data FROM quality_presets WHERE id=$1`, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get quality preset: %w", err)
	}
	var p qualityprofiles.Preset
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("decode quality preset: %w", err)
	}
	return &p, nil
}

func (r *pgQualityPresetRepo) SavePreset(ctx context.Context, p *qualityprofiles.Preset) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO quality_presets (id, app, name, data, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6)
 ON CONFLICT (id) DO UPDATE SET app=EXCLUDED.app, name=EXCLUDED.name, data=EXCLUDED.data, updated_at=EXCLUDED.updated_at`,
		p.ID, string(p.App), p.DisplayName(), data, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save quality preset: %w", err)
	}
	return nil
}

func (r *pgQualityPresetRepo) DeletePreset(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM quality_presets WHERE id=$1`, id)
	return err
}

func (r *pgQualityPresetRepo) GetSnapshot(ctx context.Context, sourceID, commit string) ([]byte, error) {
	var data []byte
	err := r.pool.QueryRow(ctx, `SELECT archive FROM quality_snapshot_cache WHERE source_id=$1 AND commit_id=$2`, sourceID, commit).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return data, err
}

func (r *pgQualityPresetRepo) PutSnapshot(ctx context.Context, sourceID, commit string, archive []byte) error {
	if _, err := r.pool.Exec(ctx, `INSERT INTO quality_snapshot_cache (source_id, commit_id, archive, fetched_at) VALUES ($1,$2,$3,NOW())
 ON CONFLICT (source_id, commit_id) DO UPDATE SET archive=EXCLUDED.archive, fetched_at=EXCLUDED.fetched_at`, sourceID, commit, archive); err != nil {
		return err
	}
	// Keep only the newest snapshots per source; presets pinned to an
	// evicted commit refetch it on demand.
	_, err := r.pool.Exec(ctx, `DELETE FROM quality_snapshot_cache WHERE source_id=$1 AND commit_id NOT IN (
 SELECT commit_id FROM quality_snapshot_cache WHERE source_id=$1 ORDER BY fetched_at DESC LIMIT $2)`, sourceID, maxCachedSnapshots)
	return err
}

func (r *pgQualityPresetRepo) GetTitlePreset(ctx context.Context, userID, clientID, contentID string) (string, error) {
	var id string
	err := r.pool.QueryRow(ctx, `SELECT preset_id FROM title_quality_presets WHERE user_id=$1 AND client_id=$2 AND content_id=$3`,
		userID, clientID, contentID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get title quality preset: %w", err)
	}
	return id, nil
}

func (r *pgQualityPresetRepo) SetTitlePreset(ctx context.Context, userID, clientID, contentID, presetID string) error {
	if presetID == "" {
		if _, err := r.pool.Exec(ctx, `DELETE FROM title_quality_presets WHERE user_id=$1 AND client_id=$2 AND content_id=$3`,
			userID, clientID, contentID); err != nil {
			return fmt.Errorf("forget title quality preset: %w", err)
		}
		return nil
	}
	if _, err := r.pool.Exec(ctx, `INSERT INTO title_quality_presets (user_id, client_id, content_id, preset_id, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (user_id, client_id, content_id) DO UPDATE SET preset_id=EXCLUDED.preset_id, updated_at=NOW()`,
		userID, clientID, contentID, presetID); err != nil {
		return fmt.Errorf("set title quality preset: %w", err)
	}
	return nil
}
