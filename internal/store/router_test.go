package store

import (
	"path/filepath"
	"testing"
)

// newTestStore opens a fresh store in a temp dir and returns it with the seeded
// admin user's id (user 1).
func newTestStore(t *testing.T) (*Store, int64) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "router.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	admin, err := s.UserByUsername("admin")
	if err != nil {
		t.Fatalf("lookup admin: %v", err)
	}
	return s, admin.ID
}

// mkcat creates a category and returns its id, failing the test on error.
func mkcat(t *testing.T, s *Store, uid int64, parent *int64, name string, dir, sort int, regex string) int64 {
	t.Helper()
	id, err := s.CreateCategory(uid, parent, name, dir, sort, regex)
	if err != nil {
		t.Fatalf("create category %q: %v", name, err)
	}
	return id
}

// classify is a helper that fails on error and returns the matched id or 0.
func classify(t *testing.T, s *Store, uid int64, cents int64, raw string) int64 {
	t.Helper()
	id, err := s.ClassifyEntry(uid, cents, raw)
	if err != nil {
		t.Fatalf("classify %q: %v", raw, err)
	}
	if id == nil {
		return 0
	}
	return *id
}

// TestRouteContainsMatch: a bare keyword matches anywhere in the raw text
// (unanchored "contains"), and the sign of the amount picks the direction.
func TestRouteContainsMatch(t *testing.T) {
	s, uid := newTestStore(t)
	coffee := mkcat(t, s, uid, nil, "咖啡", -1, 0, "瑞幸")

	if got := classify(t, s, uid, -1990, "瑞幸咖啡消费"); got != coffee {
		t.Fatalf("contains match: got %d, want %d", got, coffee)
	}
	// No rule matches -> unclassified.
	if got := classify(t, s, uid, -500, "星巴克"); got != 0 {
		t.Fatalf("no match should be unclassified, got %d", got)
	}
}

// TestRouteDirectionIsolation: an expense keyword must not match an income
// entry even if the text matches, and vice versa.
func TestRouteDirectionIsolation(t *testing.T) {
	s, uid := newTestStore(t)
	salary := mkcat(t, s, uid, nil, "工资", 1, 0, "公司")
	reimburse := mkcat(t, s, uid, nil, "报销支出", -1, 0, "公司")

	// Positive amount -> income direction -> salary.
	if got := classify(t, s, uid, 500000, "公司转账"); got != salary {
		t.Fatalf("income should match salary %d, got %d", salary, got)
	}
	// Negative amount -> expense direction -> reimburse.
	if got := classify(t, s, uid, -500000, "公司转账"); got != reimburse {
		t.Fatalf("expense should match reimburse %d, got %d", reimburse, got)
	}
}

// TestRouteLevelPriority: a deeper (more specific) category wins over a
// shallower one when both would match.
func TestRouteLevelPriority(t *testing.T) {
	s, uid := newTestStore(t)
	food := mkcat(t, s, uid, nil, "餐饮", -1, 0, "美团")
	drink := mkcat(t, s, uid, &food, "饮品", -1, 0, "美团")

	// Both level-1 食 and level-2 drink match "美团"; deeper wins.
	if got := classify(t, s, uid, -3000, "美团外卖"); got != drink {
		t.Fatalf("deeper category should win: got %d, want %d", got, drink)
	}
}

// TestRouteSortOrderTiebreak: at the same level, lower sort_order wins.
func TestRouteSortOrderTiebreak(t *testing.T) {
	s, uid := newTestStore(t)
	first := mkcat(t, s, uid, nil, "甲", -1, 1, "通用")
	_ = mkcat(t, s, uid, nil, "乙", -1, 2, "通用")

	if got := classify(t, s, uid, -100, "通用消费"); got != first {
		t.Fatalf("lower sort_order should win: got %d, want %d", got, first)
	}
}

