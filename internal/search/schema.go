package search

import "strings"

// Type tells the planner and the suggestion engine how a field behaves.
type Type int

const (
	TypeText Type = iota
	TypeEmail
	TypePhone
	TypeEnum
	TypeBool
	TypeList
	TypeDate
)

// Field describes one searchable field. It is deliberately declarative: it says
// what the field is and how to present it, not how to spell its SQL. A backend
// maps field keys onto storage separately.
type Field struct {
	Key         string
	Label       string
	Type        Type
	Aliases     []string // alternate names accepted in queries, never suggested
	Free        bool     // participates in bare (unscoped) text matching
	Values      []string // completion choices for a small closed set
	Example     string   // canonical example shown in help text
	Description string   // short human sentence used by autocomplete
	Weight      int      // ranking weight; zero means not rankable
	// Normalize canonicalizes a user value. It returns false when the value is
	// not valid for the field.
	Normalize func(string) (string, bool)
}

// Schema is an ordered set of fields with alias resolution.
type Schema struct {
	fields []Field
	byKey  map[string]int
}

// NewSchema indexes fields and their aliases. Keys and aliases are matched
// case-insensitively.
func NewSchema(fields ...Field) *Schema {
	s := &Schema{fields: fields, byKey: make(map[string]int, len(fields))}
	for i, f := range fields {
		s.byKey[strings.ToLower(f.Key)] = i
		for _, alias := range f.Aliases {
			s.byKey[strings.ToLower(alias)] = i
		}
	}
	return s
}

// Lookup resolves a key or alias to its canonical field.
func (s *Schema) Lookup(key string) (Field, bool) {
	i, ok := s.byKey[strings.ToLower(key)]
	if !ok {
		return Field{}, false
	}
	return s.fields[i], true
}

// Fields returns the schema in declaration order.
func (s *Schema) Fields() []Field { return s.fields }

// MatchingFields returns fields whose key, alias, or label starts with prefix.
// Canonical keys are returned once, even when several aliases match.
func (s *Schema) MatchingFields(prefix string) []Field {
	prefix = strings.ToLower(prefix)
	seen := make(map[string]bool, len(s.fields))
	var out []Field
	for _, f := range s.fields {
		match := strings.HasPrefix(strings.ToLower(f.Key), prefix) ||
			strings.HasPrefix(strings.ToLower(f.Label), prefix)
		for _, alias := range f.Aliases {
			if strings.HasPrefix(strings.ToLower(alias), prefix) {
				match = true
			}
		}
		if match && !seen[f.Key] {
			seen[f.Key] = true
			out = append(out, f)
		}
	}
	return out
}
