package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordVisitPersists(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// First session: open, record two visits.
	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	now := time.Now()
	if c, err := s1.RecordVisit(now); err != nil || c != 1 {
		t.Fatalf("first visit: count=%d err=%v", c, err)
	}
	if c, err := s1.RecordVisit(now); err != nil || c != 2 {
		t.Fatalf("second visit: count=%d err=%v", c, err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Second session: reopen the same file, the count must continue from 2.
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer s2.Close()
	if c, err := s2.RecordVisit(now); err != nil || c != 3 {
		t.Fatalf("after reopen: expected count=3, got count=%d err=%v", c, err)
	}
}

// TestBackfillDefaultLedger simulates a user created before the ledgers table
// existed (no default ledger) and verifies that reopening the store repairs
// it, so the webhook path can resolve a default ledger.
func TestBackfillDefaultLedger(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// Insert a user directly WITHOUT a default ledger (mimics old schema).
	now := time.Now().Format(time.RFC3339)
	if _, err := s1.db.Exec(
		"INSERT INTO users (username, password_hash, webhook_token, created_at) VALUES ('legacy', 'x', 'legacytoken', ?)",
		now,
	); err != nil {
		t.Fatalf("insert legacy user: %v", err)
	}
	u, err := s1.UserByUsername("legacy")
	if err != nil {
		t.Fatalf("lookup legacy user: %v", err)
	}
	if _, err := s1.DefaultLedgerID(u.ID); err == nil {
		t.Fatal("expected no default ledger for legacy user before backfill")
	}
	s1.Close()

	// Reopen: backfill should run and create the missing default ledger.
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer s2.Close()
	if _, err := s2.DefaultLedgerID(u.ID); err != nil {
		t.Fatalf("expected default ledger after backfill, got: %v", err)
	}
}

// TestMigrateIsAdmin simulates a database created before the account-management
// feature (users table WITHOUT the is_admin column). Reopening the store must
// add the column via ALTER TABLE and flag the legacy 'admin' account as admin
// while leaving other users as non-admins. Idempotent on a second reopen.
func TestMigrateIsAdmin(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")

	// Build a legacy users table WITHOUT is_admin, using the raw driver so the
	// current schema (which already includes is_admin) does not run.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		webhook_token TEXT UNIQUE NOT NULL,
		created_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy users table: %v", err)
	}
	now := time.Now().Format(time.RFC3339)
	if _, err := raw.Exec(
		"INSERT INTO users (username, password_hash, webhook_token, created_at) VALUES ('admin','x','admintok',?),('alice','y','alicetok',?)",
		now, now,
	); err != nil {
		t.Fatalf("seed legacy users: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	// Reopen through Open() -> migrate() -> migrateIsAdmin() adds the column.
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen after legacy schema: %v", err)
	}
	defer s.Close()

	admin, err := s.UserByUsername("admin")
	if err != nil {
		t.Fatalf("lookup admin: %v", err)
	}
	if !admin.IsAdmin {
		t.Fatal("expected legacy 'admin' user to be flagged is_admin=1 after migration")
	}
	alice, err := s.UserByUsername("alice")
	if err != nil {
		t.Fatalf("lookup alice: %v", err)
	}
	if alice.IsAdmin {
		t.Fatal("expected non-admin user 'alice' to remain is_admin=0 after migration")
	}

	// Idempotency: reopening again must not fail (column already present).
	s.Close()
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second reopen (idempotency): %v", err)
	}
	defer s2.Close()
	if a2, err := s2.UserByUsername("admin"); err != nil || !a2.IsAdmin {
		t.Fatalf("admin flag lost on second reopen: err=%v", err)
	}
}
