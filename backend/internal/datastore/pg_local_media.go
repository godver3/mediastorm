package datastore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgLocalMediaRepo struct {
	pool DB
}

func (r *pgLocalMediaRepo) ListLibraries(ctx context.Context) ([]models.LocalMediaLibrary, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, library_type, root_path, created_at, updated_at,
		       last_scan_started_at, last_scan_finished_at, last_scan_status, last_scan_error,
		       last_scan_discovered, last_scan_total, last_scan_matched, last_scan_low_confidence
		FROM local_media_libraries
		ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list local media libraries: %w", err)
	}
	defer rows.Close()

	var libraries []models.LocalMediaLibrary
	for rows.Next() {
		var library models.LocalMediaLibrary
		if err := rows.Scan(
			&library.ID, &library.Name, &library.Type, &library.RootPath, &library.CreatedAt, &library.UpdatedAt,
			&library.LastScanStartedAt, &library.LastScanFinishedAt, &library.LastScanStatus, &library.LastScanError,
			&library.LastScanDiscovered, &library.LastScanTotal, &library.LastScanMatched, &library.LastScanLowConf,
		); err != nil {
			return nil, fmt.Errorf("scan local media library: %w", err)
		}
		libraries = append(libraries, library)
	}
	return libraries, rows.Err()
}

func (r *pgLocalMediaRepo) GetLibrary(ctx context.Context, id string) (*models.LocalMediaLibrary, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, library_type, root_path, created_at, updated_at,
		       last_scan_started_at, last_scan_finished_at, last_scan_status, last_scan_error,
		       last_scan_discovered, last_scan_total, last_scan_matched, last_scan_low_confidence
		FROM local_media_libraries
		WHERE id = $1`, id)
	var library models.LocalMediaLibrary
	if err := row.Scan(
		&library.ID, &library.Name, &library.Type, &library.RootPath, &library.CreatedAt, &library.UpdatedAt,
		&library.LastScanStartedAt, &library.LastScanFinishedAt, &library.LastScanStatus, &library.LastScanError,
		&library.LastScanDiscovered, &library.LastScanTotal, &library.LastScanMatched, &library.LastScanLowConf,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get local media library: %w", err)
	}
	return &library, nil
}

func (r *pgLocalMediaRepo) CreateLibrary(ctx context.Context, library *models.LocalMediaLibrary) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO local_media_libraries (
			id, name, library_type, root_path, created_at, updated_at,
			last_scan_started_at, last_scan_finished_at, last_scan_status, last_scan_error,
			last_scan_discovered, last_scan_total, last_scan_matched, last_scan_low_confidence
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		library.ID, library.Name, library.Type, library.RootPath, library.CreatedAt, library.UpdatedAt,
		library.LastScanStartedAt, library.LastScanFinishedAt, library.LastScanStatus, library.LastScanError,
		library.LastScanDiscovered, library.LastScanTotal, library.LastScanMatched, library.LastScanLowConf,
	)
	if err != nil {
		return fmt.Errorf("create local media library: %w", err)
	}
	return nil
}

func (r *pgLocalMediaRepo) UpdateLibrary(ctx context.Context, library *models.LocalMediaLibrary) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE local_media_libraries
		SET name = $2, library_type = $3, root_path = $4, updated_at = $5,
		    last_scan_started_at = $6, last_scan_finished_at = $7, last_scan_status = $8,
		    last_scan_error = $9, last_scan_discovered = $10, last_scan_total = $11, last_scan_matched = $12,
		    last_scan_low_confidence = $13
		WHERE id = $1`,
		library.ID, library.Name, library.Type, library.RootPath, library.UpdatedAt,
		library.LastScanStartedAt, library.LastScanFinishedAt, library.LastScanStatus,
		library.LastScanError, library.LastScanDiscovered, library.LastScanTotal, library.LastScanMatched, library.LastScanLowConf,
	)
	if err != nil {
		return fmt.Errorf("update local media library: %w", err)
	}
	return nil
}

func (r *pgLocalMediaRepo) DeleteLibrary(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM local_media_libraries WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete local media library: %w", err)
	}
	return nil
}

