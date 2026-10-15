package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound    = errors.New("not found")
	ErrRateLimited = errors.New("rate limited")
)

type Confession struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Category  string    `json:"category"`
	CreatedAt time.Time `json:"createdAt"`
	Views     int       `json:"views"`
}

type confessionStore interface {
	List(context.Context, string, string, int) ([]Confession, error)
	Create(context.Context, string, string, string, time.Time) (Confession, time.Duration, error)
	RecordView(context.Context, string, string, time.Time) (int, error)
	Stats(context.Context) (map[string]int, error)
}

type Store struct {
	pool              *pgxpool.Pool
	submitMinInterval time.Duration
	submitDailyLimit  int
}

func NewPostgresStore(ctx context.Context, databaseURL string, minInterval time.Duration, dailyLimit int) (*Store, error) {
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	poolConfig.MaxConns = 10
	poolConfig.MinConns = 1
	poolConfig.MaxConnLifetime = 30 * time.Minute
	poolConfig.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	return &Store{pool: pool, submitMinInterval: minInterval, submitDailyLimit: dailyLimit}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS confessions (
			id TEXT PRIMARY KEY,
			text TEXT NOT NULL CHECK (char_length(text) BETWEEN 20 AND 2000),
			category TEXT NOT NULL CHECK (category IN ('поступок', 'отношения', 'тайна', 'мысли', 'другое')),
			created_at TIMESTAMPTZ NOT NULL,
			author_ip_hash TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS confessions_created_at_idx ON confessions (created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS confessions_author_ip_idx ON confessions (author_ip_hash, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS confession_views (
			confession_id TEXT NOT NULL REFERENCES confessions(id) ON DELETE CASCADE,
			viewer_ip_hash TEXT NOT NULL,
			viewed_at TIMESTAMPTZ NOT NULL,
			PRIMARY KEY (confession_id, viewer_ip_hash)
		)`,
		`CREATE INDEX IF NOT EXISTS confession_views_confession_idx ON confession_views (confession_id)`,
	}
	for _, statement := range statements {
		if _, err := s.pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("apply database migration: %w", err)
		}
	}
	return nil
}

func (s *Store) Seed(ctx context.Context) error {
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM confessions`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	now := time.Now().UTC()
	seeds := []struct {
		text     string
		category string
		created  time.Time
		views    int
	}{
		{"Я часто делаю вид, что мне всё равно, хотя на самом деле очень боюсь потерять близких людей. Проще казаться холодным, чем однажды услышать, что меня больше не выбирают.", "мысли", now.Add(-2*time.Hour - 14*time.Minute), 128},
		{"Несколько лет назад я присвоил себе идею коллеги и получил повышение. Никто так и не узнал. Карьера пошла вверх, а чувство стыда осталось на том же месте.", "поступок", now.Add(-19 * time.Hour), 86},
		{"Я храню старые голосовые сообщения человека, с которым мы давно не общаемся. Иногда включаю их, когда дома совсем тихо, хотя всем говорю, что уже отпустил.", "тайна", now.Add(-49 * time.Hour), 204},
	}
	for _, seed := range seeds {
		id := newID()
		if _, err := tx.Exec(ctx, `INSERT INTO confessions (id, text, category, created_at, author_ip_hash) VALUES ($1, $2, $3, $4, $5)`, id, seed.text, seed.category, seed.created, "seed-author-"+id); err != nil {
			return err
		}
		for i := 0; i < seed.views; i++ {
			if _, err := tx.Exec(ctx, `INSERT INTO confession_views (confession_id, viewer_ip_hash, viewed_at) VALUES ($1, $2, $3)`, id, fmt.Sprintf("seed-viewer-%s-%d", id, i), seed.created); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) List(ctx context.Context, order, category string, limit int) ([]Confession, error) {
	if category == "все" {
		category = ""
	}
	orderBy := "c.created_at DESC"
	if order == "popular" {
		orderBy = "COUNT(v.viewer_ip_hash) DESC, c.created_at DESC"
	}
	query := `SELECT c.id, c.text, c.category, c.created_at, COUNT(v.viewer_ip_hash)
		FROM confessions c
		LEFT JOIN confession_views v ON v.confession_id = c.id
		WHERE ($1 = '' OR c.category = $1)
		GROUP BY c.id
		ORDER BY ` + orderBy + `
		LIMIT $2`
	rows, err := s.pool.Query(ctx, query, category, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]Confession, 0)
	for rows.Next() {
		var item Confession
		var views int64
		if err := rows.Scan(&item.ID, &item.Text, &item.Category, &item.CreatedAt, &views); err != nil {
			return nil, err
		}
		item.Views = int(views)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) Create(ctx context.Context, text, category, ipHash string, now time.Time) (Confession, time.Duration, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Confession{}, 0, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, ipHash); err != nil {
		return Confession{}, 0, err
	}
	var dailyCount int
	var newest, oldest *time.Time
	if err := tx.QueryRow(ctx, `SELECT COUNT(*), MAX(created_at), MIN(created_at)
		FROM confessions
		WHERE author_ip_hash = $1
		  AND created_at > $2::timestamptz - INTERVAL '24 hours'`, ipHash, now).Scan(&dailyCount, &newest, &oldest); err != nil {
		return Confession{}, 0, err
	}
	if newest != nil {
		age := now.Sub(*newest)
		if age >= 0 && age < s.submitMinInterval {
			return Confession{}, s.submitMinInterval - age, ErrRateLimited
		}
	}
	if dailyCount >= s.submitDailyLimit {
		retry := 24 * time.Hour
		if oldest != nil && oldest.Add(24*time.Hour).After(now) {
			retry = oldest.Add(24 * time.Hour).Sub(now)
		}
		return Confession{}, retry, ErrRateLimited
	}

	item := Confession{ID: newID(), Text: text, Category: category, CreatedAt: now}
	if _, err := tx.Exec(ctx, `INSERT INTO confessions (id, text, category, created_at, author_ip_hash) VALUES ($1, $2, $3, $4, $5)`, item.ID, item.Text, item.Category, item.CreatedAt, ipHash); err != nil {
		return Confession{}, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Confession{}, 0, err
	}
	return item, 0, nil
}

func (s *Store) RecordView(ctx context.Context, id, ipHash string, now time.Time) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM confessions WHERE id = $1)`, id).Scan(&exists); err != nil {
		return 0, err
	}
	if !exists {
		return 0, ErrNotFound
	}
	if _, err := tx.Exec(ctx, `INSERT INTO confession_views (confession_id, viewer_ip_hash, viewed_at) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, id, ipHash, now); err != nil {
		return 0, err
	}
	var views int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM confession_views WHERE confession_id = $1`, id).Scan(&views); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return views, nil
}

func (s *Store) Stats(ctx context.Context) (map[string]int, error) {
	var confessions, views int
	if err := s.pool.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM confessions),
		(SELECT COUNT(*) FROM confession_views)`).Scan(&confessions, &views); err != nil {
		return nil, err
	}
	return map[string]int{"confessions": confessions, "views": views}, nil
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
