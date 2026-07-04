// Package store handles SQLite persistence for Almanac.
//
// MVP stage: it only manages a "visits" table used to validate that the
// pure-Go SQLite driver works after cross-compilation and that data survives
// across deployments.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// Store wraps the database handle.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at the given path, ensuring the
// parent directory exists and the schema is applied.
func Open(dbPath string) (*Store, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

// migrate applies the (tiny) MVP schema.
func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS visits (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    time TEXT NOT NULL
);`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// RecordVisit inserts one visit row and returns the total visit count.
func (s *Store) RecordVisit(now time.Time) (int64, error) {
	if _, err := s.db.Exec("INSERT INTO visits (time) VALUES (?)", now.Format(time.RFC3339)); err != nil {
		return 0, fmt.Errorf("insert visit: %w", err)
	}
	var count int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM visits").Scan(&count); err != nil {
		return 0, fmt.Errorf("count visits: %w", err)
	}
	return count, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}
