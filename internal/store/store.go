package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Endpoint struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"createdAt"`
}

type Event struct {
	ID         int64               `json:"id"`
	EndpointID int64               `json:"endpointId"`
	Method     string              `json:"method"`
	Path       string              `json:"path"`
	Query      string              `json:"query"`
	Headers    map[string][]string `json:"headers"`
	Body       string              `json:"body"`
	RemoteAddr string              `json:"remoteAddr"`
	CreatedAt  time.Time           `json:"createdAt"`
}

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		PRAGMA foreign_keys = ON;
		PRAGMA journal_mode = WAL;
		CREATE TABLE IF NOT EXISTS endpoints (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			token TEXT NOT NULL UNIQUE,
			created_at DATETIME NOT NULL
		);
		CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			endpoint_id INTEGER NOT NULL REFERENCES endpoints(id) ON DELETE CASCADE,
			method TEXT NOT NULL,
			path TEXT NOT NULL,
			query_string TEXT NOT NULL,
			headers_json TEXT NOT NULL,
			body TEXT NOT NULL,
			remote_addr TEXT NOT NULL,
			created_at DATETIME NOT NULL
		);
		CREATE INDEX IF NOT EXISTS events_endpoint_created
		ON events(endpoint_id, created_at DESC);
	`)
	return err
}

func (s *Store) CreateEndpoint(ctx context.Context, name, token string) (Endpoint, error) {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO endpoints(name, token, created_at) VALUES (?, ?, ?)`, name, token, now)
	if err != nil {
		return Endpoint{}, err
	}
	id, _ := result.LastInsertId()
	return Endpoint{ID: id, Name: name, Token: token, CreatedAt: now}, nil
}

func (s *Store) ListEndpoints(ctx context.Context) ([]Endpoint, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, token, created_at FROM endpoints ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Endpoint
	for rows.Next() {
		var e Endpoint
		if err := rows.Scan(&e.ID, &e.Name, &e.Token, &e.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func (s *Store) EndpointByToken(ctx context.Context, token string) (Endpoint, error) {
	var e Endpoint
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, token, created_at FROM endpoints WHERE token = ?`, token).
		Scan(&e.ID, &e.Name, &e.Token, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Endpoint{}, ErrNotFound
	}
	return e, err
}

func (s *Store) SaveEvent(ctx context.Context, event Event) (Event, error) {
	headers, err := json.Marshal(event.Headers)
	if err != nil {
		return Event{}, err
	}
	event.CreatedAt = time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO events(endpoint_id, method, path, query_string, headers_json, body, remote_addr, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EndpointID, event.Method, event.Path, event.Query, headers, event.Body, event.RemoteAddr, event.CreatedAt)
	if err != nil {
		return Event{}, err
	}
	event.ID, _ = result.LastInsertId()
	return event, nil
}

func (s *Store) ListEvents(ctx context.Context, endpointID int64, limit int) ([]Event, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, endpoint_id, method, path, query_string, headers_json, body, remote_addr, created_at
		FROM events WHERE endpoint_id = ? ORDER BY created_at DESC LIMIT ?`, endpointID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Event
	for rows.Next() {
		var e Event
		var headers []byte
		if err := rows.Scan(&e.ID, &e.EndpointID, &e.Method, &e.Path, &e.Query,
			&headers, &e.Body, &e.RemoteAddr, &e.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(headers, &e.Headers); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func (s *Store) Event(ctx context.Context, id int64) (Event, error) {
	var e Event
	var headers []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT id, endpoint_id, method, path, query_string, headers_json, body, remote_addr, created_at
		FROM events WHERE id = ?`, id).
		Scan(&e.ID, &e.EndpointID, &e.Method, &e.Path, &e.Query, &headers, &e.Body, &e.RemoteAddr, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, ErrNotFound
	}
	if err != nil {
		return Event{}, err
	}
	err = json.Unmarshal(headers, &e.Headers)
	return e, err
}