func (r *pgLocalMediaRepo) ListItemsByLibrary(ctx context.Context, libraryID string, query models.LocalMediaItemListQuery) (*models.LocalMediaItemListResult, error) {
	filter := strings.TrimSpace(query.Filter)
	search := strings.TrimSpace(query.Query)
	sortBy := normalizeLocalMediaSort(query.Sort)
	sortDir := normalizeSortDir(query.Dir)
	limit := query.Limit
	offset := query.Offset
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	args := []interface{}{libraryID}
	whereParts := []string{"library_id = $1"}
	if filter != "" && filter != "all" {
		args = append(args, filter)
		whereParts = append(whereParts, fmt.Sprintf("match_status = $%d", len(args)))
	}
	if search != "" {
		args = append(args, "%"+search+"%")
		searchArg := len(args)
		whereParts = append(whereParts, fmt.Sprintf("(relative_path ILIKE $%d OR file_name ILIKE $%d OR detected_title ILIKE $%d OR matched_name ILIKE $%d)", searchArg, searchArg, searchArg, searchArg))
	}

	whereSQL := strings.Join(whereParts, " AND ")
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM local_media_items WHERE %s", whereSQL)
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count local media items: %w", err)
	}

	args = append(args, limit, offset)
	rows, err := r.pool.Query(ctx, fmt.Sprintf(`
		SELECT id, library_id, relative_path, file_path, file_name, library_type, detected_title,
		       detected_year, season_number, episode_number, confidence, match_status,
		       matched_title_id, matched_media_type, matched_name, matched_year, is_missing, missing_since,
		       metadata, probe, size_bytes, modified_at, last_scanned_at, last_seen_scan_id, created_at, updated_at
		FROM local_media_items
		WHERE %s
		ORDER BY %s %s, relative_path ASC
		LIMIT $%d OFFSET $%d`, whereSQL, sortBy, sortDir, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("list local media items: %w", err)
	}
	defer rows.Close()

	var items []models.LocalMediaItem
	for rows.Next() {
		item, err := scanLocalMediaItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &models.LocalMediaItemListResult{
		Items:  items,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	}, nil
}

func (r *pgLocalMediaRepo) ListAllItemsByLibrary(ctx context.Context, libraryID string) ([]models.LocalMediaItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, library_id, relative_path, file_path, file_name, library_type, detected_title,
		       detected_year, season_number, episode_number, confidence, match_status,
		       matched_title_id, matched_media_type, matched_name, matched_year, is_missing, missing_since,
		       metadata, probe, size_bytes, modified_at, last_scanned_at, last_seen_scan_id, created_at, updated_at
		FROM local_media_items
		WHERE library_id = $1`, libraryID)
	if err != nil {
		return nil, fmt.Errorf("list all local media items: %w", err)
	}
	defer rows.Close()

	var items []models.LocalMediaItem
	for rows.Next() {
		item, err := scanLocalMediaItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func normalizeLocalMediaSort(sortBy string) string {
	switch strings.TrimSpace(sortBy) {
	case "name":
		return "COALESCE(NULLIF(matched_name, ''), NULLIF(detected_title, ''), file_name)"
	case "confidence":
		return "confidence"
	case "year":
		return "COALESCE(NULLIF(matched_year, 0), NULLIF(detected_year, 0), 0)"
	case "size":
		return "size_bytes"
	case "modified":
		return "modified_at"
	case "status":
		return "match_status"
	default:
		return "updated_at"
	}
}

func normalizeSortDir(dir string) string {
	if strings.EqualFold(strings.TrimSpace(dir), "asc") {
		return "ASC"
	}
	return "DESC"
}

func (r *pgLocalMediaRepo) GetItem(ctx context.Context, id string) (*models.LocalMediaItem, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, library_id, relative_path, file_path, file_name, library_type, detected_title,
		       detected_year, season_number, episode_number, confidence, match_status,
		       matched_title_id, matched_media_type, matched_name, matched_year, is_missing, missing_since,
		       metadata, probe, size_bytes, modified_at, last_scanned_at, last_seen_scan_id, created_at, updated_at
		FROM local_media_items
		WHERE id = $1`, id)
	item, err := scanLocalMediaItem(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return item, err
}

func (r *pgLocalMediaRepo) UpsertItem(ctx context.Context, item *models.LocalMediaItem) error {
	metadataJSON, _ := json.Marshal(item.Metadata)
	probeJSON, _ := json.Marshal(item.Probe)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO local_media_items (
			id, library_id, relative_path, file_path, file_name, library_type, detected_title,
			detected_year, season_number, episode_number, confidence, match_status,
			matched_title_id, matched_media_type, matched_name, matched_year, is_missing, missing_since,
			metadata, probe, size_bytes, modified_at, last_scanned_at, last_seen_scan_id, created_at, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26
		)
		ON CONFLICT (library_id, relative_path) DO UPDATE SET
			file_path = EXCLUDED.file_path,
			file_name = EXCLUDED.file_name,
			library_type = EXCLUDED.library_type,
			detected_title = EXCLUDED.detected_title,
			detected_year = EXCLUDED.detected_year,
			season_number = EXCLUDED.season_number,
			episode_number = EXCLUDED.episode_number,
			confidence = EXCLUDED.confidence,
			match_status = EXCLUDED.match_status,
			matched_title_id = EXCLUDED.matched_title_id,
			matched_media_type = EXCLUDED.matched_media_type,
			matched_name = EXCLUDED.matched_name,
			matched_year = EXCLUDED.matched_year,
			is_missing = EXCLUDED.is_missing,
			missing_since = EXCLUDED.missing_since,
			metadata = EXCLUDED.metadata,
			probe = EXCLUDED.probe,
			size_bytes = EXCLUDED.size_bytes,
			modified_at = EXCLUDED.modified_at,
			last_scanned_at = EXCLUDED.last_scanned_at,
			last_seen_scan_id = EXCLUDED.last_seen_scan_id,
			updated_at = EXCLUDED.updated_at`,
		item.ID, item.LibraryID, item.RelativePath, item.FilePath, item.FileName, item.LibraryType, item.DetectedTitle,
		item.DetectedYear, item.SeasonNumber, item.EpisodeNumber, item.Confidence, item.MatchStatus,
		item.MatchedTitleID, item.MatchedMediaType, item.MatchedName, item.MatchedYear, item.IsMissing, item.MissingSince,
		metadataJSON, probeJSON, item.SizeBytes, item.ModifiedAt, item.LastScannedAt, item.LastSeenScanID, item.CreatedAt, item.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert local media item: %w", err)
	}
	return nil
}

func (r *pgLocalMediaRepo) MarkItemsMissingNotSeenInScan(ctx context.Context, libraryID, scanID string, missingSince interface{}) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE local_media_items
		SET is_missing = TRUE,
		    missing_since = COALESCE(missing_since, $3),
		    updated_at = $3
		WHERE library_id = $1 AND last_seen_scan_id <> $2 AND is_missing = FALSE`, libraryID, scanID, missingSince)
	if err != nil {
		return fmt.Errorf("mark local media items missing: %w", err)
	}
	return nil
}

