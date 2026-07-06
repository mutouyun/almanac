package store

import "testing"

// mkentryFull inserts an entry with explicit raw_type/note/time/category so the
// filter tests can control every field the filter touches.
func mkentryFull(t *testing.T, s *Store, uid int64, cents int64, raw, note, recordTime string, catID *int64) int64 {
	t.Helper()
	ledger, err := s.DefaultLedgerID(uid)
	if err != nil {
		t.Fatalf("default ledger: %v", err)
	}
	id, err := s.InsertEntry(Entry{
		UserID:      uid,
		LedgerID:    ledger,
		CategoryID:  catID,
		AmountCents: cents,
		RawType:     raw,
		Note:        note,
		RecordTime:  recordTime,
		Source:      "manual",
	})
	if err != nil {
		t.Fatalf("insert entry: %v", err)
	}
	return id
}

// filterIDs runs ListEntries with a filter and returns the matching ids (and
// the reported total) for compact assertions.
func filterIDs(t *testing.T, s *Store, uid int64, f EntryFilter) ([]int64, int) {
	t.Helper()
	rows, total, err := s.ListEntries(uid, f, 200, 0)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids, total
}

func hasID(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestListEntriesFilterDirection: income/expense/unclassified narrowing.
func TestListEntriesFilterDirection(t *testing.T) {
	s, uid := newTestStore(t)
	food := mkcat(t, s, uid, nil, "\u9910\u996e", -1, 0, "")
	salary := mkcat(t, s, uid, nil, "\u5de5\u8d44", 1, 0, "")
	exp := mkentryFull(t, s, uid, 3000, "\u5348\u9910", "", "2026-07-05 12:00", &food)
	inc := mkentryFull(t, s, uid, 88800, "\u53d1\u5de5\u8d44", "", "2026-07-05 10:00", &salary)
	unc := mkentryFull(t, s, uid, 500, "\u4e0d\u660e", "", "2026-07-05 09:00", nil)

	d := -1
	ids, total := filterIDs(t, s, uid, EntryFilter{Direction: &d})
	if total != 1 || !hasID(ids, exp) {
		t.Errorf("expense filter = %v (total %d), want [%d]", ids, total, exp)
	}
	d2 := 1
	ids, _ = filterIDs(t, s, uid, EntryFilter{Direction: &d2})
	if len(ids) != 1 || !hasID(ids, inc) {
		t.Errorf("income filter = %v, want [%d]", ids, inc)
	}
	d0 := 0
	ids, _ = filterIDs(t, s, uid, EntryFilter{Direction: &d0})
	if len(ids) != 1 || !hasID(ids, unc) {
		t.Errorf("unclassified filter = %v, want [%d]", ids, unc)
	}
}

// TestListEntriesFilterCategorySubtree: filtering by a parent category matches
// entries filed under its children too.
func TestListEntriesFilterCategorySubtree(t *testing.T) {
	s, uid := newTestStore(t)
	food := mkcat(t, s, uid, nil, "\u9910\u996e", -1, 0, "")
	drink := mkcat(t, s, uid, &food, "\u996e\u6599", -1, 0, "")
	other := mkcat(t, s, uid, nil, "\u4ea4\u901a", -1, 0, "")
	parentEntry := mkentryFull(t, s, uid, 3000, "\u5348\u9910", "", "2026-07-05 12:00", &food)
	childEntry := mkentryFull(t, s, uid, 1500, "\u5496\u5561", "", "2026-07-05 13:00", &drink)
	otherEntry := mkentryFull(t, s, uid, 500, "\u5730\u94c1", "", "2026-07-05 14:00", &other)

	sub, err := s.SubtreeCategoryIDs(uid, food)
	if err != nil {
		t.Fatalf("subtree: %v", err)
	}
	ids, total := filterIDs(t, s, uid, EntryFilter{CategoryIDs: sub})
	if total != 2 || !hasID(ids, parentEntry) || !hasID(ids, childEntry) || hasID(ids, otherEntry) {
		t.Errorf("subtree filter = %v (total %d), want [%d %d]", ids, total, parentEntry, childEntry)
	}
}

// TestListEntriesFilterKeyword: substring match over raw_type OR note.
func TestListEntriesFilterKeyword(t *testing.T) {
	s, uid := newTestStore(t)
	a := mkentryFull(t, s, uid, 3000, "\u661f\u5df4\u514b\u5496\u5561", "", "2026-07-05 12:00", nil)
	b := mkentryFull(t, s, uid, 1500, "\u5348\u9910", "\u516c\u53f8\u9644\u8fd1", "2026-07-05 13:00", nil)
	c := mkentryFull(t, s, uid, 500, "\u5730\u94c1", "", "2026-07-05 14:00", nil)

	ids, _ := filterIDs(t, s, uid, EntryFilter{Keyword: "\u5496\u5561"})
	if len(ids) != 1 || !hasID(ids, a) {
		t.Errorf("keyword raw_type = %v, want [%d]", ids, a)
	}
	ids, _ = filterIDs(t, s, uid, EntryFilter{Keyword: "\u516c\u53f8"})
	if len(ids) != 1 || !hasID(ids, b) {
		t.Errorf("keyword note = %v, want [%d]", ids, b)
	}
	ids, _ = filterIDs(t, s, uid, EntryFilter{Keyword: "\u65e0\u5339\u914d"})
	if len(ids) != 0 {
		t.Errorf("keyword no-match = %v, want []", ids)
	}
	_ = c
}

// TestListEntriesFilterTimeAndAmount: record_time bounds and cents range.
func TestListEntriesFilterTimeAndAmount(t *testing.T) {
	s, uid := newTestStore(t)
	early := mkentryFull(t, s, uid, 1000, "a", "", "2026-07-01 09:00", nil)
	mid := mkentryFull(t, s, uid, 5000, "b", "", "2026-07-05 09:00", nil)
	late := mkentryFull(t, s, uid, 9000, "c", "", "2026-07-10 09:00", nil)

	ids, _ := filterIDs(t, s, uid, EntryFilter{StartTime: "2026-07-03 00:00", EndTime: "2026-07-08 00:00"})
	if len(ids) != 1 || !hasID(ids, mid) {
		t.Errorf("time range = %v, want [%d]", ids, mid)
	}
	min := int64(2000)
	max := int64(6000)
	ids, _ = filterIDs(t, s, uid, EntryFilter{MinCents: &min, MaxCents: &max})
	if len(ids) != 1 || !hasID(ids, mid) {
		t.Errorf("amount range = %v, want [%d]", ids, mid)
	}
	_ = early
	_ = late
}

// TestListEntriesFilterCombined: multiple dimensions AND together.
func TestListEntriesFilterCombined(t *testing.T) {
	s, uid := newTestStore(t)
	food := mkcat(t, s, uid, nil, "\u9910\u996e", -1, 0, "")
	want := mkentryFull(t, s, uid, 3000, "\u5348\u9910\u5496\u5561", "", "2026-07-05 12:00", &food)
	mkentryFull(t, s, uid, 3000, "\u665a\u9910", "", "2026-07-05 12:00", &food)          // no keyword
	mkentryFull(t, s, uid, 3000, "\u5496\u5561", "", "2026-07-20 12:00", &food)          // out of time

	d := -1
	ids, _ := filterIDs(t, s, uid, EntryFilter{
		Direction: &d,
		Keyword:   "\u5496\u5561",
		StartTime: "2026-07-01 00:00",
		EndTime:   "2026-07-10 00:00",
	})
	if len(ids) != 1 || !hasID(ids, want) {
		t.Errorf("combined filter = %v, want [%d]", ids, want)
	}
}
