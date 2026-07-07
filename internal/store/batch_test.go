package store

import (
	"errors"
	"testing"
)

// TestBatchRecategorizeEntries: a bulk recategorize reassigns every owned id,
// derives direction from the new category, and reports no skips.
func TestBatchRecategorizeEntries(t *testing.T) {
	s, uid := newTestStore(t)
	food := mkcat(t, s, uid, nil, "餐饮", -1, 0, "")
	income := mkcat(t, s, uid, nil, "收入", 1, 0, "")

	// Three entries filed under 餐饮 (expense).
	e1 := mkentry(t, s, uid, 1000, "早餐")
	e2 := mkentry(t, s, uid, 2000, "午餐")
	e3 := mkentry(t, s, uid, 3000, "晚餐")
	for _, id := range []int64{e1, e2, e3} {
		if err := s.UpdateEntryCategory(uid, id, &food); err != nil {
			t.Fatalf("seed category: %v", err)
		}
	}

	// Move all three to 收入 (income) in one batch.
	res, err := s.BatchRecategorizeEntries(uid, []int64{e1, e2, e3}, &income)
	if err != nil {
		t.Fatalf("batch recategorize: %v", err)
	}
	if res.Affected != 3 {
		t.Errorf("affected = %d, want 3", res.Affected)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("skipped = %v, want none", res.Skipped)
	}
	// Direction must now be derived from the income category.
	if e := findEntry(t, s, uid, e1); e == nil || e.Direction != 1 {
		t.Errorf("entry %d direction not switched to income", e1)
	}
}

// TestBatchRecategorizeUnclassify: passing a nil category clears the category
// on every id (direction back to 0 / 待分类).
func TestBatchRecategorizeUnclassify(t *testing.T) {
	s, uid := newTestStore(t)
	food := mkcat(t, s, uid, nil, "餐饮", -1, 0, "")
	e1 := mkentry(t, s, uid, 1000, "早餐")
	if err := s.UpdateEntryCategory(uid, e1, &food); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	res, err := s.BatchRecategorizeEntries(uid, []int64{e1}, nil)
	if err != nil {
		t.Fatalf("batch unclassify: %v", err)
	}
	if res.Affected != 1 {
		t.Errorf("affected = %d, want 1", res.Affected)
	}
	if e := findEntry(t, s, uid, e1); e == nil || e.Direction != 0 || e.CategoryID != nil {
		t.Errorf("entry %d not unclassified", e1)
	}
}

// TestBatchRecategorizeCrossTenant: ids owned by another user are reported as
// skipped and never modified; only the caller's own ids are affected.
func TestBatchRecategorizeCrossTenant(t *testing.T) {
	s, uid := newTestStore(t)
	mine := mkentry(t, s, uid, 1000, "我的")
	food := mkcat(t, s, uid, nil, "餐饮", -1, 0, "")

	// A second user with their own entry.
	otherID, err := s.CreateUser("other", "password123")
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	theirs := mkentry(t, s, otherID, 500, "别人的")

	res, err := s.BatchRecategorizeEntries(uid, []int64{mine, theirs}, &food)
	if err != nil {
		t.Fatalf("batch recategorize: %v", err)
	}
	if res.Affected != 1 {
		t.Errorf("affected = %d, want 1 (only own entry)", res.Affected)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].ID != theirs {
		t.Errorf("skipped = %v, want the other user's id %d", res.Skipped, theirs)
	}
	// The other user's entry must be untouched.
	if e := findEntry(t, s, otherID, theirs); e == nil || e.CategoryID != nil {
		t.Errorf("cross-tenant entry %d was modified", theirs)
	}
}

// TestBatchRecategorizeBadCategory: a target category not owned by the user is
// rejected wholesale (no partial writes).
func TestBatchRecategorizeBadCategory(t *testing.T) {
	s, uid := newTestStore(t)
	e1 := mkentry(t, s, uid, 1000, "早餐")
	bad := int64(99999)
	_, err := s.BatchRecategorizeEntries(uid, []int64{e1}, &bad)
	if !errors.Is(err, ErrCategoryNotFound) {
		t.Errorf("err = %v, want ErrCategoryNotFound", err)
	}
}

// TestBatchRecategorizeTooMany: exceeding MaxBatchIDs is rejected up front.
func TestBatchRecategorizeTooMany(t *testing.T) {
	s, uid := newTestStore(t)
	ids := make([]int64, MaxBatchIDs+1)
	_, err := s.BatchRecategorizeEntries(uid, ids, nil)
	if !errors.Is(err, ErrTooManyItems) {
		t.Errorf("err = %v, want ErrTooManyItems", err)
	}
}

// TestBatchDeleteEntries: bulk soft-delete hides every owned id from lists and
// reports the affected count.
func TestBatchDeleteEntries(t *testing.T) {
	s, uid := newTestStore(t)
	e1 := mkentry(t, s, uid, 1000, "a")
	e2 := mkentry(t, s, uid, 2000, "b")
	res, err := s.BatchDeleteEntries(uid, []int64{e1, e2})
	if err != nil {
		t.Fatalf("batch delete: %v", err)
	}
	if res.Affected != 2 {
		t.Errorf("affected = %d, want 2", res.Affected)
	}
	if findEntry(t, s, uid, e1) != nil || findEntry(t, s, uid, e2) != nil {
		t.Error("deleted entries still visible in list")
	}
}

// TestBatchDeleteIdempotentAndCrossTenant: already-deleted ids and other users'
// ids are reported as skipped, not re-deleted.
func TestBatchDeleteIdempotentAndCrossTenant(t *testing.T) {
	s, uid := newTestStore(t)
	e1 := mkentry(t, s, uid, 1000, "a")
	if err := s.SoftDeleteEntry(uid, e1); err != nil {
		t.Fatalf("pre-delete: %v", err)
	}
	e2 := mkentry(t, s, uid, 2000, "b")
	res, err := s.BatchDeleteEntries(uid, []int64{e1, e2})
	if err != nil {
		t.Fatalf("batch delete: %v", err)
	}
	if res.Affected != 1 {
		t.Errorf("affected = %d, want 1 (e1 already deleted)", res.Affected)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].ID != e1 {
		t.Errorf("skipped = %v, want already-deleted id %d", res.Skipped, e1)
	}
}
