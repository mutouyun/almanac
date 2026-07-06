// Routing engine: given a signed amount and a raw description, find the
// category a webhook entry should be filed under, using the user's category
// tree as an ordered set of regex rules.
//
// Design (see docs/ledger_tech_design.md §4):
//   - Rules are compiled per user and cached (map[userID]*compiledRuleSet).
//   - All categories (both directions) are merged into ONE ordered list and
//     matched in a single pass: amount sign no longer pre-locks a direction.
//     Rules are ordered by level DESC, sort_order ASC, id ASC, so the most
//     specific (deepest) category wins; first match stops. The matched
//     category's direction becomes the entry's direction.
//   - Bare keywords act as "contains" matches because Go regexp is unanchored;
//     users write ^...$ for exact matches. A category with no regex defaults to
//     an exact, case-sensitive full match on its own name (^QuoteMeta(name)$),
//     so every category participates in routing.
//   - Any category mutation invalidates that user's cache; the next
//     classification lazily rebuilds it.
package store

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
)

// validateRegex compiles a category regex to reject invalid patterns at save
// time. Empty is allowed (means "no auto-routing rule"). Returns ErrInvalidRegex
// on a compile failure.
func validateRegex(pattern string) error {
	if pattern == "" {
		return nil
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return ErrInvalidRegex
	}
	return nil
}

// compiledRule is one category's precompiled matcher plus its ordering keys.
type compiledRule struct {
	categoryID int64
	re         *regexp.Regexp
	level      int
	sortOrder  int
}

// compiledRuleSet holds a user's rules merged into ONE list, pre-sorted by
// match priority (level DESC, sort_order ASC, id ASC). Routing matches across
// both directions in a single pass; the matched category's own direction
// determines the entry's direction. It also carries a byID snapshot of the
// user's full category tree (all nodes, not just those with a regex) so path
// derivation (餐饮>饮品>咖啡) can reuse the exact same cached snapshot — same
// source, same invalidation, no N+1 queries.
type compiledRuleSet struct {
	rules []compiledRule // all categories, both directions, priority-sorted
	byID  map[int64]catNode
}

// catNode is the minimal category info needed to walk a path up to the root.
type catNode struct {
	name     string
	parentID *int64
}

// CategoryPath returns the full ">"-joined path for a category id using the
// user's cached tree snapshot (e.g. "餐饮>饮品>咖啡"). Returns "" when the id is
// unknown. A defensive depth cap (levels are <= 5 by schema) guards against
// any accidental cycle.
func (s *Store) CategoryPath(userID, categoryID int64) (string, error) {
	set, err := s.rulesFor(userID)
	if err != nil {
		return "", err
	}
	return set.pathOf(categoryID), nil
}

// pathOf walks parent links from the given id up to the root, joining names
// with ">". Empty string when the id is not in the snapshot.
func (set *compiledRuleSet) pathOf(categoryID int64) string {
	node, ok := set.byID[categoryID]
	if !ok {
		return ""
	}
	parts := []string{node.name}
	cur := node.parentID
	for i := 0; i < 8 && cur != nil; i++ {
		p, ok := set.byID[*cur]
		if !ok {
			break
		}
		parts = append(parts, p.name)
		cur = p.parentID
	}
	// parts is leaf->root; reverse to root->leaf.
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, ">")
}

// RouteEntry is the resilient wrapper used by the webhook ingestion path: it
// classifies the entry and, on any routing error (e.g. a transient DB read),
// logs and returns nil so the entry is still recorded as unclassified rather
// than failing ingestion. Prefer ClassifyEntry when the caller wants the error.
func (s *Store) RouteEntry(userID int64, amountCents int64, rawType string) *int64 {
	id, err := s.ClassifyEntry(userID, amountCents, rawType)
	if err != nil {
		log.Printf("routing: classify failed for user %d: %v", userID, err)
		return nil
	}
	return id
}

