package store

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nicolaegr/mnemo/internal/search"
)

// planner validates and types a parsed query against the contact schema. It is
// the only part that needs the database, because list names resolve to ids.
type planner struct {
	ctx    context.Context
	store  ScopedStore
	warns  []string
	books  []Book
	loaded bool
}

func (p *planner) warnf(format string, args ...any) {
	p.warns = append(p.warns, fmt.Sprintf(format, args...))
}

func (p *planner) plan(e search.Expr) (search.Expr, error) {
	switch v := e.(type) {
	case nil:
		return nil, nil
	case search.And, search.Or:
		var kids []search.Expr
		var src []search.Expr
		switch x := v.(type) {
		case search.And:
			src = x.Kids
		case search.Or:
			src = x.Kids
		}
		for _, kid := range src {
			planned, err := p.plan(kid)
			if err != nil {
				return nil, err
			}
			if planned != nil {
				kids = append(kids, planned)
			}
		}
		switch len(kids) {
		case 0:
			return nil, nil
		case 1:
			return kids[0], nil
		default:
			if _, ok := v.(search.And); ok {
				return search.And{Kids: kids}, nil
			}
			return search.Or{Kids: kids}, nil
		}
	case search.Not:
		kid, err := p.plan(v.Kid)
		if err != nil || kid == nil {
			return nil, err
		}
		return search.Not{Kid: kid}, nil
	case search.Term:
		return p.term(v)
	default:
		return nil, fmt.Errorf("search: unsupported expression %T", e)
	}
}

func (p *planner) term(t search.Term) (search.Expr, error) {
	if t.Field == "" {
		text, ok := t.Value.(search.Text)
		if !ok {
			return nil, nil
		}
		text = search.Text(strings.TrimSpace(string(text)))
		if text == "" {
			return nil, nil
		}
		if t.Op != search.OpContains && t.Op != search.OpPrefix {
			return nil, nil
		}
		return search.Term{Op: t.Op, Value: text}, nil
	}

	f, ok := contactSchema.Lookup(t.Field)
	if !ok {
		return nil, nil
	}
	text, _ := t.Value.(search.Text)
	raw := strings.TrimSpace(string(text))

	switch f.Type {
	case search.TypeText, search.TypeEmail:
		if t.Op == search.OpExists {
			return search.Term{Field: f.Key, Op: search.OpExists, Value: search.Text("")}, nil
		}
		if !oneOf(t.Op, search.OpContains, search.OpPrefix, search.OpEqual) {
			p.warnf("%s does not support %s", f.Key, t.Op)
			return nil, nil
		}
		if raw == "" {
			p.warnf("missing value for %s", f.Key)
			return nil, nil
		}
		return search.Term{Field: f.Key, Op: t.Op, Value: search.Text(strings.ToLower(raw))}, nil

	case search.TypePhone:
		if t.Op == search.OpExists {
			return search.Term{Field: f.Key, Op: search.OpExists, Value: search.Text("")}, nil
		}
		if !oneOf(t.Op, search.OpContains, search.OpPrefix, search.OpEqual) {
			p.warnf("%s does not support %s", f.Key, t.Op)
			return nil, nil
		}
		normalize := f.Normalize
		if normalize == nil {
			normalize = phoneValue
		}
		digits, ok := normalize(raw)
		if !ok {
			p.warnf("%q has no phone digits", raw)
			return nil, nil
		}
		return search.Term{Field: f.Key, Op: t.Op, Value: search.Text(digits)}, nil

	case search.TypeEnum:
		if t.Op == search.OpExists {
			return search.Term{Field: f.Key, Op: search.OpExists, Value: search.Enum("")}, nil
		}
		if !oneOf(t.Op, search.OpContains, search.OpEqual) {
			p.warnf("%s does not support %s", f.Key, t.Op)
			return nil, nil
		}
		val := strings.ToLower(raw)
		if !containsString(f.Values, val) {
			p.warnf("%s is not a valid %s value; try %s", raw, f.Key, strings.Join(f.Values, " or "))
			return nil, nil
		}
		return search.Term{Field: f.Key, Op: search.OpEqual, Value: search.Enum(val)}, nil

	case search.TypeBool:
		if t.Op == search.OpExists {
			return search.Term{Field: f.Key, Op: search.OpEqual, Value: search.Bool(true)}, nil
		}
		if !oneOf(t.Op, search.OpContains, search.OpEqual) {
			p.warnf("%s does not support %s", f.Key, t.Op)
			return nil, nil
		}
		val, ok := parseBool(raw)
		if !ok {
			p.warnf("%q is not true or false", raw)
			return nil, nil
		}
		return search.Term{Field: f.Key, Op: search.OpEqual, Value: search.Bool(val)}, nil

	case search.TypeList:
		if t.Op == search.OpExists {
			return search.Term{Field: f.Key, Op: search.OpExists, Value: search.IDs(nil)}, nil
		}
		if !oneOf(t.Op, search.OpContains, search.OpEqual, search.OpPrefix) {
			p.warnf("%s does not support %s", f.Key, t.Op)
			return nil, nil
		}
		ids, err := p.resolveLists(raw, t.Op)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return nil, nil
		}
		return search.Term{Field: f.Key, Op: search.OpEqual, Value: search.IDs(ids)}, nil

	case search.TypeDate:
		if t.Op == search.OpExists {
			return search.Term{Field: f.Key, Op: search.OpExists, Value: search.Time{}}, nil
		}
		if !oneOf(t.Op, search.OpContains, search.OpEqual,
			search.OpGt, search.OpGe, search.OpLt, search.OpLe) {
			p.warnf("%s does not support %s", f.Key, t.Op)
			return nil, nil
		}
		val, ok := parseDateValue(raw, t.Op)
		if !ok {
			p.warnf("invalid date %q for %s (use 7d, 30d, 2024-01-01, or RFC3339)", raw, f.Key)
			return nil, nil
		}
		return search.Term{Field: f.Key, Op: t.Op, Value: val}, nil
	}
	return nil, nil
}

