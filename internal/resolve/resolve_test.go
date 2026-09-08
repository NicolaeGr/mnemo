package resolve

import (
	"testing"

	"github.com/nicolaegr/mnemo/internal/store"
)

func b(id int64, tiers ...string) store.Book {
	return store.Book{ID: id, SyncedTiers: tiers}
}

func TestCompute(t *testing.T) {
	active := []store.Book{
		b(1, "primary", "secondary"), // system book
		b(2, "secondary"),            // work book
		b(3, "primary"),              // family book
	}

	// Tier-all (password auth) sees every active book regardless of synced_tiers.
	if got := Compute(active, "", nil); !allTrue(got, 1, 2, 3) {
		t.Fatalf("tier-all = %v, want all visible", got)
	}

	// A principal only sees books whose synced_tiers contain its tier.
	if got := Compute(active, "archived", nil); !allFalse(got, 1, 2, 3) {
		t.Fatalf("archived principal should see nothing, got %v", got)
	}
	if got := Compute(active, "secondary", nil); !allTrue(got, 1, 2) || got[3] {
		t.Fatalf("secondary = %v, want books 1,2 only", got)
	}

	// Overrides only ever subtract: disabling book 2 hides it.
	if got := Compute(active, "secondary", map[int64]bool{2: false}); got[2] {
		t.Fatalf("book 2 should be hidden by override, got %v", got)
	}

	// An enabled override on an already-invisible book does not grant it.
	if got := Compute(active, "primary", map[int64]bool{2: true}); got[2] {
		t.Fatalf("enabled override must not grant book 2, got %v", got)
	}
}

func allTrue(m map[int64]bool, ids ...int64) bool {
	for _, id := range ids {
		if !m[id] {
			return false
		}
	}
	return true
}

func allFalse(m map[int64]bool, ids ...int64) bool {
	for _, id := range ids {
		if m[id] {
			return false
		}
	}
	return true
}
