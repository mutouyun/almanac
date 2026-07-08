package store

import (
	"fmt"
	"time"
)

// MonthSummary is the aggregated income/expense/balance for one calendar month
// (Asia/Shanghai wall-clock), plus the prior month's income/expense for
// "vs last month" deltas. Direction is derived from each entry's category:
// unclassified entries (category_id IS NULL) carry no direction and are
// excluded from income/expense, surfaced separately instead.
type MonthSummary struct {
	Month             string `json:"month"`
	Period            string `json:"period"`
	Value             string `json:"value"`
	IncomeCents       int64  `json:"income_cents"`
	ExpenseCents      int64  `json:"expense_cents"`
	BalanceCents      int64  `json:"balance_cents"`
	UnclassifiedCents int64  `json:"unclassified_cents"`
	UnclassifiedCount int64  `json:"unclassified_count"`
	PrevIncomeCents   int64  `json:"prev_income_cents"`
	PrevExpenseCents  int64  `json:"prev_expense_cents"`
}

// CategorySlice is one category's total for a given direction/month, used to
// feed the breakdown chart.
type CategorySlice struct {
	CategoryID int64  `json:"category_id"`
	Name       string `json:"name"`
	Direction  int    `json:"direction"`
	TotalCents int64  `json:"total_cents"`
}

// periodRange parses a period type ("month"/"year"/"all") plus its value and
// returns the [start, end) wall-clock bounds and the prior period's
// [prevStart, prevEnd), all in "YYYY-MM-DD HH:mm" form so they compare
// lexicographically against stored record_time values (dictionary order ==
// chronological order for this fixed-width format). For "all" every bound is
// empty: callers skip the record_time filter and report no prior period.
func periodRange(period, value string) (start, end, prevStart, prevEnd string, err error) {
	const layout = "2006-01-02 15:04"
	switch period {
	case "", "month":
		t, e := time.Parse("2006-01", value)
		if e != nil {
			return "", "", "", "", fmt.Errorf("invalid month %q: %w", value, e)
		}
		start = t.Format(layout)
		end = t.AddDate(0, 1, 0).Format(layout)
		prevStart = t.AddDate(0, -1, 0).Format(layout)
		prevEnd = start
	case "year":
		t, e := time.Parse("2006", value)
		if e != nil {
			return "", "", "", "", fmt.Errorf("invalid year %q: %w", value, e)
		}
		start = t.Format(layout)
		end = t.AddDate(1, 0, 0).Format(layout)
		prevStart = t.AddDate(-1, 0, 0).Format(layout)
		prevEnd = start
	case "all":
		// No bounds: whole history, no comparison period.
		return "", "", "", "", nil
	default:
		return "", "", "", "", fmt.Errorf("invalid period %q", period)
	}
	return start, end, prevStart, prevEnd, nil
}

// monthRange parses a "YYYY-MM" key and returns the [start, end) wall-clock
// string bounds plus the previous month's start, all in "YYYY-MM-DD HH:mm"
// form so they compare lexicographically against stored record_time values
// (dictionary order == chronological order for this fixed-width format).
func monthRange(month string) (start, end, prevStart string, err error) {
	s, e, ps, _, err := periodRange("month", month)
	return s, e, ps, err
}

// SummaryByMonth aggregates the user's entries for the given "YYYY-MM" month.
// Kept for backward compatibility; delegates to SummaryByPeriod.
func (s *Store) SummaryByMonth(userID int64, month string) (MonthSummary, error) {
	return s.SummaryByPeriod(userID, "month", month)
}