// TestRouteAnchoredExact: a user-written ^...$ pattern is an exact match, not
// contains.
func TestRouteAnchoredExact(t *testing.T) {
	s, uid := newTestStore(t)
	exact := mkcat(t, s, uid, nil, "精确", -1, 0, "^充值$")

	if got := classify(t, s, uid, -100, "充值"); got != exact {
		t.Fatalf("anchored exact should match identical text: got %d", got)
	}
	if got := classify(t, s, uid, -100, "账户充值成功"); got != 0 {
		t.Fatalf("anchored exact should NOT match substring, got %d", got)
	}
}

// TestRouteCacheInvalidation: after adding/updating a rule the next
// classification reflects the change (cache was invalidated).
func TestRouteCacheInvalidation(t *testing.T) {
	s, uid := newTestStore(t)
	// Prime the cache with no matching rule.
	if got := classify(t, s, uid, -100, "滴滴出行"); got != 0 {
		t.Fatalf("expected unclassified before rule exists, got %d", got)
	}
	taxi := mkcat(t, s, uid, nil, "交通", -1, 0, "滴滴")
	// Cache must have been invalidated by CreateCategory.
	if got := classify(t, s, uid, -100, "滴滴出行"); got != taxi {
		t.Fatalf("expected match after create invalidates cache, got %d", got)
	}
	// Update the regex; next classify must reflect it.
	if err := s.UpdateCategory(uid, taxi, "交通", 0, "高德"); err != nil {
		t.Fatalf("update category: %v", err)
	}
	if got := classify(t, s, uid, -100, "滴滴出行"); got != 0 {
		t.Fatalf("old keyword should no longer match after update, got %d", got)
	}
	if got := classify(t, s, uid, -100, "高德打车"); got != taxi {
		t.Fatalf("new keyword should match after update, got %d", got)
	}
}

// TestInvalidRegexRejected: saving a category with an uncompilable pattern is
// rejected at save time.
func TestInvalidRegexRejected(t *testing.T) {
	s, uid := newTestStore(t)
	if _, err := s.CreateCategory(uid, nil, "坏", -1, 0, "[unclosed"); err != ErrInvalidRegex {
		t.Fatalf("expected ErrInvalidRegex on create, got %v", err)
	}
	good := mkcat(t, s, uid, nil, "好", -1, 0, "正常")
	if err := s.UpdateCategory(uid, good, "好", 0, "(broken"); err != ErrInvalidRegex {
		t.Fatalf("expected ErrInvalidRegex on update, got %v", err)
	}
}

// TestZeroAmountUnclassified: a zero amount never routes.
func TestZeroAmountUnclassified(t *testing.T) {
	s, uid := newTestStore(t)
	mkcat(t, s, uid, nil, "任意", -1, 0, "x")
	if got := classify(t, s, uid, 0, "x"); got != 0 {
		t.Fatalf("zero amount must be unclassified, got %d", got)
	}
}

// mkentry inserts an entry for the user and returns its id.
func mkentry(t *testing.T, s *Store, uid int64, cents int64, raw string) int64 {
	t.Helper()
	ledger, err := s.DefaultLedgerID(uid)
	if err != nil {
		t.Fatalf("default ledger: %v", err)
	}
	id, err := s.InsertEntry(Entry{
		UserID:      uid,
		LedgerID:    ledger,
		AmountCents: cents,
		RawType:     raw,
		RecordTime:  "2026-07-05 12:00",
		Source:      "manual",
	})
	if err != nil {
		t.Fatalf("insert entry: %v", err)
	}
	return id
}

// entryCategory returns the category_id of an entry (0 = unclassified).
func entryCategory(t *testing.T, s *Store, uid, entryID int64) int64 {
	t.Helper()
	rows, _, err := s.ListEntries(uid, 200, 0)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	for _, e := range rows {
		if e.ID == entryID {
			if e.CategoryID == nil {
				return 0
			}
			return *e.CategoryID
		}
	}
	t.Fatalf("entry %d not found", entryID)
	return 0
}

