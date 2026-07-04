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