func (r *pgLocalMediaRepo) DeleteItem(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM local_media_items WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete local media item: %w", err)
	}
	return nil
}

func scanLocalMediaItem(row pgx.Row) (*models.LocalMediaItem, error) {
	var item models.LocalMediaItem
	var metadataJSON, probeJSON []byte
	if err := row.Scan(
		&item.ID, &item.LibraryID, &item.RelativePath, &item.FilePath, &item.FileName, &item.LibraryType, &item.DetectedTitle,
		&item.DetectedYear, &item.SeasonNumber, &item.EpisodeNumber, &item.Confidence, &item.MatchStatus,
		&item.MatchedTitleID, &item.MatchedMediaType, &item.MatchedName, &item.MatchedYear, &item.IsMissing, &item.MissingSince,
		&metadataJSON, &probeJSON, &item.SizeBytes, &item.ModifiedAt, &item.LastScannedAt, &item.LastSeenScanID, &item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if len(metadataJSON) > 0 && string(metadataJSON) != "null" {
		var metadata models.Title
		if err := json.Unmarshal(metadataJSON, &metadata); err == nil {
			item.Metadata = &metadata
		}
	}
	if len(probeJSON) > 0 && string(probeJSON) != "null" {
		var probe models.LocalMediaProbe
		if err := json.Unmarshal(probeJSON, &probe); err == nil {
			item.Probe = &probe
		}
	}
	return &item, nil
}
