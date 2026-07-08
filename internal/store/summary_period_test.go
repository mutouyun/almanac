package store

import "testing"

// TestSummaryByPeriodYear verifies yearly aggregation spans all months of the
// requested year, excludes other years, and returns the prior year for deltas.
func TestSummaryByPeriodYear(t *testing.T) {
	s, uid := newTestStore(t)

	salary := mkcat(t, s, uid, nil, "工资", 1, 0, "")
	food := mkcat(t, s, uid, nil, "餐饮", -1, 0, "")

	// 2026 entries across different months.
	mkentryAt(t, s, uid, 500000, "2026-01-03 09:00", &salary) // +5000
	mkentryAt(t, s, uid, 300000, "2026-07-10 09:00", &salary) // +3000
	mkentryAt(t, s, uid, 4500, "2026-12-31 23:59", &food)     // -45

	// Prior year (2025) for delta.
	mkentryAt(t, s, uid, 100000, "2025-06-01 09:00", &salary) // +1000 prev income
	mkentryAt(t, s, uid, 2000, "2025-06-02 09:00", &food)     // -20 prev expense

	// Boundary: next year must be excluded.
	mkentryAt(t, s, uid, 999900, "2027-01-01 00:00", &salary)

	sum, err := s.SummaryByPeriod(uid, "year", "2026")
	if err != nil {
		t.Fatalf("summary year: %v", err)
	}
	if sum.IncomeCents != 800000 {
		t.Errorf("income: want 800000, got %d", sum.IncomeCents)
	}
	if sum.ExpenseCents != 4500 {
		t.Errorf("expense: want 4500, got %d", sum.ExpenseCents)
	}
	if sum.PrevIncomeCents != 100000 {
		t.Errorf("prev income: want 100000, got %d", sum.PrevIncomeCents)
	}
	if sum.PrevExpenseCents != 2000 {
		t.Errorf("prev expense: want 2000, got %d", sum.PrevExpenseCents)
	}
	if sum.Period != "year" || sum.Value != "2026" {
		t.Errorf("period/value: want year/2026, got %s/%s", sum.Period, sum.Value)
	}
}

// TestSummaryByPeriodAll verifies "all" scans the whole history regardless of
// time and carries no prior-period comparison.
func TestSummaryByPeriodAll(t *testing.T) {
	s, uid := newTestStore(t)

	salary := mkcat(t, s, uid, nil, "工资", 1, 0, "")
	food := mkcat(t, s, uid, nil, "餐饮", -1, 0, "")

	mkentryAt(t, s, uid, 100000, "2024-01-01 09:00", &salary) // +1000
	mkentryAt(t, s, uid, 500000, "2026-07-10 09:00", &salary) // +5000
	mkentryAt(t, s, uid, 4500, "2026-12-31 23:59", &food)     // -45
	mkentryAt(t, s, uid, 8800, "2026-07-15 08:00", nil)       // unclassified

	sum, err := s.SummaryByPeriod(uid, "all", "")
	if err != nil {
		t.Fatalf("summary all: %v", err)
	}
	if sum.IncomeCents != 600000 {
		t.Errorf("income: want 600000, got %d", sum.IncomeCents)
	}
	if sum.ExpenseCents != 4500 {
		t.Errorf("expense: want 4500, got %d", sum.ExpenseCents)
	}
	if sum.UnclassifiedCents != 8800 || sum.UnclassifiedCount != 1 {
		t.Errorf("unclassified: want 8800/1, got %d/%d", sum.UnclassifiedCents, sum.UnclassifiedCount)
	}
	if sum.PrevIncomeCents != 0 || sum.PrevExpenseCents != 0 {
		t.Errorf("all period must have zero prev, got %d/%d", sum.PrevIncomeCents, sum.PrevExpenseCents)
	}
	if sum.Period != "all" {
		t.Errorf("period: want all, got %s", sum.Period)
	}
}

// TestSummaryByPeriodBadInputs verifies invalid period type and bad values err.
func TestSummaryByPeriodBadInputs(t *testing.T) {
	s, uid := newTestStore(t)
	if _, err := s.SummaryByPeriod(uid, "week", "2026-07"); err == nil {
		t.Error("expected error for unknown period 'week'")
	}
	if _, err := s.SummaryByPeriod(uid, "year", "notayear"); err == nil {
		t.Error("expected error for bad year value")
	}
}

// TestSummaryByCategoryPeriodYearAndAll verifies the per-category breakdown
// honors year bounds and drops the time filter for "all".
func TestSummaryByCategoryPeriodYearAndAll(t *testing.T) {
	s, uid := newTestStore(t)

	food := mkcat(t, s, uid, nil, "餐饮", -1, 0, "")

	mkentryAt(t, s, uid, 3000, "2026-02-01 09:00", &food) // -30 in 2026
	mkentryAt(t, s, uid, 7000, "2026-09-01 09:00", &food) // -70 in 2026
	mkentryAt(t, s, uid, 5000, "2025-05-01 09:00", &food) // -50 in 2025

	year, err := s.SummaryByCategoryPeriod(uid, "year", "2026", -1)
	if err != nil {
		t.Fatalf("by-category year: %v", err)
	}
	if len(year) != 1 || year[0].TotalCents != 10000 {
		t.Errorf("year total: want one slice of 10000, got %+v", year)
	}

	all, err := s.SummaryByCategoryPeriod(uid, "all", "", -1)
	if err != nil {
		t.Fatalf("by-category all: %v", err)
	}
	if len(all) != 1 || all[0].TotalCents != 15000 {
		t.Errorf("all total: want one slice of 15000, got %+v", all)
	}
}
