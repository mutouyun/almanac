package store

import (
	"errors"
	"testing"
)

// findEntry returns the EntryRow with the given id from the user's list, or
// nil if it is not present (e.g. soft-deleted). Fails the test on query error.
func findEntry(t *testing.T, s *Store, uid, id int64) *EntryRow {
	t.Helper()
	rows, _, err := s.ListEntries(uid, 200, 0)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	for i := range rows {
		if rows[i].ID == id {
			return &rows[i]
		}
	}
	return nil
}

// TestCreateManualEntry: a manual entry with a category derives its direction
// from that category, stores the amount unsigned, and is marked source=manual.
func TestCreateManualEntry(t *testing.T) {
	s, uid := newTestStore(t)
	food := mkcat(t, s, uid, nil, "餐饮", -1, 0, "")

	id, err := s.CreateManualEntry(uid, ManualEntryInput{
		CategoryID:  &food,
		AmountCents: 3500,
		RawType:     "午餐",
		RecordTime:  "2026-07-06 12:30",
		Note:        "和同事",
	})
	if err != nil {
		t.Fatalf("create manual entry: %v", err)
	}
	e := findEntry(t, s, uid, id)
	if e == nil {
		t.Fatal("created entry not found in list")
	}
	if e.AmountCents != 3500 {
		t.Errorf("amount = %d, want 3500", e.AmountCents)
	}
	if e.Direction != -1 {
		t.Errorf("direction = %d, want -1 (from category)", e.Direction)
	}
	if e.Source != "manual" {
		t.Errorf("source = %q, want manual", e.Source)
	}
	if e.Note != "和同事" {
		t.Errorf("note = %q, want 和同事", e.Note)
	}
}