// TestUpdateEntryCategory: manual assign, clear, and the ownership/direction
// guards.
func TestUpdateEntryCategory(t *testing.T) {
	s, uid := newTestStore(t)
	expenseCat := mkcat(t, s, uid, nil, "餐饮", -1, 0, "")
	incomeCat := mkcat(t, s, uid, nil, "工资", 1, 0, "")
	entry := mkentry(t, s, uid, -3000, "unknown expense")

	// Assign an expense category to an expense entry -> ok.
	if err := s.UpdateEntryCategory(uid, entry, &expenseCat); err != nil {
		t.Fatalf("assign expense category: %v", err)
	}
	if got := entryCategory(t, s, uid, entry); got != expenseCat {
		t.Fatalf("expected category %d, got %d", expenseCat, got)
	}

	// Direction mismatch: income category on an expense entry -> rejected.
	if err := s.UpdateEntryCategory(uid, entry, &incomeCat); err != ErrDirectionMismatch {
		t.Fatalf("expected ErrDirectionMismatch, got %v", err)
	}

	// Clear (unclassify) with nil.
	if err := s.UpdateEntryCategory(uid, entry, nil); err != nil {
		t.Fatalf("clear category: %v", err)
	}
	if got := entryCategory(t, s, uid, entry); got != 0 {
		t.Fatalf("expected unclassified after clear, got %d", got)
	}

	// Nonexistent category -> ErrCategoryNotFound.
	bogus := int64(99999)
	if err := s.UpdateEntryCategory(uid, entry, &bogus); err != ErrCategoryNotFound {
		t.Fatalf("expected ErrCategoryNotFound, got %v", err)
	}

	// Nonexistent entry -> ErrEntryNotFound.
	if err := s.UpdateEntryCategory(uid, 99999, &expenseCat); err != ErrEntryNotFound {
		t.Fatalf("expected ErrEntryNotFound, got %v", err)
	}
}

// TestCategoryPath: derive the full ">"-joined path and reflect renames after
// cache invalidation.
func TestCategoryPath(t *testing.T) {
	s, uid := newTestStore(t)
	food := mkcat(t, s, uid, nil, "餐饮", -1, 0, "")
	drink := mkcat(t, s, uid, &food, "饮品", -1, 0, "")
	coffee := mkcat(t, s, uid, &drink, "咖啡", -1, 0, "")

	if got, _ := s.CategoryPath(uid, coffee); got != "餐饮>饮品>咖啡" {
		t.Fatalf("deep path: got %q, want 餐饮>饮品>咖啡", got)
	}
	if got, _ := s.CategoryPath(uid, food); got != "餐饮" {
		t.Fatalf("root path: got %q, want 餐饮", got)
	}
	if got, _ := s.CategoryPath(uid, 99999); got != "" {
		t.Fatalf("unknown id should be empty, got %q", got)
	}

	if err := s.UpdateCategory(uid, drink, "水饮", 0, ""); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got, _ := s.CategoryPath(uid, coffee); got != "餐饮>水饮>咖啡" {
		t.Fatalf("path after rename: got %q, want 餐饮>水饮>咖啡", got)
	}
}

