package store

import (
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
