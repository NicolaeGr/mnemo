package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/nicolaegr/mnemo/internal/search"
)

// ContactSearch is a page of search results. Total is the number of matching
// contacts across every page, so the UI can show a count and a pager. Warnings
// describe parts of the query that could not be understood; the search still
// runs against the rest.
type ContactSearch struct {
	Contacts    []Contact
	NextPage    bool
	Total       int
	Warnings    []string
	Description string // human-readable rendering of the parsed query
}

// SearchParams bounds a contact search. Query is the contact query language:
// free words and field terms (`name:"Josh Smith"`, `has:phone`, `in:Work`,
// `no has:email`, `updated>30d`), combined with implicit AND, `or`, `not`, and
// parentheses. BookID, when non-nil, restricts to a single list (kept for the
// REST API; the admin UI uses in: instead). Page is 1-based. Sort is one of
// name, org, updated, created (default name); Dir is asc or desc. Rank, when set
// and the query is non-empty, orders results by relevance; callers set it only
// when the user has not chosen an explicit sort.
type SearchParams struct {
	Query  string
	BookID *int64
	Page   int
	Per    int
	Sort   string
	Dir    string
	Rank   bool
}

// contactSchema is the single place that describes what contact search knows.
// SQL storage is mapped separately in search_compile.go, so adding a field here
// teaches the parser and the suggestion engine without any SQL strings.
var contactSchema = search.NewSchema(
	search.Field{
		Key:         "name",
		Label:       "Name",
		Type:        search.TypeText,
		Free:        true,
		Weight:      3,
		Example:     `name:"Josh Smith"`,
		Description: "contact name",
	},
	search.Field{
		Key:         "org",
		Label:       "Organization",
		Type:        search.TypeText,
		Free:        true,
		Weight:      2,
		Aliases:     []string{"organization", "company"},
		Example:     `org:acme`,
		Description: "organization",
	},
	search.Field{
		Key:         "email",
		Label:       "Email",
		Type:        search.TypeEmail,
		Free:        true,
		Weight:      1,
		Example:     `email:alice@example.com`,
		Description: "email address",
	},
	search.Field{
		Key:         "number",
		Label:       "Phone",
		Type:        search.TypePhone,
		Free:        true,
		Weight:      1,
		Aliases:     []string{"phone", "tel"},
		Example:     `number:"+1 555 123 4567"`,
		Description: "phone number",
		Normalize:   phoneValue,
	},
	search.Field{
		Key:         "has",
		Label:       "Has",
		Type:        search.TypeEnum,
		Values:      []string{"phone", "email", "org"},
		Example:     "has:phone",
		Description: "has a phone, email, or organization",
	},
	search.Field{
		Key:         "untagged",
		Label:       "Untagged",
		Type:        search.TypeBool,
		Example:     "untagged:true",
		Description: "not in any list",
	},
	search.Field{
		Key:         "in",
		Label:       "List",
		Type:        search.TypeList,
		Aliases:     []string{"list"},
		Example:     `in:"Work"`,
		Description: "in a list",
	},
	search.Field{
		Key:         "created",
		Label:       "Created",
		Type:        search.TypeDate,
		Example:     "created>30d",
		Description: "date created",
	},
	search.Field{
		Key:         "updated",
		Label:       "Updated",
		Type:        search.TypeDate,
		Example:     "updated>30d",
		Description: "date updated",
	},
)

// Search returns live contacts matching the query, scoped by the actor's user
// and, optionally, one book, in the requested order.
func (s ScopedStore) Search(ctx context.Context, p SearchParams) (ContactSearch, error) {
	per := p.Per
	if per <= 0 || per > 100 {
		per = 50
	}
	page := p.Page
	if page < 1 {
		page = 1
	}

	parsed := search.Parse(p.Query, contactSchema)
	planner := &planner{ctx: ctx, store: s}
	expr, err := planner.plan(parsed.Expr)
	if err != nil {
		return ContactSearch{}, err
	}
	warnings := append(warningMessages(parsed.Warnings), planner.warns...)
	warnings = dedupeStrings(warnings)
	description := search.Describe(expr, contactSchema, planner.listPhrase)

	b := &sqlBuilder{}
	conds := []string{"c.user_id = " + b.arg(s.Actor.UserID) + " AND c.deleted_at IS NULL"}
	if p.BookID != nil {
		conds = append(conds, "c.id IN (SELECT contact_id FROM contact_books WHERE book_id = "+b.arg(*p.BookID)+")")
	}
	if expr != nil {
		cond, err := b.compile(expr)
		if err != nil {
			return ContactSearch{}, err
		}
		if cond != "" {
			conds = append(conds, cond)
		}
	}

	order := sortExpr(p.Sort) + " " + sortDir(p.Dir)
	if p.Rank && expr != nil {
		if score := b.rankScore(expr); score != "" {
			order = score + ", " + order
		}
	}

	query := fmt.Sprintf(`
		SELECT id, filename, vcard_text, uid, search_meta, etag,
		       modified_by, deleted_at, created_at, updated_at,
		       count(*) OVER() AS total
		  FROM contacts c
		 WHERE %s
		 ORDER BY %s, c.filename
		 LIMIT %s OFFSET %s`,
		strings.Join(conds, " AND "), order, b.arg(per+1), b.arg((page-1)*per))

	rows, err := s.Tx.Query(ctx, query, b.args...)
	if err != nil {
		return ContactSearch{}, err
	}
	defer rows.Close()

	out := make([]Contact, 0, per)
	total := 0
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.ID, &c.Filename, &c.VCardText, &c.UID, &c.SearchMeta,
			&c.ETag, &c.ModifiedBy, &c.DeletedAt, &c.CreatedAt, &c.UpdatedAt, &total); err != nil {
			return ContactSearch{}, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return ContactSearch{}, err
	}

	next := false
	if len(out) > per {
		out = out[:per]
		next = true
	}
	return ContactSearch{Contacts: out, NextPage: next, Total: total, Warnings: warnings, Description: description}, nil
}

func warningMessages(ws []search.Warning) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.Message)
	}
	return out
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// sortExpr maps a UI sort key onto a safe SQL expression. Unknown keys fall back
// to the display name, so a bad query string can never reach SQL as a column.
func sortExpr(key string) string {
	switch key {
	case "org":
		return "lower(coalesce(c.search_meta->>'org', ''))"
	case "updated":
		return "c.updated_at"
	case "created":
		return "c.created_at"
	default:
		return contactNameAnchor
	}
}

func sortDir(dir string) string {
	if strings.EqualFold(dir, "desc") {
		return "DESC"
	}
	return "ASC"
}

const contactNameAnchor = "lower(coalesce(c.search_meta->>'fn', ''))"