// TestListEntriesCategoryPath: ListEntries fills category_path for classified
// entries and leaves it empty for unclassified ones.
func TestListEntriesCategoryPath(t *testing.T) {
	s, uid := newTestStore(t)
	food := mkcat(t, s, uid, nil, "餐饮", -1, 0, "")
	coffee := mkcat(t, s, uid, &food, "咖啡", -1, 0, "")
	classified := mkentry(t, s, uid, -1990, "latte")
	if err := s.UpdateEntryCategory(uid, classified, &coffee); err != nil {
		t.Fatalf("assign: %v", err)
	}
	unclassified := mkentry(t, s, uid, -500, "mystery")

	rows, _, err := s.ListEntries(uid, 200, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, e := range rows {
		if e.ID == classified && e.CategoryPath != "餐饮>咖啡" {
			t.Fatalf("classified path: got %q, want 餐饮>咖啡", e.CategoryPath)
		}
		if e.ID == unclassified && e.CategoryPath != "" {
			t.Fatalf("unclassified path should be empty, got %q", e.CategoryPath)
		}
	}
}

// TestUpdateEntryCategoryCrossUser: user B cannot touch user A's entry.
func TestMoveCategory(t *testing.T) {
	s, uid := newTestStore(t)
	// expense tree: A(root) -> B -> C ; and standalone root D
	a := mkcat(t, s, uid, nil, "A", -1, 0, "")
	b := mkcat(t, s, uid, &a, "B", -1, 0, "")
	c := mkcat(t, s, uid, &b, "C", -1, 0, "")
	d := mkcat(t, s, uid, nil, "D", -1, 0, "")
	// income root E (different direction)
	e := mkcat(t, s, uid, nil, "E", 1, 0, "")

	levelOf := func(id int64) int {
		cats, _ := s.ListCategories(uid)
		for _, x := range cats {
			if x.ID == id {
				return x.Level
			}
		}
		return -1
	}

	// Move B (with child C) under D: B becomes level 2, C cascades to level 3.
	if err := s.MoveCategory(uid, b, &d); err != nil {
		t.Fatalf("move B under D: %v", err)
	}
	if lv := levelOf(b); lv != 2 {
		t.Fatalf("B level after move: got %d want 2", lv)
	}
	if lv := levelOf(c); lv != 3 {
		t.Fatalf("C level should cascade: got %d want 3", lv)
	}

	// Cycle: move A under C (C is now a descendant of A? no, C moved with B under D).
	// Build a clear cycle: move D under C -> D is ancestor of C now, so reject.
	if err := s.MoveCategory(uid, d, &c); err != ErrInvalidMove {
		t.Fatalf("expected ErrInvalidMove (cycle), got %v", err)
	}
	// Self-move.
	if err := s.MoveCategory(uid, a, &a); err != ErrInvalidMove {
		t.Fatalf("expected ErrInvalidMove (self), got %v", err)
	}
	// Cross-direction: move A (expense) under E (income) -> reject.
	if err := s.MoveCategory(uid, a, &e); err != ErrInvalidMove {
		t.Fatalf("expected ErrInvalidMove (direction), got %v", err)
	}

	// Promote B back to a root (nil parent) -> level 1, C -> level 2.
	if err := s.MoveCategory(uid, b, nil); err != nil {
		t.Fatalf("promote B to root: %v", err)
	}
	if lv := levelOf(b); lv != 1 {
		t.Fatalf("B promoted level: got %d want 1", lv)
	}
	if lv := levelOf(c); lv != 2 {
		t.Fatalf("C level after promote: got %d want 2", lv)
	}
}

// TestMoveCategoryDepthLimit: moving a subtree that would push a descendant
// past level 5 is rejected.
func TestMoveCategoryDepthLimit(t *testing.T) {
	s, uid := newTestStore(t)
	// Deep chain L1..L4 (root .. level 4).
	l1 := mkcat(t, s, uid, nil, "L1", -1, 0, "")
	l2 := mkcat(t, s, uid, &l1, "L2", -1, 0, "")
	l3 := mkcat(t, s, uid, &l2, "L3", -1, 0, "")
	_ = mkcat(t, s, uid, &l3, "L4", -1, 0, "")
	// Another chain M1 -> M2 (root, level 2).
	m1 := mkcat(t, s, uid, nil, "M1", -1, 0, "")
	m2 := mkcat(t, s, uid, &m1, "M2", -1, 0, "")
	// Move L1 (height 4) under M2 (level 2) -> deepest would be 2+4=6 > 5.
	if err := s.MoveCategory(uid, l1, &m2); err != ErrMaxDepth {
		t.Fatalf("expected ErrMaxDepth, got %v", err)
	}
}

// TestUpdateEntryCategoryCrossUser: user B cannot touch user A's entry.
func TestUpdateEntryCategoryCrossUser(t *testing.T) {
	s, uidA := newTestStore(t)
	bID, err := s.CreateUser("bob", "secret123")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	entryA := mkentry(t, s, uidA, -100, "a's entry")
	bCat := mkcat(t, s, bID, nil, "bob餐饮", -1, 0, "")
	// Bob tries to reclassify Alice's entry -> not found (scoped by user_id).
	if err := s.UpdateEntryCategory(bID, entryA, &bCat); err != ErrEntryNotFound {
		t.Fatalf("cross-user must be ErrEntryNotFound, got %v", err)
	}
}
