package search

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	schema := testSchema()
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"josh", `term("",contains,"josh")`},
		{"josh smith", `and(term("",contains,"josh"),term("",contains,"smith"))`},
		{`"josh smith"`, `term("",contains,"josh smith")`},
		{"name:josh", `term("name",contains,"josh")`},
		{"Name:Josh", `term("name",contains,"Josh")`},
		{"name=Josh", `term("name",equal,"Josh")`},
		{`name="Josh Smith"`, `term("name",equal,"Josh Smith")`},
		{`name:"Josh Smith"`, `term("name",contains,"Josh Smith")`},
		{"name:Josh Smith", `and(term("name",contains,"Josh"),term("",contains,"Smith"))`},
		{"name:jo*", `term("name",prefix,"jo")`},
		{"name:*", `term("name",exists,"*")`},
		{"josh*", `term("",prefix,"josh")`},
		{"phone:555", `term("number",contains,"555")`},
		{"no has:phone", `not(term("has",contains,"phone"))`},
		{"not phone", `not(term("",contains,"phone"))`},
		{`not in:"Family Reunion"`, `not(term("in",contains,"Family Reunion"))`},
		{"-in:Work", `not(term("in",contains,"Work"))`},
		{"-anne-marie", `not(term("",contains,"anne-marie"))`},
		{"anne-marie", `term("",contains,"anne-marie")`},
		{"no", `term("",contains,"no")`},
		{"no phone", `not(term("",contains,"phone"))`},
		{"(org:acme or org:beta) no has:phone", `and(or(term("org",contains,"acme"),term("org",contains,"beta")),not(term("has",contains,"phone")))`},
		{"a or b c", `or(term("",contains,"a"),and(term("",contains,"b"),term("",contains,"c")))`},
		{"a b or c", `or(and(term("",contains,"a"),term("",contains,"b")),term("",contains,"c"))`},
		{"updated>7d", `term("updated",>,"7d")`},
		{"updated>=2024-01-01", `term("updated",>=,"2024-01-01")`},
		{"untagged!=true", `not(term("untagged",equal,"true"))`},
		{"name: josh", `term("name",contains,"josh")`},
		{"name : josh", `term("name",contains,"josh")`},
		{"in: Work", `term("in",contains,"Work")`},
		{`name:"The \"A\" List"`, `term("name",contains,"The \"A\" List")`},
		{"foo:bar", `term("",contains,"foo:bar")`},
		{"http://example.com", `term("",contains,"http://example.com")`},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := dump(Parse(tc.in, schema).Expr)
			if got != tc.want {
				t.Fatalf("Parse(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseWarnings(t *testing.T) {
	schema := testSchema()
	cases := []struct {
		in   string
		want string
	}{
		{"foo:bar", "unknown field"},
		{"name:", "missing value"},
		{`name:"unterminated`, "unterminated quote"},
		{"(a or b", "missing ')'"},
		{"a or", "expected a term after 'or'"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			warns := Parse(tc.in, schema).Warnings
			var joined strings.Builder
			for _, w := range warns {
				joined.WriteString(w.Message)
				joined.WriteByte('\n')
			}
			if !strings.Contains(joined.String(), tc.want) {
				t.Fatalf("warnings for %q = %q, want one containing %q", tc.in, joined.String(), tc.want)
			}
		})
	}
}

func TestAnalyze(t *testing.T) {
	schema := testSchema()
	cases := []struct {
		name      string
		in        string
		caret     int
		kind      ContextKind
		field     string
		prefix    string
		word      string
		negated   bool
		insertNeg bool
		spanStart int
		spanEnd   int
	}{
		{"value", "name:jo", 7, ContextFieldValue, "name", "jo", "", false, false, 0, 7},
		{"quoted value", `in:"Fam`, 7, ContextFieldValue, "in", "Fam", "", false, false, 0, 7},
		{"exact field", "has", 3, ContextField, "has", "", "", false, false, 0, 3},
		{"field prefix", "na", 2, ContextFieldPrefix, "", "na", "", false, false, 0, 2},
		{"word", "josh", 4, ContextWord, "", "", "josh", false, false, 0, 4},
		{"negation word", "no", 2, ContextFieldPrefix, "", "", "", true, true, 0, 2},
		{"after no", "no ", 3, ContextEmpty, "", "", "", true, false, 3, 3},
		{"after minus", "- ", 2, ContextEmpty, "", "", "", true, false, 2, 2},
		{"after and", "a and ", 6, ContextEmpty, "", "", "", false, false, 6, 6},
		{"empty", "", 0, ContextNone, "", "", "", false, false, 0, 0},
		{"after word", "josh ", 5, ContextNone, "", "", "", false, false, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Analyze(tc.in, tc.caret, schema)
			if got.Kind != tc.kind || got.Field.Key != tc.field || got.Prefix != tc.prefix ||
				got.Word != tc.word || got.Negated != tc.negated || got.InsertNegation != tc.insertNeg ||
				got.SpanStart != tc.spanStart || got.SpanEnd != tc.spanEnd {
				t.Fatalf("Analyze(%q, %d) = %+v", tc.in, tc.caret, got)
			}
		})
	}
}

func testSchema() *Schema {
	return NewSchema(
		Field{Key: "name", Label: "Name", Type: TypeText, Free: true},
		Field{Key: "org", Label: "Organization", Type: TypeText, Free: true},
		Field{Key: "email", Label: "Email", Type: TypeEmail, Free: true},
		Field{Key: "number", Label: "Phone", Type: TypePhone, Free: true, Aliases: []string{"phone", "tel"}},
		Field{Key: "has", Label: "Has", Type: TypeEnum, Values: []string{"phone", "email", "org"}},
		Field{Key: "untagged", Label: "Untagged", Type: TypeBool},
		Field{Key: "in", Label: "List", Type: TypeList},
		Field{Key: "created", Label: "Created", Type: TypeDate},
		Field{Key: "updated", Label: "Updated", Type: TypeDate},
	)
}

func dump(e Expr) string {
	switch v := e.(type) {
	case nil:
		return ""
	case And:
		return "and(" + dumpAll(v.Kids) + ")"
	case Or:
		return "or(" + dumpAll(v.Kids) + ")"
	case Not:
		return "not(" + dump(v.Kid) + ")"
	case Term:
		return fmt.Sprintf("term(%s,%s,%s)", strconv.Quote(v.Field), v.Op, strconv.Quote(dumpValue(v.Value)))
	default:
		return "?"
	}
}

func dumpAll(kids []Expr) string {
	parts := make([]string, 0, len(kids))
	for _, kid := range kids {
		parts = append(parts, dump(kid))
	}
	return strings.Join(parts, ",")
}

func dumpValue(v Value) string {
	switch x := v.(type) {
	case Text:
		return string(x)
	case Enum:
		return string(x)
	case Bool:
		return fmt.Sprint(bool(x))
	default:
		return fmt.Sprint(x)
	}
}