// SummaryByPeriod aggregates the user's entries for the given period
// ("month"/"year"/"all"). Income/expense are grouped by the assigned category's
// direction; unclassified entries are reported separately and excluded from
// income/expense/balance. For "all" there is no comparison period so prev_* are
// zero and the frontend hides month/year-over-period deltas.
func (s *Store) SummaryByPeriod(userID int64, period, value string) (MonthSummary, error) {
	start, end, prevStart, prevEnd, err := periodRange(period, value)
	if err != nil {
		return MonthSummary{}, err
	}
	if period == "" {
		period = "month"
	}
	sum := MonthSummary{Month: value, Period: period, Value: value}

	// Current period, grouped by derived direction (NULL category -> 0).
	// timeFilter is empty for "all" so the whole history is scanned.
	timeFilter := ""
	args := []any{userID}
	if start != "" {
		timeFilter = " AND e.record_time >= ? AND e.record_time < ?"
		args = append(args, start, end)
	}
	rows, err := s.db.Query(`
SELECT COALESCE(c.direction, 0) AS dir, COALESCE(SUM(e.amount_cents), 0), COUNT(*)
FROM ledger_entries e
LEFT JOIN categories c ON c.id = e.category_id
WHERE e.user_id = ? AND e.deleted_at IS NULL`+timeFilter+`
GROUP BY dir`, args...)
	if err != nil {
		return MonthSummary{}, fmt.Errorf("summary current: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var dir, total, count int64
		if err := rows.Scan(&dir, &total, &count); err != nil {
			return MonthSummary{}, fmt.Errorf("scan summary: %w", err)
		}
		switch dir {
		case 1:
			sum.IncomeCents += total
		case -1:
			sum.ExpenseCents += total
		default:
			sum.UnclassifiedCents += total
			sum.UnclassifiedCount += count
		}
	}
	if err := rows.Err(); err != nil {
		return MonthSummary{}, fmt.Errorf("iterate summary: %w", err)
	}
	sum.BalanceCents = sum.IncomeCents - sum.ExpenseCents

	// Previous period income/expense for deltas. Skipped for "all".
	if prevStart != "" {
		if err := s.db.QueryRow(`
SELECT
  COALESCE(SUM(CASE WHEN c.direction = 1 THEN e.amount_cents ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN c.direction = -1 THEN e.amount_cents ELSE 0 END), 0)
FROM ledger_entries e
LEFT JOIN categories c ON c.id = e.category_id
WHERE e.user_id = ? AND e.deleted_at IS NULL AND e.record_time >= ? AND e.record_time < ?`,
			userID, prevStart, prevEnd,
		).Scan(&sum.PrevIncomeCents, &sum.PrevExpenseCents); err != nil {
			return MonthSummary{}, fmt.Errorf("summary prev: %w", err)
		}
	}
	return sum, nil
}

// SummaryByCategory returns per-root-category totals for the given direction and
// month, sorted by total descending. Every entry is rolled up to its top-level
// (root) category via a recursive CTE, so a deep entry (e.g. 餐饮>饮品>咖啡)
// contributes to its root bucket (餐饮). Only classified entries whose root
// category matches the requested direction are included; direction is immutable
// down a tree so a root's direction applies to its whole subtree.
func (s *Store) SummaryByCategory(userID int64, month string, direction int) ([]CategorySlice, error) {
	return s.SummaryByCategoryPeriod(userID, "month", month, direction)
}

// SummaryByCategoryPeriod is SummaryByCategory generalized over period type
// ("month"/"year"/"all"). For "all" the record_time filter is dropped.
func (s *Store) SummaryByCategoryPeriod(userID int64, period, value string, direction int) ([]CategorySlice, error) {
	start, end, _, _, err := periodRange(period, value)
	if err != nil {
		return nil, err
	}
	timeFilter := ""
	args := []any{userID, userID, direction}
	if start != "" {
		timeFilter = "\n  AND e.record_time >= ? AND e.record_time < ?"
		args = append(args, start, end)
	}
	rows, err := s.db.Query(`
WITH RECURSIVE roots(id, root_id) AS (
    SELECT id, id FROM categories WHERE user_id = ? AND parent_id IS NULL
    UNION ALL
    SELECT c.id, r.root_id FROM categories c
    JOIN roots r ON c.parent_id = r.id
)
SELECT root.id, root.name, root.direction, SUM(e.amount_cents) AS total
FROM ledger_entries e
JOIN roots r ON r.id = e.category_id
JOIN categories root ON root.id = r.root_id
WHERE e.user_id = ? AND e.deleted_at IS NULL AND root.direction = ?`+timeFilter+`
GROUP BY root.id, root.name, root.direction
ORDER BY total DESC, root.id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("summary by category: %w", err)
	}
	defer rows.Close()

	slices := make([]CategorySlice, 0)
	for rows.Next() {
		var cs CategorySlice
		if err := rows.Scan(&cs.CategoryID, &cs.Name, &cs.Direction, &cs.TotalCents); err != nil {
			return nil, fmt.Errorf("scan category slice: %w", err)
		}
		slices = append(slices, cs)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate category slices: %w", err)
	}
	return slices, nil
}
