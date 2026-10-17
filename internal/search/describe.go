package search

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// ListNamer turns resolved list ids into a human phrase such as "the list Work"
// or "the lists Work and Family". A nil namer falls back to numeric ids.
type ListNamer func([]int64) string

// Describe renders an expression as a short human phrase, e.g.
//
//	no has:email              -> "with no email"
//	name:josh                 -> "name containing josh"
//	(org:acme or org:beta)    -> "organization containing acme or
//	no has:phone                 organization containing beta and with no phone"
//
// The result has no trailing period and starts lowercase; callers capitalise it
// when it begins a sentence.
func Describe(e Expr, schema *Schema, namer ListNamer) string {
	return describeExpr(e, schema, namer)
}

func describeExpr(e Expr, schema *Schema, namer ListNamer) string {
	switch v := e.(type) {
	case nil:
		return ""
	case And:
		return describeJoin(v.Kids, "and", schema, namer)
	case Or:
		return describeJoin(v.Kids, "or", schema, namer)
	case Not:
		return describeNot(v.Kid, schema, namer)
	case Term:
		return describeTerm(v, schema, namer)
	default:
		return ""
	}
}

func describeJoin(kids []Expr, word string, schema *Schema, namer ListNamer) string {
	parts := make([]string, 0, len(kids))
	for _, kid := range kids {
		d := describeExpr(kid, schema, namer)
		if d == "" {
			continue
		}
		if needsParens(kid, word) {
			d = "(" + d + ")"
		}
		parts = append(parts, d)
	}
	return joinWords(parts, word)
}

func needsParens(e Expr, parent string) bool {
	switch e.(type) {
	case Or:
		return parent == "and"
	case And:
		return parent == "or"
	default:
		return false
	}
}

func describeNot(kid Expr, schema *Schema, namer ListNamer) string {
	t, ok := kid.(Term)
	if !ok {
		d := describeExpr(kid, schema, namer)
		if d == "" {
			return ""
		}
		return "not (" + d + ")"
	}

	switch t.Field {
	case "":
		switch t.Op {
		case OpContains:
			return "not matching " + valueText(t.Value)
		case OpPrefix:
			return "not starting with " + valueText(t.Value)
		}
	case "has":
		if t.Op == OpExists {
			return "with neither a phone, an email, nor an organization"
		}
		switch enumString(t.Value) {
		case "phone":
			return "with no phone"
		case "email":
			return "with no email"
		case "org":
			return "with no organization"
		}
	case "untagged":
		if v, ok := boolValue(t.Value); ok {
			if v {
				return "in a list"
			}
			return "untagged"
		}
	case "in":
		if d := describeTerm(t, schema, namer); d != "" {
			return "not " + d
		}
	case "email", "number", "org":
		if t.Op == OpExists {
			if t.Field == "email" {
				return "without an email"
			}
			if t.Field == "org" {
				return "without an organization"
			}
			return "without a phone"
		}
	case "created", "updated":
		label := fieldLabel(t.Field, schema)
		if t.Op == OpGt {
			if ago, ok := agoPhrase(t.Value); ok {
				return label + " more than " + ago + " ago"
			}
		}
	}

	label := fieldLabel(t.Field, schema)
	switch t.Op {
	case OpContains:
		return label + " not containing " + valueText(t.Value)
	case OpPrefix:
		return label + " not starting with " + valueText(t.Value)
	case OpEqual:
		return label + " is not " + valueText(t.Value)
	}
	if d := describeTerm(t, schema, namer); d != "" {
		return "not " + d
	}
	return ""
}

func describeTerm(t Term, schema *Schema, namer ListNamer) string {
	if t.Field == "" {
		switch t.Op {
		case OpContains:
			return valueText(t.Value)
		case OpPrefix:
			return "starting with " + valueText(t.Value)
		case OpEqual:
			return "exactly " + valueText(t.Value)
		default:
			return ""
		}
	}

	switch t.Field {
	case "has":
		if t.Op == OpExists {
			return "with a phone, an email, or an organization"
		}
		switch enumString(t.Value) {
		case "phone":
			return "with a phone"
		case "email":
			return "with an email"
		case "org":
			return "with an organization"
		}
		return "with a contact method"
	case "untagged":
		if v, ok := boolValue(t.Value); ok {
			if v {
				return "untagged"
			}
			return "in a list"
		}
		return "untagged"
	case "in":
		if t.Op == OpExists {
			return "in a list"
		}
		if ids, ok := t.Value.(IDs); ok {
			if len(ids) == 0 {
				return "in a list"
			}
			if namer != nil {
				if phrase := namer([]int64(ids)); phrase != "" {
					return "in " + phrase
				}
			}
			return "in " + idPhrase([]int64(ids))
		}
		if text, ok := t.Value.(Text); ok {
			return "in the list " + string(text)
		}
		return "in a list"
	case "created", "updated":
		return describeDate(t, fieldLabel(t.Field, schema))
	}

	label := fieldLabel(t.Field, schema)
	switch t.Op {
	case OpExists:
		switch t.Field {
		case "email":
			return "with an email"
		case "number":
			return "with a phone"
		case "org":
			return "with an organization"
		default:
			return "with a " + label
		}
	case OpContains:
		return label + " containing " + valueText(t.Value)
	case OpPrefix:
		return label + " starting with " + valueText(t.Value)
	case OpEqual:
		return label + " is " + valueText(t.Value)
	default:
		return label
	}
}