// InvalidateRules drops the cached rule set for a user. Call after any create,
// update, or delete of that user's categories. The next ClassifyEntry rebuilds
// lazily. Cheap and safe to call even if nothing is cached.
func (s *Store) InvalidateRules(userID int64) {
	s.rulesMu.Lock()
	delete(s.rules, userID)
	s.rulesMu.Unlock()
}

// ClassifyEntry returns the category_id a webhook entry should be filed under,
// or nil (unclassified) when no rule matches. Matching spans BOTH directions in
// one merged pass; the matched category's direction becomes the entry's
// direction. amountCents is only used as a zero-amount guard (a zero amount is
// rejected upstream and never reaches here; defensively it returns nil).
func (s *Store) ClassifyEntry(userID int64, amountCents int64, rawType string) (*int64, error) {
	if amountCents == 0 {
		return nil, nil
	}
	set, err := s.rulesFor(userID)
	if err != nil {
		return nil, err
	}
	// Trim so a webhook description with leading/trailing spaces can still
	// satisfy an exact full-match rule (^name$).
	raw := strings.TrimSpace(rawType)
	for i := range set.rules {
		if set.rules[i].re.MatchString(raw) {
			id := set.rules[i].categoryID
			return &id, nil
		}
	}
	return nil, nil
}

// rulesFor returns the user's compiled rule set, building and caching it on a
// miss. Uses a read lock for the fast path and a write lock only to install a
// freshly built set.
func (s *Store) rulesFor(userID int64) (*compiledRuleSet, error) {
	s.rulesMu.RLock()
	set, ok := s.rules[userID]
	s.rulesMu.RUnlock()
	if ok {
		return set, nil
	}

	set, err := s.buildRuleSet(userID)
	if err != nil {
		return nil, err
	}
	s.rulesMu.Lock()
	s.rules[userID] = set
	s.rulesMu.Unlock()
	return set, nil
}

// buildRuleSet loads the user's categories, compiles each into a matcher, and
// returns them split by direction and sorted by match priority. A category with
// a non-empty regex uses that pattern; one with an empty regex falls back to an
// exact full match on its name (^QuoteMeta(name)$). A category whose pattern
// fails to compile is skipped (save-time validation already rejects bad
// user-supplied patterns; this is a defensive fallback) so one bad rule can't
// disable the whole set.
func (s *Store) buildRuleSet(userID int64) (*compiledRuleSet, error) {
	cats, err := s.ListCategories(userID)
	if err != nil {
		return nil, fmt.Errorf("load categories for routing: %w", err)
	}
	set := &compiledRuleSet{byID: make(map[int64]catNode, len(cats))}
	for _, c := range cats {
		set.byID[c.ID] = catNode{name: c.Name, parentID: c.ParentID}
		// A category without a regex defaults to an exact, case-sensitive
		// full match on its own name. QuoteMeta escapes any regex metachars
		// in the name (e.g. "生活/娱乐", "C++") so anchoring can't break.
		pattern := c.Regex
		if pattern == "" {
			pattern = "^" + regexp.QuoteMeta(c.Name) + "$"
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue // skip invalid rule, keep the rest usable
		}
		rule := compiledRule{
			categoryID: c.ID,
			re:         re,
			level:      c.Level,
			sortOrder:  c.SortOrder,
		}
		set.rules = append(set.rules, rule)
	}
	sortRules(set.rules)
	return set, nil
}

// sortRules orders rules by match priority: deeper level first (more specific
// wins), then sort_order ascending (user drag order), then category id
// ascending (stable, reproducible tiebreak).
func sortRules(rules []compiledRule) {
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].level != rules[j].level {
			return rules[i].level > rules[j].level
		}
		if rules[i].sortOrder != rules[j].sortOrder {
			return rules[i].sortOrder < rules[j].sortOrder
		}
		return rules[i].categoryID < rules[j].categoryID
	})
}
