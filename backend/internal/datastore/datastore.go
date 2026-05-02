package datastore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier abstracts pgxpool.Pool and pgx.Tx so repositories work with both.
type Querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// querierExec wraps Querier to provide an Exec that returns command tag.
// pgxpool.Pool and pgx.Tx both satisfy this directly, but we need a common interface.

// DataStore is the single entry point for all data access.
// Services receive repository interfaces from here, never raw DB connections.
type DataStore struct {
	pool *pgxpool.Pool
}

// New connects to PostgreSQL, runs migrations, and returns a ready DataStore.
func New(ctx context.Context, databaseURL string, opts ...Option) (*DataStore, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	o := defaultOptions()
	for _, opt := range opts {
		opt(&o)
	}
	if o.maxConns > 0 {
		cfg.MaxConns = int32(o.maxConns)
	}
	if o.minConns > 0 {
		cfg.MinConns = int32(o.minConns)
	}
	if o.maxConnLifetime > 0 {
		cfg.MaxConnLifetime = o.maxConnLifetime
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	if err := runMigrations(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return &DataStore{pool: pool}, nil
}

// Close shuts down the connection pool.
func (ds *DataStore) Close() {
	ds.pool.Close()
}

// Pool exposes the raw pool for backup/migration operations only.
func (ds *DataStore) Pool() *pgxpool.Pool {
	return ds.pool
}

// --- Repository accessors ---

func (ds *DataStore) Accounts() AccountRepository       { return &pgAccountRepo{pool: ds.pool} }
func (ds *DataStore) Users() UserRepository             { return &pgUserRepo{pool: ds.pool} }
func (ds *DataStore) Sessions() SessionRepository       { return &pgSessionRepo{pool: ds.pool} }
func (ds *DataStore) Invitations() InvitationRepository { return &pgInvitationRepo{pool: ds.pool} }
func (ds *DataStore) Clients() ClientRepository         { return &pgClientRepo{pool: ds.pool} }
func (ds *DataStore) ClientSettings() ClientSettingsRepository {
	return &pgClientSettingsRepo{pool: ds.pool}
}
func (ds *DataStore) UserSettings() UserSettingsRepository { return &pgUserSettingsRepo{pool: ds.pool} }
func (ds *DataStore) Watchlist() WatchlistRepository       { return &pgWatchlistRepo{pool: ds.pool} }
func (ds *DataStore) CustomLists() CustomListRepository    { return &pgCustomListRepo{pool: ds.pool} }
func (ds *DataStore) WatchHistory() WatchHistoryRepository { return &pgWatchHistoryRepo{pool: ds.pool} }
func (ds *DataStore) PlaybackProgress() PlaybackProgressRepository {
	return &pgPlaybackProgressRepo{pool: ds.pool}
}
func (ds *DataStore) ContentPreferences() ContentPreferencesRepository {
	return &pgContentPrefsRepo{pool: ds.pool}
}
func (ds *DataStore) Prequeue() PrequeueRepository       { return &pgPrequeueRepo{pool: ds.pool} }
func (ds *DataStore) Prewarm() PrewarmRepository         { return &pgPrewarmRepo{pool: ds.pool} }
func (ds *DataStore) ImportQueue() ImportQueueRepository { return &pgImportQueueRepo{pool: ds.pool} }
func (ds *DataStore) FileHealth() FileHealthRepository   { return &pgFileHealthRepo{pool: ds.pool} }
func (ds *DataStore) MediaFiles() MediaFileRepository    { return &pgMediaFileRepo{pool: ds.pool} }
func (ds *DataStore) LocalMedia() LocalMediaRepository   { return &pgLocalMediaRepo{pool: ds.pool} }
func (ds *DataStore) Recordings() RecordingRepository    { return &pgRecordingRepo{pool: ds.pool} }

// --- Transaction support ---

// Tx wraps a pgx transaction and provides repository accessors
// that execute within that transaction.
type Tx struct {
	tx pgx.Tx
}

// WithTx runs fn inside a database transaction. If fn returns an error,
// the transaction is rolled back. Otherwise it is committed.
func (ds *DataStore) WithTx(ctx context.Context, fn func(tx *Tx) error) error {
	pgxTx, err := ds.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	t := &Tx{tx: pgxTx}
	if err := fn(t); err != nil {
		_ = pgxTx.Rollback(ctx)
		return err
	}
	return pgxTx.Commit(ctx)
}

func (t *Tx) Accounts() AccountRepository              { return &pgAccountRepo{pool: t.tx} }
func (t *Tx) Users() UserRepository                    { return &pgUserRepo{pool: t.tx} }
func (t *Tx) Sessions() SessionRepository              { return &pgSessionRepo{pool: t.tx} }
func (t *Tx) Invitations() InvitationRepository        { return &pgInvitationRepo{pool: t.tx} }
func (t *Tx) Clients() ClientRepository                { return &pgClientRepo{pool: t.tx} }
func (t *Tx) ClientSettings() ClientSettingsRepository { return &pgClientSettingsRepo{pool: t.tx} }
func (t *Tx) UserSettings() UserSettingsRepository     { return &pgUserSettingsRepo{pool: t.tx} }
func (t *Tx) Watchlist() WatchlistRepository           { return &pgWatchlistRepo{pool: t.tx} }
func (t *Tx) CustomLists() CustomListRepository        { return &pgCustomListRepo{pool: t.tx} }
func (t *Tx) WatchHistory() WatchHistoryRepository     { return &pgWatchHistoryRepo{pool: t.tx} }
func (t *Tx) PlaybackProgress() PlaybackProgressRepository {
	return &pgPlaybackProgressRepo{pool: t.tx}
}
func (t *Tx) ContentPreferences() ContentPreferencesRepository {
	return &pgContentPrefsRepo{pool: t.tx}
}
func (t *Tx) Prequeue() PrequeueRepository       { return &pgPrequeueRepo{pool: t.tx} }
func (t *Tx) Prewarm() PrewarmRepository         { return &pgPrewarmRepo{pool: t.tx} }
func (t *Tx) ImportQueue() ImportQueueRepository { return &pgImportQueueRepo{pool: t.tx} }
func (t *Tx) FileHealth() FileHealthRepository   { return &pgFileHealthRepo{pool: t.tx} }
func (t *Tx) MediaFiles() MediaFileRepository    { return &pgMediaFileRepo{pool: t.tx} }
func (t *Tx) LocalMedia() LocalMediaRepository   { return &pgLocalMediaRepo{pool: t.tx} }
func (t *Tx) Recordings() RecordingRepository    { return &pgRecordingRepo{pool: t.tx} }