func describeDate(t Term, label string) string {
	switch t.Op {
	case OpExists:
		return label + " at any time"
	case OpGt:
		return label + " after " + formatDate(t.Value)
	case OpGe:
		return label + " on or after " + formatDate(t.Value)
	case OpLt:
		return label + " before " + formatDate(t.Value)
	case OpLe:
		return label + " on or before " + formatDate(t.Value)
	case OpContains, OpEqual:
		switch v := t.Value.(type) {
		case TimeRange:
			if phrase, ok := rangePhrase(v); ok {
				return label + " " + phrase
			}
			return label + " between " + v.From.Format("2006-01-02") + " and " + v.To.Format("2006-01-02")
		case Time:
			return label + " on " + time.Time(v).Format("2006-01-02")
		}
	}
	return label
}

func rangePhrase(r TimeRange) (string, bool) {
	if isDayRange(r) {
		return "on " + r.From.Format("2006-01-02"), true
	}
	// Relative ranges are built as [now-duration, now].
	if r.To.Before(time.Now().Add(-2 * time.Minute)) {
		return "", false
	}
	d := r.To.Sub(r.From)
	days := int(math.Round(d.Hours() / 24))
	switch {
	case days >= 365 && days%365 == 0:
		n := days / 365
		if n == 1 {
			return "in the last year", true
		}
		return fmt.Sprintf("in the last %d years", n), true
	case days == 1:
		return "in the last day", true
	case days > 1:
		return fmt.Sprintf("in the last %d days", days), true
	default:
		return "", false
	}
}

func isDayRange(r TimeRange) bool {
	return r.From.Hour() == 0 && r.From.Minute() == 0 && r.From.Second() == 0 &&
		r.From.Nanosecond() == 0 && r.To.Sub(r.From) == 24*time.Hour
}

func agoPhrase(v Value) (string, bool) {
	t, ok := v.(Time)
	if !ok {
		return "", false
	}
	d := time.Since(time.Time(t))
	if d < 0 {
		return "", false
	}
	days := int(math.Round(d.Hours() / 24))
	switch {
	case days >= 365 && days%365 == 0:
		if days == 365 {
			return "a year", true
		}
		return fmt.Sprintf("%d years", days/365), true
	case days >= 30 && days%30 == 0:
		if days == 30 {
			return "a month", true
		}
		return fmt.Sprintf("%d months", days/30), true
	case days >= 7 && days%7 == 0:
		if days == 7 {
			return "a week", true
		}
		return fmt.Sprintf("%d weeks", days/7), true
	case days == 1:
		return "a day", true
	case days > 1:
		return fmt.Sprintf("%d days", days), true
	default:
		return "", false
	}
}

func formatDate(v Value) string {
	switch x := v.(type) {
	case Time:
		return time.Time(x).Format("2006-01-02")
	case TimeRange:
		return x.From.Format("2006-01-02")
	default:
		return ""
	}
}

func fieldLabel(key string, schema *Schema) string {
	if schema != nil {
		if f, ok := schema.Lookup(key); ok {
			return strings.ToLower(f.Label)
		}
	}
	return key
}

func enumString(v Value) string {
	switch x := v.(type) {
	case Enum:
		return string(x)
	case Text:
		return string(x)
	default:
		return ""
	}
}

func boolValue(v Value) (bool, bool) {
	switch x := v.(type) {
	case Bool:
		return bool(x), true
	case Text:
		switch strings.ToLower(string(x)) {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off":
			return false, true
		}
	}
	return false, false
}

func valueText(v Value) string {
	switch x := v.(type) {
	case Text:
		return string(x)
	case Enum:
		return string(x)
	case Bool:
		return fmt.Sprint(bool(x))
	case IDs:
		return idPhrase([]int64(x))
	default:
		return fmt.Sprint(x)
	}
}

func idPhrase(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("%d", id))
	}
	if len(parts) == 1 {
		return "list " + parts[0]
	}
	return "lists " + joinWords(parts, "and")
}

func joinWords(parts []string, word string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " " + word + " " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " " + word + " " + parts[len(parts)-1]
	}
}
