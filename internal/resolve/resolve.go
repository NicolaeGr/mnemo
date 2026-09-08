// Package resolve decides which of a user's books a caller can see. The
// decision is pure (Compute); Resolver loads the inputs from the scoped store,
// and callers cache the result per request.
package resolve

import (
	"context"

	"github.com/nicolaegr/mnemo/internal/store"
)

// Resolver loads and answers visibility questions against a scoped store.
type Resolver struct{}

// VisibleBooks returns, for each active book, whether the actor can see it on
// s. See Compute.
func (Resolver) VisibleBooks(ctx context.Context, s store.ScopedStore) (map[int64]bool, error) {
	active, err := s.ActiveBooks(ctx)
	if err != nil {
		return nil, err
	}
	principal, err := s.PrincipalInfo(ctx)
	if err != nil {
		return nil, err
	}
	return Compute(active, principal.Tier, principal.Overrides), nil
}

// Compute reports which of the active books a caller can see: visible to a
// tier-all caller (empty tier) or when the caller's tier is in the book's
// synced_tiers, and hidden again if an override disables it.
func Compute(active []store.Book, tier string, overrides map[int64]bool) map[int64]bool {
	visible := make(map[int64]bool, len(active))
	for _, b := range active {
		visible[b.ID] = tier == "" || hasTier(b.SyncedTiers, tier)
	}
	for id, enabled := range overrides {
		if !enabled {
			visible[id] = false
		}
	}
	return visible
}

func hasTier(tiers []string, want string) bool {
	for _, t := range tiers {
		if t == want {
			return true
		}
	}
	return false
}
