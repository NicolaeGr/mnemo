package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicolaegr/mnemo/internal/model"
	"github.com/nicolaegr/mnemo/internal/store"
)

// TestSearchSpecifiers exercises the field-scoped query language and the filter
// specifiers against a real index, all with the default name ordering.
func TestSearchSpecifiers(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()
	user, err := store.NewUsers(pool).Signup(ctx, "searchuser", "search@example.com", "Search", "pw", 12)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}

	base := insertBook(t, ctx, pool, user.ID, "base", "{primary}")
	a := insertSearchContact(t, ctx, pool, user.ID, "a.vcf",
		searchMetaJSON("Alice Adams", "Acme", []string{"alice@acme.com"}, "123", "123"), base.ID)
	b := insertSearchContact(t, ctx, pool, user.ID, "b.vcf",
		searchMetaJSON("Bob Brown", "Beta", nil, "", ""))
	c := insertSearchContact(t, ctx, pool, user.ID, "c.vcf",
		searchMetaJSON("Carol Diaz", "Acme Labs", []string{"carol@gamma.com"}, "399 123", "399123"))

	cases := []struct {
		name  string
		query string
		want  []int64
	}{
		{"scoped name", "name:alice", []int64{a}},
		{"scoped org", "org=acme", []int64{a, c}},
		{"scoped email", "email:gamma", []int64{c}},
		{"scoped number", "number=399", []int64{c}},
		{"lenient spaces", "org : Acme", []int64{a, c}},
		{"has phone", "has:phone", []int64{a, c}},
		{"has email", "has=email", []int64{a, c}},
		{"untagged", "untagged:1", []int64{b, c}},
		{"free text spans fields", "acme", []int64{a, c}},
		{"unknown specifier stays free text", "foo=bar", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := searchIDs(t, ctx, pool, user.ID, store.SearchParams{Query: tc.query, Per: 50})
			assertIDs(t, got, tc.want)
		})
	}
}

// TestSearchRanking checks that relevance ordering leads only when asked: a name
// prefix outranks a substring match, but an explicit sort still wins.
func TestSearchRanking(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()
	user, err := store.NewUsers(pool).Signup(ctx, "rankuser", "rank@example.com", "Rank", "pw", 12)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	arab := insertSearchContact(t, ctx, pool, user.ID, "arab.vcf", searchMetaJSON("Arab Spring", "", nil, "", ""))
	rashid := insertSearchContact(t, ctx, pool, user.ID, "rashid.vcf", searchMetaJSON("Rashid Khan", "", nil, "", ""))

	// "ra" prefixes Rashid, not Arab; ranking puts the prefix match first even
	// though Arab sorts earlier by name.
	got := searchIDs(t, ctx, pool, user.ID, store.SearchParams{Query: "ra", Rank: true, Per: 50})
	assertIDs(t, got, []int64{rashid, arab})

	// An explicit sort ignores ranking.
	got = searchIDs(t, ctx, pool, user.ID, store.SearchParams{Query: "ra", Sort: "name", Dir: "asc", Per: 50})
	assertIDs(t, got, []int64{arab, rashid})
}

func searchIDs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int64, p store.SearchParams) []int64 {
	t.Helper()
	var ids []int64
	actor := model.Actor{UserID: userID, Username: "search"}
	err := store.WithTx(ctx, pool, actor, func(s store.ScopedStore) error {
		res, err := s.Search(ctx, p)
		if err != nil {
			return err
		}
		for _, c := range res.Contacts {
			ids = append(ids, c.ID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	return ids
}

func insertSearchContact(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int64, filename, meta string, books ...int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO contacts (user_id, filename, uid, vcard_text, search_meta, etag)
		VALUES ($1, $2, $2, 'BEGIN:VCARD\r\nEND:VCARD', $3::jsonb, 'etag')
		RETURNING id`, userID, filename, meta).Scan(&id); err != nil {
		t.Fatalf("insert contact %s: %v", filename, err)
	}
	for _, book := range books {
		if _, err := pool.Exec(ctx,
			`INSERT INTO contact_books (contact_id, book_id) VALUES ($1, $2)`, id, book); err != nil {
			t.Fatalf("tag contact: %v", err)
		}
	}
	return id
}

func searchMetaJSON(fn, org string, emails []string, telRaw, telNorm string) string {
	tels := []map[string]string{}
	norms := []string{}
	if telNorm != "" {
		tels = append(tels, map[string]string{"raw": telRaw, "norm": telNorm})
		norms = append(norms, telNorm)
	}
	if emails == nil {
		emails = []string{}
	}
	b, err := json.Marshal(map[string]any{
		"fn": fn, "n": fn, "org": org, "emails": emails, "tels": tels, "tel_norm": norms,
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func assertIDs(t *testing.T, got, want []int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ids = %v, want %v", got, want)
		}
	}
}
