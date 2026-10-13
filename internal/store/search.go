package store

import (
	"context"
	"fmt"
	"strings"
)

// ContactSearch is a page of search results. Total is the number of matching
// contacts across every page, so the UI can show a count and a pager.
type ContactSearch struct {
	Contacts []Contact
	NextPage bool
	Total    int
}

// SearchParams bounds a contact search. Query is a small query language:
// whitespace-separated terms, each either a bare word (matched against every
// free field) or a specifier like name:Josh or org:"Acme Inc". A specifier takes
// ':' or '=' and tolerates spaces around it (name : Josh). BookID, when non-nil,
// restricts to a single tag. Page is 1-based. Sort is one of name, org, updated,
// created (default name); Dir is asc or desc. Rank, when set and the query is
// non-empty, orders results by relevance (name/org prefix matches first); callers
// set it only when the user has not chosen an explicit sort, so a chosen column
// always wins.
type SearchParams struct {
	Query  string
	BookID *int64
	Page   int
	Per    int
	Sort   string
	Dir    string
	Rank   bool
}

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

	terms := parseQuery(p.Query)

	conds := []string{"c.user_id = $1 AND c.deleted_at IS NULL"}
	args := []any{s.Actor.UserID}
	ph := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	if p.BookID != nil {
		conds = append(conds, fmt.Sprintf(
			"c.id IN (SELECT contact_id FROM contact_books WHERE book_id = %s)", ph(*p.BookID)))
	}
	for _, t := range terms {
		conds = append(conds, termConditions(t, ph)...)
	}

	order := sortExpr(p.Sort) + " " + sortDir(p.Dir)
	if p.Rank && len(terms) > 0 {
		if rank := rankExpr(terms, ph); rank != "" {
			order = rank + ", " + order
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
		strings.Join(conds, " AND "), order, ph(per+1), ph((page-1)*per))

	rows, err := s.Tx.Query(ctx, query, args...)
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
	return ContactSearch{Contacts: out, NextPage: next, Total: total}, nil
}

// termConditions renders one parsed term as SQL. A bare term (field "") matches
// across every free field with OR; a scoped term matches only its field. A term
// carrying no matchable value (e.g. number=<no digits>) contributes nothing.
func termConditions(t queryTerm, ph func(any) string) []string {
	if t.field != "" {
		f, ok := lookupField(t.field)
		if !ok {
			return nil
		}
		if expr := fieldMatch(f, t.value, ph); expr != "" {
			return []string{expr}
		}
		return nil
	}
	var or []string
	for _, f := range contactFields {
		if !f.free {
			continue
		}
		if expr := fieldMatch(f, t.value, ph); expr != "" {
			or = append(or, expr)
		}
	}
	if len(or) == 0 {
		return nil
	}
	return []string{"(" + strings.Join(or, " OR ") + ")"}
}

func fieldMatch(f contactField, value string, ph func(any) string) string {
	if f.prepare != nil {
		value = f.prepare(value)
		if value == "" {
			return ""
		}
	}
	return f.match(value, ph)
}

// rankExpr orders a search by how strongly the first rankable term matches: a
// prefix match on the field outranks a substring match, which outranks the rest.
// Only name and org are rankable today; other terms still filter but leave the
// order to the chosen sort.
func rankExpr(terms []queryTerm, ph func(any) string) string {
	for _, t := range terms {
		anchor := fieldRankAnchor(t.field)
		if anchor == "" || strings.TrimSpace(t.value) == "" {
			continue
		}
		p := ph(strings.ToLower(t.value))
		return fmt.Sprintf(
			"CASE WHEN %s LIKE %s || '%%' THEN 0 WHEN %s LIKE '%%' || %s || '%%' THEN 1 ELSE 2 END",
			anchor, p, anchor, p)
	}
	return ""
}

func fieldRankAnchor(field string) string {
	if field == "" {
		return contactNameAnchor
	}
	if f, ok := lookupField(field); ok {
		return f.rank
	}
	return ""
}

const contactNameAnchor = "lower(coalesce(c.search_meta->>'fn', ''))"

// contactField is one specifier in the contact query language: it maps a key the
// user types onto a SQL predicate over a contacts column or a search_meta key.
// It is the single place to teach search about a new field: add an entry here
// (and, for a new search_meta key, extend vcard.SearchMeta and its indexes).
//
//   - free fields also take part in bare (unscoped) matching.
//   - prepare normalizes a raw value before matching (nil = identity).
//   - match renders a boolean SQL expression, binding its value through ph, or
//     "" to drop the term.
//   - rank is the lowercased text expression used to order prefix matches first,
//     empty when the field is not rankable.
//   - suggest fields appear in the UI's specifier autocomplete.
type contactField struct {
	key     string
	label   string
	free    bool
	suggest bool
	prepare func(string) string
	match   func(value string, ph func(any) string) string
	rank    string
}