func (p *planner) resolveLists(raw string, op search.Op) ([]int64, error) {
	books, err := p.listBooks()
	if err != nil {
		return nil, err
	}
	if raw == "" {
		p.warnf("missing list name")
		return nil, nil
	}
	if strings.HasPrefix(raw, "#") {
		id, err := strconv.ParseInt(strings.TrimPrefix(raw, "#"), 10, 64)
		if err != nil {
			p.warnf("%q is not a list id", raw)
			return nil, nil
		}
		for _, b := range books {
			if b.ID == id {
				return []int64{id}, nil
			}
		}
		p.warnf("no list with id %d", id)
		return nil, nil
	}

	lower := strings.ToLower(raw)
	var ids []int64
	for _, b := range books {
		name := strings.ToLower(b.DisplayName)
		slug := strings.ToLower(b.Slug)
		var hit bool
		if op == search.OpPrefix {
			hit = strings.HasPrefix(name, lower) || strings.HasPrefix(slug, lower)
		} else {
			hit = name == lower || slug == lower
		}
		if hit {
			ids = append(ids, b.ID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	ids = dedupeIDs(ids)
	if len(ids) == 0 {
		p.warnf("no list named %q", raw)
		return nil, nil
	}
	if len(ids) > 1 && op != search.OpPrefix {
		p.warnf("%q matches %d lists; matching all of them", raw, len(ids))
	}
	return ids, nil
}

func (p *planner) listBooks() ([]Book, error) {
	if p.loaded {
		return p.books, nil
	}
	books, err := p.store.ListBooks(p.ctx)
	if err != nil {
		return nil, err
	}
	p.books, p.loaded = books, true
	return books, nil
}

// listPhrase renders resolved ids as a human phrase for the query description.
func (p *planner) listPhrase(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		for _, b := range p.books {
			if b.ID == id {
				names = append(names, b.DisplayName)
				break
			}
		}
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return "the list " + names[0]
	default:
		return "the lists " + humanJoin(names)
	}
}

func humanJoin(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

func dedupeIDs(ids []int64) []int64 {
	if len(ids) < 2 {
		return ids
	}
	out := ids[:1]
	for _, id := range ids[1:] {
		if id != out[len(out)-1] {
			out = append(out, id)
		}
	}
	return out
}

func oneOf[T comparable](v T, allowed ...T) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func phoneValue(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			b.WriteByte(s[i])
		}
	}
	return b.String(), b.Len() > 0
}

func parseBool(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "on":
		return true, true
	case "false", "0", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func parseDateValue(raw string, op search.Op) (search.Value, bool) {
	s := strings.TrimSpace(raw)
	now := time.Now().UTC()
	if d, ok := parseRelativeDuration(s); ok {
		t := now.Add(-d)
		if op == search.OpContains || op == search.OpEqual {
			return search.TimeRange{From: t, To: now}, true
		}
		return search.Time(t), true
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.UTC); err == nil {
		if op == search.OpContains || op == search.OpEqual {
			return search.TimeRange{From: t, To: t.Add(24 * time.Hour)}, true
		}
		return search.Time(t), true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return search.Time(t), true
	}
	return nil, false
}

func parseRelativeDuration(s string) (time.Duration, bool) {
	if len(s) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 0, false
	}
	switch s[len(s)-1] {
	case 'h':
		return time.Duration(n) * time.Hour, true
	case 'd':
		return time.Duration(n) * 24 * time.Hour, true
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, true
	case 'm':
		return time.Duration(n) * 30 * 24 * time.Hour, true
	case 'y':
		return time.Duration(n) * 365 * 24 * time.Hour, true
	default:
		return 0, false
	}
}

// sqlSource compiles one field's typed predicate. Values arrive already
// validated, so each source only chooses the right SQL shape.
type sqlSource interface {
	compile(b *sqlBuilder, op search.Op, v search.Value) (string, error)
}

var contactSources = map[string]sqlSource{
	"name":     textSource{exprs: []string{"lower(coalesce(c.search_meta->>'fn', ''))", "lower(coalesce(c.search_meta->>'n', ''))"}},
	"org":      textSource{exprs: []string{"lower(coalesce(c.search_meta->>'org', ''))"}},
	"email":    arraySource{key: "emails"},
	"number":   arraySource{key: "tel_norm", phone: true},
	"has":      hasSource{},
	"untagged": untaggedSource{},
	"in":       listSource{},
	"created":  dateSource{expr: "c.created_at"},
	"updated":  dateSource{expr: "c.updated_at"},
}

type sqlBuilder struct {
	args []any
}

func (b *sqlBuilder) arg(v any) string {
	b.args = append(b.args, v)
	return fmt.Sprintf("$%d", len(b.args))
}

func (b *sqlBuilder) compile(e search.Expr) (string, error) {
	switch v := e.(type) {
	case nil:
		return "", nil
	case search.And:
		return b.join(v.Kids, " AND ")
	case search.Or:
		return b.join(v.Kids, " OR ")
	case search.Not:
		kid, err := b.compile(v.Kid)
		if err != nil || kid == "" {
			return "", err
		}
		return "NOT (" + kid + ")", nil
	case search.Term:
		return b.term(v)
	default:
		return "", fmt.Errorf("search: unsupported expression %T", e)
	}
}

func (b *sqlBuilder) join(kids []search.Expr, sep string) (string, error) {
	parts := make([]string, 0, len(kids))
	for _, kid := range kids {
		part, err := b.compile(kid)
		if err != nil {
			return "", err
		}
		if part != "" {
			parts = append(parts, part)
		}
	}
	switch len(parts) {
	case 0:
		return "", nil
	case 1:
		return parts[0], nil
	default:
		return "(" + strings.Join(parts, sep) + ")", nil
	}
}

func (b *sqlBuilder) term(t search.Term) (string, error) {
	if t.Field == "" {
		return b.free(t)
	}
	src, ok := contactSources[t.Field]
	if !ok {
		return "", nil
	}
	return src.compile(b, t.Op, t.Value)
}

func (b *sqlBuilder) free(t search.Term) (string, error) {
	text, ok := t.Value.(search.Text)
	if !ok {
		return "", nil
	}
	val := strings.TrimSpace(string(text))
	if val == "" {
		return "", nil
	}
	op := search.OpContains
	if t.Op == search.OpPrefix {
		op = search.OpPrefix
	}
	var parts []string
	for _, f := range contactSchema.Fields() {
		if !f.Free {
			continue
		}
		src, ok := contactSources[f.Key]
		if !ok {
			continue
		}
		cond, err := src.compile(b, op, search.Text(val))
		if err != nil {
			return "", err
		}
		if cond != "" {
			parts = append(parts, cond)
		}
	}
	switch len(parts) {
	case 0:
		return "", nil
	case 1:
		return parts[0], nil
	default:
		return "(" + strings.Join(parts, " OR ") + ")", nil
	}
}

type textSource struct{ exprs []string }

func (s textSource) compile(b *sqlBuilder, op search.Op, v search.Value) (string, error) {
	text, ok := v.(search.Text)
	if !ok {
		return "", nil
	}
	val := strings.ToLower(strings.TrimSpace(string(text)))
	var parts []string
	switch op {
	case search.OpExists:
		for _, expr := range s.exprs {
			parts = append(parts, expr+" <> ''")
		}
	case search.OpContains:
		if val == "" {
			return "", nil
		}
		p := b.arg(val)
		for _, expr := range s.exprs {
			parts = append(parts, "position("+p+" in "+expr+") > 0")
		}
	case search.OpPrefix:
		if val == "" {
			return "", nil
		}
		p := b.arg(val)
		for _, expr := range s.exprs {
			parts = append(parts, "left("+expr+", length("+p+")) = "+p)
		}
	case search.OpEqual:
		p := b.arg(val)
		for _, expr := range s.exprs {
			parts = append(parts, expr+" = "+p)
		}
	default:
		return "", nil
	}
	if len(parts) == 0 {
		return "", nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", nil
}

type arraySource struct {
	key   string
	phone bool
}

func (s arraySource) compile(b *sqlBuilder, op search.Op, v search.Value) (string, error) {
	text, ok := v.(search.Text)
	if !ok {
		return "", nil
	}
	val := strings.ToLower(strings.TrimSpace(string(text)))
	if s.phone {
		digits, ok := phoneValue(val)
		if !ok {
			return "", nil
		}
		val = digits
	}
	json := "c.search_meta->'" + s.key + "'"
	if op == search.OpExists {
		return "jsonb_array_length(coalesce(" + json + ", '[]'::jsonb)) > 0", nil
	}
	if val == "" {
		return "", nil
	}
	p := b.arg(val)
	var cond string
	switch op {
	case search.OpContains:
		cond = "position(" + p + " in lower(e)) > 0"
	case search.OpPrefix:
		cond = "left(lower(e), length(" + p + ")) = " + p
	case search.OpEqual:
		cond = "lower(e) = " + p
	default:
		return "", nil
	}
	return "EXISTS (SELECT 1 FROM jsonb_array_elements_text(" + json + ") AS e WHERE " + cond + ")", nil
}

type hasSource struct{}

func (hasSource) compile(_ *sqlBuilder, op search.Op, v search.Value) (string, error) {
	phone := "jsonb_array_length(coalesce(c.search_meta->'tels', '[]'::jsonb)) > 0"
	email := "jsonb_array_length(coalesce(c.search_meta->'emails', '[]'::jsonb)) > 0"
	org := "lower(coalesce(c.search_meta->>'org', '')) <> ''"
	if op == search.OpExists {
		return "(" + phone + " OR " + email + " OR " + org + ")", nil
	}
	val, _ := v.(search.Enum)
	if val == "" {
		if text, ok := v.(search.Text); ok {
			val = search.Enum(text)
		}
	}
	switch strings.ToLower(string(val)) {
	case "phone":
		return phone, nil
	case "email":
		return email, nil
	case "org":
		return org, nil
	default:
		return "", nil
	}
}

type untaggedSource struct{}

func (untaggedSource) compile(_ *sqlBuilder, op search.Op, v search.Value) (string, error) {
	val := true
	if b, ok := v.(search.Bool); ok {
		val = bool(b)
	}
	if op == search.OpExists {
		val = true
	}
	if val {
		return "NOT EXISTS (SELECT 1 FROM contact_books cb WHERE cb.contact_id = c.id)", nil
	}
	return "EXISTS (SELECT 1 FROM contact_books cb WHERE cb.contact_id = c.id)", nil
}

type listSource struct{}

func (listSource) compile(b *sqlBuilder, op search.Op, v search.Value) (string, error) {
	if op == search.OpExists {
		return "EXISTS (SELECT 1 FROM contact_books cb WHERE cb.contact_id = c.id)", nil
	}
	ids, ok := v.(search.IDs)
	if !ok || len(ids) == 0 {
		return "", nil
	}
	p := b.arg([]int64(ids))
	return "EXISTS (SELECT 1 FROM contact_books cb JOIN books bk ON bk.id = cb.book_id AND bk.owner_user_id = c.user_id WHERE cb.contact_id = c.id AND cb.book_id = ANY(" + p + "))", nil
}

type dateSource struct{ expr string }

func (s dateSource) compile(b *sqlBuilder, op search.Op, v search.Value) (string, error) {
	switch op {
	case search.OpExists:
		return "true", nil
	case search.OpGt, search.OpGe, search.OpLt, search.OpLe:
		t, ok := v.(search.Time)
		if !ok {
			return "", nil
		}
		return s.expr + " " + sqlOp(op) + " " + b.arg(time.Time(t)), nil
	case search.OpContains, search.OpEqual:
		switch x := v.(type) {
		case search.TimeRange:
			return "(" + s.expr + " >= " + b.arg(x.From) + " AND " + s.expr + " < " + b.arg(x.To) + ")", nil
		case search.Time:
			return s.expr + " = " + b.arg(time.Time(x)), nil
		}
	}
	return "", nil
}

func sqlOp(op search.Op) string {
	switch op {
	case search.OpGt:
		return ">"
	case search.OpGe:
		return ">="
	case search.OpLt:
		return "<"
	case search.OpLe:
		return "<="
	default:
		return "="
	}
}

func (b *sqlBuilder) rankScore(e search.Expr) string {
	var parts []string
	var walk func(search.Expr)
	walk = func(e search.Expr) {
		switch v := e.(type) {
		case search.And:
			for _, kid := range v.Kids {
				walk(kid)
			}
		case search.Or:
			for _, kid := range v.Kids {
				walk(kid)
			}
		case search.Not:
			// Negated terms filter; they must not reward a match.
		case search.Term:
			anchor := rankAnchor(v.Field)
			if anchor == "" {
				return
			}
			if v.Op != search.OpContains && v.Op != search.OpPrefix && v.Op != search.OpEqual {
				return
			}
			text, ok := v.Value.(search.Text)
			if !ok || strings.TrimSpace(string(text)) == "" {
				return
			}
			p := b.arg(strings.ToLower(string(text)))
			if v.Op == search.OpPrefix {
				parts = append(parts, "CASE WHEN left("+anchor+", length("+p+")) = "+p+" THEN 0 ELSE 1 END")
			} else {
				parts = append(parts, "CASE WHEN left("+anchor+", length("+p+")) = "+p+
					" THEN 0 WHEN position("+p+" in "+anchor+") > 0 THEN 1 ELSE 2 END")
			}
		}
	}
	walk(e)
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, " + ") + ")"
}

func rankAnchor(field string) string {
	switch field {
	case "", "name":
		return contactNameAnchor
	case "org":
		return "lower(coalesce(c.search_meta->>'org', ''))"
	default:
		return ""
	}
}
