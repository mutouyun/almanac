package store

import "testing"

// mkentryAt inserts an entry with a specific record_time (and optional category)
// for summary/month-boundary tests, returning its id.
func mkentryAt(t *testing.T, s *Store, uid int64, cents int64, recordTime string, catID *int64) int64 {
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
		RawType:     "test",
		RecordTime:  recordTime,
		Source:      "manual",
	})
	if err != nil {
		t.Fatalf("insert entry: %v", err)
	}
	return id
}

// TestSummaryByMonth verifies income/expense/balance aggregation with direction
// derived from the assigned category, unclassified entries excluded from
// income/expense but surfaced separately, and strict month boundaries.
func TestSummaryByMonth(t *testing.T) {
	s, uid := newTestStore(t)

	salary := mkcat(t, s, uid, nil, "工资", 1, 0, "")
	food := mkcat(t, s, uid, nil, "餐饮", -1, 0, "")

	// July entries.
	mkentryAt(t, s, uid, 500000, "2026-07-03 09:00", &salary) // +5000 income
	mkentryAt(t, s, uid, 3000, "2026-07-05 12:30", &food)     // -30 expense
	mkentryAt(t, s, uid, 1500, "2026-07-10 19:00", &food)     // -15 expense
	mkentryAt(t, s, uid, 8800, "2026-07-15 08:00", nil)       // unclassified

	// Boundary rows that must be excluded from July.
	mkentryAt(t, s, uid, 999900, "2026-06-30 23:59", &salary) // June
	mkentryAt(t, s, uid, 777700, "2026-08-01 00:00", &salary) // August

	sum, err := s.SummaryByMonth(uid, "2026-07")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.IncomeCents != 500000 {
		t.Errorf("income: want 500000, got %d", sum.IncomeCents)
	}
	if sum.ExpenseCents != 4500 {
		t.Errorf("expense: want 4500, got %d", sum.ExpenseCents)
	}
	if sum.BalanceCents != 495500 {
		t.Errorf("balance: want 495500, got %d", sum.BalanceCents)
	}
	if sum.UnclassifiedCents != 8800 {
		t.Errorf("unclassified cents: want 8800, got %d", sum.UnclassifiedCents)
	}
	if sum.UnclassifiedCount != 1 {
		t.Errorf("unclassified count: want 1, got %d", sum.UnclassifiedCount)
	}
}

// TestSummaryByMonthPrev verifies previous-month totals are returned alongside
// the current month for the "vs last month" card deltas.
func TestSummaryByMonthPrev(t *testing.T) {
	s, uid := newTestStore(t)
	salary := mkcat(t, s, uid, nil, "工资", 1, 0, "")
	food := mkcat(t, s, uid, nil, "餐饮", -1, 0, "")

	mkentryAt(t, s, uid, 400000, "2026-06-10 09:00", &salary) // prev income
	mkentryAt(t, s, uid, 2000, "2026-06-12 12:00", &food)     // prev expense
	mkentryAt(t, s, uid, 500000, "2026-07-03 09:00", &salary) // cur income

	sum, err := s.SummaryByMonth(uid, "2026-07")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.PrevIncomeCents != 400000 {
		t.Errorf("prev income: want 400000, got %d", sum.PrevIncomeCents)
	}
	if sum.PrevExpenseCents != 2000 {
		t.Errorf("prev expense: want 2000, got %d", sum.PrevExpenseCents)
	}
}

// TestSummaryByCategory verifies per-category totals for one direction, sorted
// by total descending, with only the requested direction included.
func TestSummaryByCategory(t *testing.T) {
	s, uid := newTestStore(t)
	food := mkcat(t, s, uid, nil, "餐饮", -1, 0, "")
	transit := mkcat(t, s, uid, nil, "交通", -1, 0, "")
	salary := mkcat(t, s, uid, nil, "工资", 1, 0, "")

	mkentryAt(t, s, uid, 3000, "2026-07-05 12:00", &food)
	mkentryAt(t, s, uid, 2000, "2026-07-06 12:00", &food)
	mkentryAt(t, s, uid, 1000, "2026-07-07 08:00", &transit)
	mkentryAt(t, s, uid, 500000, "2026-07-03 09:00", &salary) // income, excluded
	mkentryAt(t, s, uid, 9999, "2026-06-30 12:00", &food)     // June, excluded

	slices, err := s.SummaryByCategory(uid, "2026-07", -1)
	if err != nil {
		t.Fatalf("summary by category: %v", err)
	}
	if len(slices) != 2 {
		t.Fatalf("want 2 expense categories, got %d", len(slices))
	}
	if slices[0].Name != "餐饮" || slices[0].TotalCents != 5000 {
		t.Errorf("slice[0]: want 餐饮/5000, got %s/%d", slices[0].Name, slices[0].TotalCents)
	}
	if slices[1].Name != "交通" || slices[1].TotalCents != 1000 {
		t.Errorf("slice[1]: want 交通/1000, got %s/%d", slices[1].Name, slices[1].TotalCents)
	}
}

// TestMonthRange verifies the [start, end) string bounds for a YYYY-MM key,
// including December rolling into the next year.
func TestMonthRange(t *testing.T) {
	cases := []struct {
		month, start, end, prevStart string
	}{
		{"2026-07", "2026-07-01 00:00", "2026-08-01 00:00", "2026-06-01 00:00"},
		{"2026-12", "2026-12-01 00:00", "2027-01-01 00:00", "2026-11-01 00:00"},
		{"2026-01", "2026-01-01 00:00", "2026-02-01 00:00", "2025-12-01 00:00"},
	}
	for _, c := range cases {
		start, end, prevStart, err := monthRange(c.month)
		if err != nil {
			t.Fatalf("monthRange(%q): %v", c.month, err)
		}
		if start != c.start || end != c.end || prevStart != c.prevStart {
			t.Errorf("monthRange(%q) = %q,%q,%q; want %q,%q,%q",
				c.month, start, end, prevStart, c.start, c.end, c.prevStart)
		}
	}
	if _, _, _, err := monthRange("bad"); err == nil {
		t.Error("monthRange(bad): want error, got nil")
	}
}