// TestCreateManualEntryUnclassified: no category => direction 0 (待分类).
func TestCreateManualEntryUnclassified(t *testing.T) {
	s, uid := newTestStore(t)
	id, err := s.CreateManualEntry(uid, ManualEntryInput{
		AmountCents: 999,
		RawType:     "神秘支出",
		RecordTime:  "2026-07-06 09:00",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	e := findEntry(t, s, uid, id)
	if e == nil {
		t.Fatal("entry not found")
	}
	if e.CategoryID != nil {
		t.Errorf("category_id = %v, want nil", *e.CategoryID)
	}
	if e.Direction != 0 {
		t.Errorf("direction = %d, want 0 (unclassified)", e.Direction)
	}
}

// TestCreateManualEntryValidation: non-positive amount and empty fields reject.
func TestCreateManualEntryValidation(t *testing.T) {
	s, uid := newTestStore(t)
	cases := []struct {
		name string
		in   ManualEntryInput
		want error
	}{
		{"zero amount", ManualEntryInput{AmountCents: 0, RawType: "x", RecordTime: "2026-07-06 09:00"}, ErrInvalidAmount},
		{"negative amount", ManualEntryInput{AmountCents: -5, RawType: "x", RecordTime: "2026-07-06 09:00"}, ErrInvalidAmount},
		{"empty raw_type", ManualEntryInput{AmountCents: 100, RawType: "  ", RecordTime: "2026-07-06 09:00"}, ErrInvalidEntry},
		{"empty time", ManualEntryInput{AmountCents: 100, RawType: "x", RecordTime: ""}, ErrInvalidEntry},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.CreateManualEntry(uid, c.in)
			if !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// TestCreateManualEntryForeignCategory: a category owned by another user is
// rejected with ErrCategoryNotFound.
func TestCreateManualEntryForeignCategory(t *testing.T) {
	s, uid := newTestStore(t)
	otherID, err := s.CreateUser("bob", "pw123456")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	foreign := mkcat(t, s, otherID, nil, "别人的分类", -1, 0, "")
	_, err = s.CreateManualEntry(uid, ManualEntryInput{
		CategoryID:  &foreign,
		AmountCents: 100,
		RawType:     "x",
		RecordTime:  "2026-07-06 09:00",
	})
	if !errors.Is(err, ErrCategoryNotFound) {
		t.Errorf("err = %v, want ErrCategoryNotFound", err)
	}
}

// TestUpdateEntry: full-field edit changes amount/summary/time/note and
// re-derives direction from the new category (cross-direction allowed).
func TestUpdateEntry(t *testing.T) {
	s, uid := newTestStore(t)
	food := mkcat(t, s, uid, nil, "餐饮", -1, 0, "")
	salary := mkcat(t, s, uid, nil, "工资", 1, 0, "")

	id, err := s.CreateManualEntry(uid, ManualEntryInput{
		CategoryID: &food, AmountCents: 100, RawType: "旧", RecordTime: "2026-07-01 08:00",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Re-file the expense entry under an income category; direction flips.
	if err := s.UpdateEntry(uid, id, ManualEntryInput{
		CategoryID: &salary, AmountCents: 88800, RawType: "七月工资", RecordTime: "2026-07-05 10:00", Note: "到账",
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	e := findEntry(t, s, uid, id)
	if e == nil {
		t.Fatal("entry not found")
	}
	if e.AmountCents != 88800 || e.RawType != "七月工资" || e.RecordTime != "2026-07-05 10:00" || e.Note != "到账" {
		t.Errorf("fields not updated: %+v", e)
	}
	if e.Direction != 1 {
		t.Errorf("direction = %d, want 1 (from new income category)", e.Direction)
	}
}

// TestUpdateEntryNotFound: editing a missing/foreign entry yields ErrEntryNotFound.
func TestUpdateEntryNotFound(t *testing.T) {
	s, uid := newTestStore(t)
	err := s.UpdateEntry(uid, 99999, ManualEntryInput{
		AmountCents: 100, RawType: "x", RecordTime: "2026-07-06 09:00",
	})
	if !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("err = %v, want ErrEntryNotFound", err)
	}
}

// TestSoftDeleteEntry: a soft-deleted entry disappears from the list and the
// row count, and a second delete of the same id yields ErrEntryNotFound.
func TestSoftDeleteEntry(t *testing.T) {
	s, uid := newTestStore(t)
	id, err := s.CreateManualEntry(uid, ManualEntryInput{
		AmountCents: 500, RawType: "待删", RecordTime: "2026-07-06 09:00",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, total, _ := s.ListEntries(uid, 50, 0); total != 1 {
		t.Fatalf("pre-delete total = %d, want 1", total)
	}
	if err := s.SoftDeleteEntry(uid, id); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if e := findEntry(t, s, uid, id); e != nil {
		t.Error("deleted entry still visible in list")
	}
	_, total, _ := s.ListEntries(uid, 50, 0)
	if total != 0 {
		t.Errorf("post-delete total = %d, want 0", total)
	}
	if err := s.SoftDeleteEntry(uid, id); !errors.Is(err, ErrEntryNotFound) {
		t.Errorf("second delete err = %v, want ErrEntryNotFound", err)
	}
}

// TestSoftDeleteExcludedFromSummary: a soft-deleted entry drops out of the
// monthly summary aggregation.
func TestSoftDeleteExcludedFromSummary(t *testing.T) {
	s, uid := newTestStore(t)
	food := mkcat(t, s, uid, nil, "餐饮", -1, 0, "")
	id, err := s.CreateManualEntry(uid, ManualEntryInput{
		CategoryID: &food, AmountCents: 5000, RawType: "晚餐", RecordTime: "2026-07-06 19:00",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sum, _ := s.SummaryByMonth(uid, "2026-07")
	if sum.ExpenseCents != 5000 {
		t.Fatalf("pre-delete expense = %d, want 5000", sum.ExpenseCents)
	}
	if err := s.SoftDeleteEntry(uid, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	sum, _ = s.SummaryByMonth(uid, "2026-07")
	if sum.ExpenseCents != 0 {
		t.Errorf("post-delete expense = %d, want 0", sum.ExpenseCents)
	}
}
