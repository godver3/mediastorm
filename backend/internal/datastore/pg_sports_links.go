package datastore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"novastream/models"
)

type pgSportsLinksRepo struct {
	pool DB
}

func (r *pgSportsLinksRepo) UpsertTeam(ctx context.Context, team models.SportsTeamRecord) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO sports_teams (id, league, espn_team_id, name, location, nickname, abbreviation, logo_url, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (id) DO UPDATE SET
			name = $4, location = $5, nickname = $6, abbreviation = $7, logo_url = $8, updated_at = now()
		WHERE (sports_teams.name, sports_teams.location, sports_teams.nickname, sports_teams.abbreviation, sports_teams.logo_url)
			IS DISTINCT FROM (EXCLUDED.name, EXCLUDED.location, EXCLUDED.nickname, EXCLUDED.abbreviation, EXCLUDED.logo_url)`,
		team.ID, team.League, team.EspnTeamID, team.Name, team.Location, team.Nickname, team.Abbreviation, team.LogoURL)
	if err != nil {
		return fmt.Errorf("upsert sports team: %w", err)
	}
	return nil
}

func (r *pgSportsLinksRepo) ListTeamsByLeague(ctx context.Context, league string) ([]models.SportsTeamRecord, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, league, espn_team_id, name, location, nickname, abbreviation, logo_url, updated_at
		FROM sports_teams WHERE league = $1 ORDER BY name`, league)
	if err != nil {
		return nil, fmt.Errorf("list sports teams: %w", err)
	}
	defer rows.Close()

	var teams []models.SportsTeamRecord
	for rows.Next() {
		var t models.SportsTeamRecord
		if err := rows.Scan(&t.ID, &t.League, &t.EspnTeamID, &t.Name, &t.Location, &t.Nickname, &t.Abbreviation, &t.LogoURL, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan sports team: %w", err)
		}
		teams = append(teams, t)
	}
	return teams, rows.Err()
}

func (r *pgSportsLinksRepo) GetTeam(ctx context.Context, teamID string) (*models.SportsTeamRecord, error) {
	var t models.SportsTeamRecord
	err := r.pool.QueryRow(ctx, `
		SELECT id, league, espn_team_id, name, location, nickname, abbreviation, logo_url, updated_at
		FROM sports_teams WHERE id = $1`, teamID).
		Scan(&t.ID, &t.League, &t.EspnTeamID, &t.Name, &t.Location, &t.Nickname, &t.Abbreviation, &t.LogoURL, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get sports team: %w", err)
	}
	return &t, nil
}

const sportsLinkColumns = `id, team_id, slot, position, channel_tvg_id, channel_name, channel_url, channel_logo, source_id, source_name, auto_linked, link_confidence, match_reason, last_verified_at, created_at, updated_at`

func scanSportsLink(row pgx.Row) (models.SportsTeamChannelLink, error) {
	var l models.SportsTeamChannelLink
	err := row.Scan(&l.ID, &l.TeamID, &l.Slot, &l.Position, &l.ChannelTvgID, &l.ChannelName, &l.ChannelURL,
		&l.ChannelLogo, &l.SourceID, &l.SourceName, &l.AutoLinked, &l.LinkConfidence, &l.MatchReason,
		&l.LastVerifiedAt, &l.CreatedAt, &l.UpdatedAt)
	return l, err
}

func (r *pgSportsLinksRepo) GetLinksForTeam(ctx context.Context, teamID string) ([]models.SportsTeamChannelLink, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+sportsLinkColumns+`
		FROM sports_team_channel_links
		WHERE team_id = $1
		ORDER BY slot DESC, position ASC`, teamID) // slot DESC: "primary" > "backup" alphabetically
	if err != nil {
		return nil, fmt.Errorf("get team channel links: %w", err)
	}
	defer rows.Close()

	var links []models.SportsTeamChannelLink
	for rows.Next() {
		l, err := scanSportsLink(rows)
		if err != nil {
			return nil, fmt.Errorf("scan team channel link: %w", err)
		}
		links = append(links, l)
	}
	return links, rows.Err()
}

