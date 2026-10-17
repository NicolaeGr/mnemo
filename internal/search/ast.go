// Package search implements the typed query language used by contact search.
// It is storage agnostic: Parse turns raw text into an AST and a caller compiles
// that AST for its own backend.
package search

import "time"

// Expr is one node in a parsed query.
type Expr interface{ expr() }

// And requires every child to match. An empty And matches everything.
type And struct{ Kids []Expr }

func (And) expr() {}

// Or requires at least one child to match.
type Or struct{ Kids []Expr }

func (Or) expr() {}

// Not negates its child.
type Not struct{ Kid Expr }

func (Not) expr() {}

// Term is a single predicate. Field is a canonical schema key, or "" for a
// free-text term that the compiler expands over every free field.
type Term struct {
	Field string
	Op    Op
	Value Value
}

func (Term) expr() {}

// Op is the comparison a Term asks for.
type Op int

const (
	OpContains Op = iota // substring match
	OpPrefix             // prefix match
	OpEqual              // exact match
	OpExists             // the field is present / non-empty
	OpGt
	OpGe
	OpLt
	OpLe
)

func (o Op) String() string {
	switch o {
	case OpContains:
		return "contains"
	case OpPrefix:
		return "prefix"
	case OpEqual:
		return "equal"
	case OpExists:
		return "exists"
	case OpGt:
		return ">"
	case OpGe:
		return ">="
	case OpLt:
		return "<"
	case OpLe:
		return "<="
	default:
		return "?"
	}
}

// Value is a typed term value. The parser produces Text; a planner may replace
// it with a more specific type after validation.
type Value interface{ value() }

type Text string

func (Text) value() {}

type Enum string

func (Enum) value() {}

type Bool bool

func (Bool) value() {}

// IDs is a set of resolved list/book ids.
type IDs []int64

func (IDs) value() {}

// Time is a point in time.
type Time time.Time

func (Time) value() {}

// TimeRange is a half-open interval [From, To). It represents "that day" or
// "the last seven days" without inventing two separate terms.
type TimeRange struct{ From, To time.Time }

func (TimeRange) value() {}