var contactFields = []contactField{
	{
		key:     "name",
		label:   "Name",
		free:    true,
		suggest: true,
		rank:    contactNameAnchor,
		match: func(v string, ph func(any) string) string {
			p := ph(v)
			return fmt.Sprintf(
				"(c.search_meta->>'fn' ILIKE '%%' || %s || '%%' OR c.search_meta->>'n' ILIKE '%%' || %s || '%%')", p, p)
		},
	},
	{
		key:     "org",
		label:   "Organization",
		free:    true,
		suggest: true,
		rank:    "lower(coalesce(c.search_meta->>'org', ''))",
		match: func(v string, ph func(any) string) string {
			return fmt.Sprintf("c.search_meta->>'org' ILIKE '%%' || %s || '%%'", ph(v))
		},
	},
	{
		key:     "email",
		label:   "Email",
		free:    true,
		suggest: true,
		match: func(v string, ph func(any) string) string {
			return fmt.Sprintf(
				"EXISTS (SELECT 1 FROM jsonb_array_elements_text(c.search_meta->'emails') AS em WHERE em ILIKE '%%' || %s || '%%')",
				ph(v))
		},
	},
	{
		key:     "number",
		label:   "Phone",
		free:    true,
		suggest: true,
		prepare: phoneDigits,
		match: func(v string, ph func(any) string) string {
			return fmt.Sprintf(
				"EXISTS (SELECT 1 FROM jsonb_array_elements_text(c.search_meta->'tel_norm') AS tel WHERE tel ILIKE '%%' || %s || '%%')",
				ph(v))
		},
	},
	{
		key:   "has",
		label: "Has",
		match: func(v string, ph func(any) string) string {
			return fmt.Sprintf(
				"(CASE lower(%s) WHEN 'phone' THEN jsonb_array_length(coalesce(c.search_meta->'tels', '[]'::jsonb)) > 0 WHEN 'email' THEN jsonb_array_length(coalesce(c.search_meta->'emails', '[]'::jsonb)) > 0 ELSE true END)",
				ph(v))
		},
	},
	{
		key:   "untagged",
		label: "Untagged",
		match: func(string, func(any) string) string {
			return "NOT EXISTS (SELECT 1 FROM contact_books cb WHERE cb.contact_id = c.id)"
		},
	},
}

func lookupField(key string) (contactField, bool) {
	for _, f := range contactFields {
		if f.key == key {
			return f, true
		}
	}
	return contactField{}, false
}

// ContactFieldOption is a suggestable search specifier for the UI. It mirrors a
// contactField, so a new field appears in the search box with no extra wiring.
type ContactFieldOption struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// ContactSearchFields lists the specifiers the search box offers as completions.
func ContactSearchFields() []ContactFieldOption {
	out := make([]ContactFieldOption, 0, len(contactFields))
	for _, f := range contactFields {
		if f.suggest {
			out = append(out, ContactFieldOption{Key: f.key, Label: f.label})
		}
	}
	return out
}

// queryTerm is one parsed search token: a bare value (field "") matched against
// every free field, or a field-scoped value like name:Josh.
type queryTerm struct {
	field string
	value string
}

// parseQuery splits a raw query into terms. It understands field:value (or
// field=value), field:"quoted value", and bare words or quoted free text. The
// separator may be surrounded by spaces, since a key is only treated as a
// specifier when a separator actually follows it. An unknown key is not a
// specifier, so a stray "=" or ":" keeps the whole token as free text rather
// than swallowing the search.
func parseQuery(raw string) []queryTerm {
	var terms []queryTerm
	for i, n := 0, len(raw); i < n; {
		if isQuerySpace(raw[i]) {
			i++
			continue
		}
		if raw[i] == '"' {
			value, next := readQuoted(raw, i)
			i = next
			if value != "" {
				terms = append(terms, queryTerm{value: value})
			}
			continue
		}
		start := i
		for i < n && !isQuerySpace(raw[i]) && raw[i] != '=' && raw[i] != ':' && raw[i] != '"' {
			i++
		}
		word := strings.ToLower(raw[start:i])
		if _, ok := lookupField(word); ok {
			if value, next, found := readSpecifier(raw, i); found {
				i = next
				if value != "" {
					terms = append(terms, queryTerm{field: word, value: value})
				}
				continue
			}
		}
		for i < n && !isQuerySpace(raw[i]) {
			i++
		}
		// A lone separator (e.g. between an unknown key and its value) is not a
		// search term of its own.
		if token := raw[start:i]; token != "" && token != ":" && token != "=" {
			terms = append(terms, queryTerm{value: token})
		}
	}
	return terms
}

// readSpecifier reads a ":value" or "=value" starting at i, tolerating spaces on
// either side of the separator. found is false when the next non-space byte is
// not a separator, so the caller keeps the word as free text.
func readSpecifier(s string, i int) (value string, next int, found bool) {
	j := i
	for j < len(s) && isQuerySpace(s[j]) {
		j++
	}
	if j >= len(s) || (s[j] != ':' && s[j] != '=') {
		return "", i, false
	}
	j++
	for j < len(s) && isQuerySpace(s[j]) {
		j++
	}
	value, j = readValue(s, j)
	return value, j, true
}

// readValue reads a specifier value at i: a quoted string, or a run of
// non-space characters.
func readValue(s string, i int) (string, int) {
	if i < len(s) && s[i] == '"' {
		return readQuoted(s, i)
	}
	start := i
	for i < len(s) && !isQuerySpace(s[i]) {
		i++
	}
	return s[start:i], i
}

// readQuoted reads a double-quoted string starting at the quote at i, returning
// the inner text and the index just past the closing quote (or end of string).
func readQuoted(s string, i int) (string, int) {
	j := i + 1
	for j < len(s) && s[j] != '"' {
		j++
	}
	value := s[i+1 : j]
	if j < len(s) {
		j++
	}
	return value, j
}

func isQuerySpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
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

func phoneDigits(q string) string {
	var b strings.Builder
	for i := 0; i < len(q); i++ {
		if c := q[i]; c >= '0' && c <= '9' {
			b.WriteByte(c)
		}
	}
	return b.String()
}
