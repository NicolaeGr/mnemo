package store

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/nicolaegr/mnemo/internal/search"
)

// Suggestion is one autocomplete choice. Start and End are byte offsets in the
// raw query; the HTTP layer converts them to JS string offsets. Insert replaces
// that span and already includes a trailing space.
type Suggestion struct {
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	Insert      string `json:"insert"`
	Description string `json:"description,omitempty"`
	Start       int    `json:"start"`
	End         int    `json:"end"`
}

const maxSuggestions = 10

// SuggestContacts proposes completions for a partial query at caret. The
// grammar lives here, not in the browser: the client only replaces a span with
// the returned Insert text.
func (s ScopedStore) SuggestContacts(ctx context.Context, raw string, caret int) ([]Suggestion, error) {
	c := search.Analyze(raw, caret, contactSchema)
	var (
		out []Suggestion
		err error
	)
	switch c.Kind {
	case search.ContextWord:
		out = wordSuggestions(c)
	case search.ContextField:
		out, err = s.fieldSuggestions(ctx, c)
	case search.ContextFieldPrefix:
		if c.InsertNegation {
			out, err = s.negatedFieldSuggestions(ctx, c)
		} else {
			out = fieldPrefixSuggestions(c)
		}
	case search.ContextFieldValue:
		out, err = s.valueSuggestions(ctx, c)
	case search.ContextEmpty:
		if c.Negated {
			out, err = s.negatedFieldSuggestions(ctx, c)
		} else {
			out = fieldPrefixSuggestions(c)
		}
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(out) > maxSuggestions {
		out = out[:maxSuggestions]
	}
	return out, nil
}

func suggest(c search.SuggestContext, kind, label, insert string) Suggestion {
	if c.InsertNegation {
		insert = "no " + insert
	}
	return Suggestion{
		Kind:        kind,
		Label:       label,
		Insert:      insert,
		Description: strings.TrimSpace(insert),
		Start:       c.SpanStart,
		End:         c.SpanEnd,
	}
}

func fieldPrefixSuggestions(c search.SuggestContext) []Suggestion {
	fields := contactSchema.MatchingFields(c.Prefix)
	out := make([]Suggestion, 0, len(fields))
	for _, f := range fields {
		label := f.Label
		kind := "Field"
		if c.Negated {
			label = "Not " + strings.ToLower(label)
			kind = "Without"
		}
		out = append(out, suggest(c, kind, label, f.Key+": "))
	}
	return out
}

func (s ScopedStore) fieldSuggestions(ctx context.Context, c search.SuggestContext) ([]Suggestion, error) {
	switch c.Field.Type {
	case search.TypeEnum, search.TypeBool, search.TypeDate, search.TypeList:
		return s.valueSuggestions(ctx, c)
	default:
		label := c.Field.Label
		kind := "Field"
		if c.Negated {
			label = "Not " + strings.ToLower(label)
			kind = "Without"
		}
		return []Suggestion{suggest(c, kind, label, c.Field.Key+": ")}, nil
	}
}

func (s ScopedStore) valueSuggestions(ctx context.Context, c search.SuggestContext) ([]Suggestion, error) {
	switch c.Field.Type {
	case search.TypeEnum:
		return enumSuggestions(c), nil
	case search.TypeBool:
		return boolSuggestions(c), nil
	case search.TypeDate:
		return dateSuggestions(c), nil
	case search.TypeList:
		return s.listSuggestions(ctx, c)
	default:
		return nil, nil
	}
}

func enumSuggestions(c search.SuggestContext) []Suggestion {
	if c.Field.Key != "has" {
		return nil
	}
	prefix := strings.ToLower(c.Prefix)
	var out []Suggestion
	for _, value := range c.Field.Values {
		if prefix != "" && !strings.HasPrefix(value, prefix) {
			continue
		}
		label, negated := hasLabels(value)
		kind := "Value"
		if c.Negated {
			label = negated
			kind = "Without"
		}
		out = append(out, suggest(c, kind, label, "has:"+value+" "))
	}
	return out
}

func hasLabels(value string) (label, negated string) {
	switch value {
	case "phone":
		return "Has a phone", "Without a phone"
	case "email":
		return "Has an email", "Without an email"
	case "org":
		return "Has an organization", "Without an organization"
	default:
		return "Has a " + value, "Without a " + value
	}
}

func boolSuggestions(c search.SuggestContext) []Suggestion {
	prefix := strings.ToLower(c.Prefix)
	var out []Suggestion
	if c.Negated {
		if prefix == "" || strings.HasPrefix("true", prefix) {
			out = append(out, suggest(c, "Without", "Not untagged", "untagged:true "))
		}
		return out
	}
	if prefix == "" || strings.HasPrefix("true", prefix) {
		out = append(out, suggest(c, "Value", "Is untagged", "untagged:true "))
	}
	if prefix == "" || strings.HasPrefix("false", prefix) {
		out = append(out, suggest(c, "Value", "Is in a list", "untagged:false "))
	}
	return out
}

type dateExample struct {
	Value string
	Last  string
	Older string
}

var dateExamples = []dateExample{
	{Value: "7d", Last: "in the last 7 days", Older: "more than 7 days ago"},
	{Value: "30d", Last: "in the last 30 days", Older: "more than 30 days ago"},
	{Value: "1y", Last: "in the last year", Older: "more than a year ago"},
}

func dateSuggestions(c search.SuggestContext) []Suggestion {
	prefix := strings.ToLower(c.Prefix)
	var out []Suggestion
	for _, ex := range dateExamples {
		if prefix != "" &&
			!strings.HasPrefix(strings.ToLower(ex.Value), prefix) &&
			!strings.HasPrefix(strings.ToLower(ex.Last), prefix) &&
			!strings.HasPrefix(strings.ToLower(ex.Older), prefix) {
			continue
		}
		label := c.Field.Label + " " + ex.Last
		kind := "Date"
		if c.Negated {
			label = c.Field.Label + " " + ex.Older
			kind = "Without"
		}
		out = append(out, suggest(c, kind, label, c.Field.Key+">"+ex.Value+" "))
	}
	return out
}

func (s ScopedStore) listSuggestions(ctx context.Context, c search.SuggestContext) ([]Suggestion, error) {
	books, err := s.ListBooks(ctx)
	if err != nil {
		return nil, err
	}
	prefix := strings.ToLower(c.Prefix)
	counts := make(map[string]int, len(books))
	for _, b := range books {
		counts[strings.ToLower(b.DisplayName)]++
	}
	sort.SliceStable(books, func(i, j int) bool {
		return strings.ToLower(books[i].DisplayName) < strings.ToLower(books[j].DisplayName)
	})

	var out []Suggestion
	for _, b := range books {
		name := strings.ToLower(b.DisplayName)
		if prefix != "" &&
			!strings.HasPrefix(name, prefix) &&
			!strings.HasPrefix(strings.ToLower(b.Slug), prefix) {
			continue
		}
		token := quoteValue(b.DisplayName)
		if counts[name] > 1 {
			token = "#" + strconv.FormatInt(b.ID, 10)
		}
		label := `In "` + b.DisplayName + `"`
		kind := "List"
		if c.Negated {
			label = `Not in "` + b.DisplayName + `"`
			kind = "Without"
		}
		if !b.IsActive {
			label += " (inactive)"
		}
		out = append(out, suggest(c, kind, label, "in:"+token+" "))
	}
	return out, nil
}

func (s ScopedStore) negatedFieldSuggestions(ctx context.Context, c search.SuggestContext) ([]Suggestion, error) {
	var out []Suggestion

	// The most useful negated filters come first; less common fields follow.
	out = append(out,
		suggest(c, "Without", "Without a phone", "has:phone "),
		suggest(c, "Without", "Without an email", "has:email "),
		suggest(c, "Without", "Without an organization", "has:org "),
	)

	books, err := s.ListBooks(ctx)
	if err != nil {
		return nil, err
	}
	for i, b := range books {
		if i == 4 {
			break
		}
		token := quoteValue(b.DisplayName)
		out = append(out, suggest(c, "Without", `Not in "`+b.DisplayName+`"`, "in:"+token+" "))
	}
	if len(books) == 0 {
		out = append(out, suggest(c, "Without", "Not in a list", "in:"))
	}

	out = append(out,
		suggest(c, "Without", "Not untagged", "untagged:true "),
		suggest(c, "Without", "Not name", "name: "),
		suggest(c, "Without", "Not organization", "org: "),
		suggest(c, "Without", "Not email", "email: "),
		suggest(c, "Without", "Not phone", "number: "),
		suggest(c, "Without", "Not created", "created: "),
		suggest(c, "Without", "Not updated", "updated: "),
	)
	return out, nil
}

func wordSuggestions(c search.SuggestContext) []Suggestion {
	word := c.Word
	if word == "" {
		return nil
	}
	hasDigit := strings.ContainsAny(word, "0123456789")
	fields := []string{"name", "org", "email"}
	if hasDigit {
		fields = append(fields, "number")
	}
	var out []Suggestion
	for _, key := range fields {
		f, ok := contactSchema.Lookup(key)
		if !ok {
			continue
		}
		insert := key + ":" + quoteValue(word) + " "
		label := f.Label + ` contains "` + word + `"`
		kind := "Match"
		if c.Negated {
			label = f.Label + ` does not contain "` + word + `"`
			kind = "Without"
		}
		out = append(out, suggest(c, kind, label, insert))
	}
	return out
}

// quoteValue produces a query value that survives the lexer. Bare values stay
// bare; anything with whitespace, quotes, parentheses, or wildcard characters is
// quoted.
func quoteValue(v string) string {
	if v != "" && !strings.ContainsAny(v, " \t\r\n\"()*") {
		return v
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		if r == '\\' || r == '"' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}