func (r *pgSportsLinksRepo) GetLinksForTeams(ctx context.Context, teamIDs []string) (map[string][]models.SportsTeamChannelLink, error) {
	result := make(map[string][]models.SportsTeamChannelLink)
	if len(teamIDs) == 0 {
		return result, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+sportsLinkColumns+`
		FROM sports_team_channel_links
		WHERE team_id = ANY($1)
		ORDER BY team_id, slot DESC, position ASC`, teamIDs)
	if err != nil {
		return nil, fmt.Errorf("get team channel links batch: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		l, err := scanSportsLink(rows)
		if err != nil {
			return nil, fmt.Errorf("scan team channel link: %w", err)
		}
		result[l.TeamID] = append(result[l.TeamID], l)
	}
	return result, rows.Err()
}

func (r *pgSportsLinksRepo) SetPrimaryLink(ctx context.Context, teamID string, link models.SportsTeamChannelLink) (*models.SportsTeamChannelLink, error) {
	if link.ID == "" {
		link.ID = uuid.NewString()
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO sports_team_channel_links
			(id, team_id, slot, position, channel_tvg_id, channel_name, channel_url, channel_logo, source_id, source_name, auto_linked, link_confidence, match_reason, created_at, updated_at)
		VALUES ($1, $2, 'primary', 0, $3, $4, $5, $6, $7, $8, false, 0, '', now(), now())
		ON CONFLICT (team_id, slot, position) DO UPDATE SET
			channel_tvg_id = $3, channel_name = $4, channel_url = $5, channel_logo = $6,
			source_id = $7, source_name = $8, auto_linked = false, link_confidence = 0,
			match_reason = '', updated_at = now()
		RETURNING `+sportsLinkColumns,
		link.ID, teamID, link.ChannelTvgID, link.ChannelName, link.ChannelURL, link.ChannelLogo, link.SourceID, link.SourceName)
	saved, err := scanSportsLink(row)
	if err != nil {
		return nil, fmt.Errorf("set primary channel link: %w", err)
	}
	return &saved, nil
}

func (r *pgSportsLinksRepo) InsertAutoPrimaryLinks(ctx context.Context, links []models.SportsTeamChannelLink) ([]models.SportsTeamChannelLink, error) {
	if len(links) == 0 {
		return []models.SportsTeamChannelLink{}, nil
	}
	values := make([]string, 0, len(links))
	args := make([]any, 0, len(links)*10)
	for i := range links {
		link := &links[i]
		if link.ID == "" {
			link.ID = uuid.NewString()
		}
		base := i*10 + 1
		values = append(values, fmt.Sprintf("($%d,$%d,'primary',0,$%d,$%d,$%d,$%d,$%d,$%d,true,$%d,$%d,now(),now())",
			base, base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9))
		args = append(args, link.ID, link.TeamID, link.ChannelTvgID, link.ChannelName, link.ChannelURL,
			link.ChannelLogo, link.SourceID, link.SourceName, link.LinkConfidence, link.MatchReason)
	}
	query := `INSERT INTO sports_team_channel_links
		(id, team_id, slot, position, channel_tvg_id, channel_name, channel_url, channel_logo, source_id, source_name, auto_linked, link_confidence, match_reason, created_at, updated_at)
		VALUES ` + strings.Join(values, ",") + `
		ON CONFLICT (team_id, slot, position) DO NOTHING
		RETURNING ` + sportsLinkColumns
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("insert auto primary links: %w", err)
	}
	defer rows.Close()
	saved := make([]models.SportsTeamChannelLink, 0, len(links))
	for rows.Next() {
		link, scanErr := scanSportsLink(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan auto primary link: %w", scanErr)
		}
		saved = append(saved, link)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read auto primary links: %w", err)
	}
	return saved, nil
}

func (r *pgSportsLinksRepo) AddBackupLink(ctx context.Context, teamID string, link models.SportsTeamChannelLink) (*models.SportsTeamChannelLink, error) {
	if link.ID == "" {
		link.ID = uuid.NewString()
	}
	row := r.pool.QueryRow(ctx, `
		WITH next_pos AS (
			SELECT COALESCE(MAX(position), 0) + 1 AS pos
			FROM sports_team_channel_links
			WHERE team_id = $2 AND slot = 'backup'
		)
		INSERT INTO sports_team_channel_links
			(id, team_id, slot, position, channel_tvg_id, channel_name, channel_url, channel_logo, source_id, source_name, created_at, updated_at)
		SELECT $1, $2, 'backup', next_pos.pos, $3, $4, $5, $6, $7, $8, now(), now()
		FROM next_pos
		RETURNING `+sportsLinkColumns,
		link.ID, teamID, link.ChannelTvgID, link.ChannelName, link.ChannelURL, link.ChannelLogo, link.SourceID, link.SourceName)
	saved, err := scanSportsLink(row)
	if err != nil {
		return nil, fmt.Errorf("add backup channel link: %w", err)
	}
	return &saved, nil
}

func (r *pgSportsLinksRepo) DeleteLink(ctx context.Context, teamID, linkID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM sports_team_channel_links WHERE id = $1 AND team_id = $2`, linkID, teamID)
	if err != nil {
		return fmt.Errorf("delete channel link: %w", err)
	}
	return nil
}

// reorderPositionOffset is added to every backup's position before the final set-based
// update, so intermediate values can never collide with either the old or new position
// ordering (both live in [0, reorderPositionOffset)).
const reorderPositionOffset = 1_000_000

// ReorderBackups rewrites backup positions to 1..len(orderedLinkIDs) in the given order.
// Postgres checks the UNIQUE(team_id, slot, position) constraint per row as an UPDATE
// processes them, even within one statement - a straight "set final positions" statement
// can still transiently collide mid-statement on a swap (e.g. position 1 -> 2 lands on a
// row still at 2). So this runs in two passes: first bump every backup's position out of
// the live range, then set final positions - two non-transactional statements (this
// repository doesn't have transaction access), acceptable for this low-concurrency admin
// action, but not atomic against a concurrent reorder of the same team.
func (r *pgSportsLinksRepo) ReorderBackups(ctx context.Context, teamID string, orderedLinkIDs []string) error {
	if len(orderedLinkIDs) == 0 {
		return nil
	}
	if _, err := r.pool.Exec(ctx, `
		UPDATE sports_team_channel_links
		SET position = position + $1
		WHERE team_id = $2 AND slot = 'backup'`,
		reorderPositionOffset, teamID); err != nil {
		return fmt.Errorf("reorder backup channel links (offset pass): %w", err)
	}

	positions := make([]int32, len(orderedLinkIDs))
	for i := range orderedLinkIDs {
		positions[i] = int32(i + 1)
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE sports_team_channel_links AS l
		SET position = v.pos, updated_at = now()
		FROM (SELECT unnest($1::text[]) AS link_id, unnest($2::int[]) AS pos) AS v
		WHERE l.id = v.link_id AND l.team_id = $3 AND l.slot = 'backup'`,
		orderedLinkIDs, positions, teamID)
	if err != nil {
		return fmt.Errorf("reorder backup channel links (final pass): %w", err)
	}
	return nil
}

func (r *pgSportsLinksRepo) ListAllTeams(ctx context.Context) ([]models.SportsTeamRecord, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, league, espn_team_id, name, location, nickname, abbreviation, logo_url, updated_at
		FROM sports_teams ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list all sports teams: %w", err)
	}
	defer rows.Close()
	teams := []models.SportsTeamRecord{}
	for rows.Next() {
		var t models.SportsTeamRecord
		if err := rows.Scan(&t.ID, &t.League, &t.EspnTeamID, &t.Name, &t.Location, &t.Nickname, &t.Abbreviation, &t.LogoURL, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan sports team: %w", err)
		}
		teams = append(teams, t)
	}
	return teams, rows.Err()
}

func (r *pgSportsLinksRepo) ListAllLinks(ctx context.Context) ([]models.SportsTeamChannelLink, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+sportsLinkColumns+`
		FROM sports_team_channel_links
		ORDER BY team_id, slot DESC, position ASC`)
	if err != nil {
		return nil, fmt.Errorf("list all team channel links: %w", err)
	}
	defer rows.Close()
	links := []models.SportsTeamChannelLink{}
	for rows.Next() {
		l, err := scanSportsLink(rows)
		if err != nil {
			return nil, fmt.Errorf("scan team channel link: %w", err)
		}
		links = append(links, l)
	}
	return links, rows.Err()
}

func (r *pgSportsLinksRepo) RestoreLink(ctx context.Context, link models.SportsTeamChannelLink) error {
	if link.ID == "" {
		link.ID = uuid.NewString()
	}
	if link.CreatedAt.IsZero() {
		link.CreatedAt = time.Now().UTC()
	}
	if link.UpdatedAt.IsZero() {
		link.UpdatedAt = link.CreatedAt
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO sports_team_channel_links (`+sportsLinkColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		link.ID, link.TeamID, string(link.Slot), link.Position, link.ChannelTvgID, link.ChannelName, link.ChannelURL,
		link.ChannelLogo, link.SourceID, link.SourceName, link.AutoLinked, link.LinkConfidence, link.MatchReason,
		link.LastVerifiedAt, link.CreatedAt, link.UpdatedAt)
	if err != nil {
		return fmt.Errorf("restore channel link: %w", err)
	}
	return nil
}

func (r *pgSportsLinksRepo) DeleteAllTeams(ctx context.Context) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM sports_teams`); err != nil {
		return fmt.Errorf("delete all sports teams: %w", err)
	}
	return nil
}
