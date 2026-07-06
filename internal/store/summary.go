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

// monthRange parses a "YYYY-MM" key and returns the [start, end) wall-clock
// string bounds plus the previous month's start, all in "YYYY-MM-DD HH:mm"
// form so they compare lexicographically against stored record_time values
// (dictionary order == chronological order for this fixed-width format).
func monthRange(month string) (start, end, prevStart string, err error) {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid month %q: %w", month, err)
	}
	const layout = "2006-01-02 15:04"
	start = t.Format(layout)
	end = t.AddDate(0, 1, 0).Format(layout)
	prevStart = t.AddDate(0, -1, 0).Format(layout)
	return start, end, prevStart, nil
}

// SummaryByMonth aggregates the user's entries for the given "YYYY-MM" month.
// Income/expense are grouped by the assigned category's direction; unclassified
// entries are reported separately and excluded from income/expense/balance.
func (s *Store) SummaryByMonth(userID int64, month string) (MonthSummary, error) {
	start, end, prevStart, err := monthRange(month)
	if err != nil {
		return MonthSummary{}, err
	}
	sum := MonthSummary{Month: month}

	// Current month, grouped by derived direction (NULL category -> 0).
	rows, err := s.db.Query(`
SELECT COALESCE(c.direction, 0) AS dir, COALESCE(SUM(e.amount_cents), 0), COUNT(*)
FROM ledger_entries e
LEFT JOIN categories c ON c.id = e.category_id
WHERE e.user_id = ? AND e.record_time >= ? AND e.record_time < ?
GROUP BY dir`, userID, start, end)
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

	// Previous month income/expense for deltas.
	if err := s.db.QueryRow(`
SELECT
  COALESCE(SUM(CASE WHEN c.direction = 1 THEN e.amount_cents ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN c.direction = -1 THEN e.amount_cents ELSE 0 END), 0)
FROM ledger_entries e
LEFT JOIN categories c ON c.id = e.category_id
WHERE e.user_id = ? AND e.record_time >= ? AND e.record_time < ?`,
		userID, prevStart, start,
	).Scan(&sum.PrevIncomeCents, &sum.PrevExpenseCents); err != nil {
		return MonthSummary{}, fmt.Errorf("summary prev: %w", err)
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
	start, end, _, err := monthRange(month)
	if err != nil {
		return nil, err
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
WHERE e.user_id = ? AND root.direction = ?
  AND e.record_time >= ? AND e.record_time < ?
GROUP BY root.id, root.name, root.direction
ORDER BY total DESC, root.id ASC`, userID, userID, direction, start, end)
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
