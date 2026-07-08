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

// TestListEntriesEndExclusive: EndExclusive uses a strict `<` so an entry at
// the exact boundary (first instant of the next period) is excluded, matching
// the [start, end) window the overview aggregation uses.
func TestListEntriesEndExclusive(t *testing.T) {
	s, uid := newTestStore(t)
	inJuly := mkentryFull(t, s, uid, 1000, "a", "", "2026-07-31 23:59", nil)
	boundary := mkentryFull(t, s, uid, 2000, "b", "", "2026-08-01 00:00", nil)

	// Month window [2026-07-01 00:00, 2026-08-01 00:00): boundary excluded.
	ids, total := filterIDs(t, s, uid, EntryFilter{
		StartTime:    "2026-07-01 00:00",
		EndExclusive: "2026-08-01 00:00",
	})
	if total != 1 || !hasID(ids, inJuly) || hasID(ids, boundary) {
		t.Errorf("end-exclusive window = %v (total %d), want [%d]", ids, total, inJuly)
	}

	// Inclusive EndTime at the same boundary WOULD include it (contrast).
	ids, _ = filterIDs(t, s, uid, EntryFilter{
		StartTime: "2026-07-01 00:00",
		EndTime:   "2026-08-01 00:00",
	})
	if !hasID(ids, boundary) {
		t.Errorf("inclusive end should include boundary %d, got %v", boundary, ids)
	}
}

// TestPeriodRangeFeedsEntryFilter: the exported PeriodRange bounds, fed into an
// EntryFilter (start + EndExclusive), select exactly the period's entries.
func TestPeriodRangeFeedsEntryFilter(t *testing.T) {
	s, uid := newTestStore(t)
	jun := mkentryFull(t, s, uid, 1000, "jun", "", "2026-06-15 12:00", nil)
	jul := mkentryFull(t, s, uid, 2000, "jul", "", "2026-07-15 12:00", nil)
	nextYear := mkentryFull(t, s, uid, 3000, "y27", "", "2027-01-02 12:00", nil)

	// Month 2026-07 -> only the July entry.
	start, end, err := PeriodRange("month", "2026-07")
	if err != nil {
		t.Fatalf("PeriodRange month: %v", err)
	}
	ids, total := filterIDs(t, s, uid, EntryFilter{StartTime: start, EndExclusive: end})
	if total != 1 || !hasID(ids, jul) {
		t.Errorf("month window = %v (total %d), want [%d]", ids, total, jul)
	}

	// Year 2026 -> June + July, not the 2027 entry.
	start, end, err = PeriodRange("year", "2026")
	if err != nil {
		t.Fatalf("PeriodRange year: %v", err)
	}
	ids, total = filterIDs(t, s, uid, EntryFilter{StartTime: start, EndExclusive: end})
	if total != 2 || !hasID(ids, jun) || !hasID(ids, jul) || hasID(ids, nextYear) {
		t.Errorf("year window = %v (total %d), want [%d %d]", ids, total, jun, jul)
	}

	// All -> empty bounds, no time filter, everything.
	start, end, err = PeriodRange("all", "")
	if err != nil {
		t.Fatalf("PeriodRange all: %v", err)
	}
	if start != "" || end != "" {
		t.Errorf("all period should have empty bounds, got %q %q", start, end)
	}
	ids, total = filterIDs(t, s, uid, EntryFilter{StartTime: start, EndExclusive: end})
	if total != 3 {
		t.Errorf("all window total = %d, want 3 (ids %v)", total, ids)
	}
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
