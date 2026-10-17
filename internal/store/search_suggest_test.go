package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicolaegr/mnemo/internal/model"
	"github.com/nicolaegr/mnemo/internal/store"
)

func TestSuggestContacts(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()
	user, err := store.NewUsers(pool).Signup(ctx, "suggestuser", "suggest@example.com", "Suggest", "pw", 12)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	book := insertBook(t, ctx, pool, user.ID, "work", "{primary}")
	if _, err := pool.Exec(ctx,
		`UPDATE books SET display_name = 'Family Reunion' WHERE id = $1`, book.ID); err != nil {
		t.Fatalf("rename book: %v", err)
	}

	cases := []struct {
		name       string
		raw        string
		caret      int
		wantInsert string
		wantLabel  string
		wantStart  int
		wantEnd    int
	}{
		{"field values", "has", 3, "has:phone ", "Has a phone", 0, 3},
		{"field value prefix", "has:p", 5, "has:phone ", "Has a phone", 0, 5},
		{"field values include org", "has:o", 5, "has:org ", "Has an organization", 0, 5},
		{"list value", "in:Fam", 6, `in:"Family Reunion" `, `In "Family Reunion"`, 0, 6},
		{"negated after no", "no ", 3, "has:phone ", "Without a phone", 3, 3},
		{"negation replacement", "no", 2, "no has:phone ", "Without a phone", 0, 2},
		{"word wrap", "josh", 4, "name:josh ", `Name contains "josh"`, 0, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := suggest(t, ctx, pool, user.ID, tc.raw, tc.caret)
			item, ok := findInsert(items, tc.wantInsert)
			if !ok {
				t.Fatalf("suggestions = %#v, want insert %q", items, tc.wantInsert)
			}
			if item.Label != tc.wantLabel {
				t.Fatalf("label = %q, want %q", item.Label, tc.wantLabel)
			}
			if item.Start != tc.wantStart || item.End != tc.wantEnd {
				t.Fatalf("span = [%d,%d), want [%d,%d)", item.Start, item.End, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

func suggest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int64, raw string, caret int) []store.Suggestion {
	t.Helper()
	var out []store.Suggestion
	actor := model.Actor{UserID: userID, Username: "suggest"}
	err := store.WithTx(ctx, pool, actor, func(s store.ScopedStore) error {
		var err error
		out, err = s.SuggestContacts(ctx, raw, caret)
		return err
	})
	if err != nil {
		t.Fatalf("suggest %q: %v", raw, err)
	}
	return out
}

func findInsert(items []store.Suggestion, want string) (store.Suggestion, bool) {
	for _, item := range items {
		if strings.EqualFold(item.Insert, want) {
			return item, true
		}
	}
	return store.Suggestion{}, false
}
