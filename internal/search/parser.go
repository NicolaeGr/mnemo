package search

import "strings"

// Warning is a non-fatal problem with part of a query. Search still runs; the
// UI shows these so a typo does not silently return the wrong result set.
type Warning struct {
	Code       string
	Message    string
	Start, End int // byte offsets in the raw query
}

// Parsed is the result of parsing raw query text.
type Parsed struct {
	Expr     Expr
	Warnings []Warning
}

// Parse turns raw text into an expression. A nil Expr means "match everything".
// Unknown fields become free text and a warning rather than swallowing the
// search, so a URL or an unlearned key still does something sensible.
func Parse(raw string, schema *Schema) Parsed {
	toks, warns := lex(raw)
	p := &parser{toks: toks, schema: schema, warns: warns}
	expr := p.parseOr()
	for !p.atEnd() {
		t := p.next()
		p.warnAt("unexpected token "+t.text, t.start, t.end)
	}
	return Parsed{Expr: expr, Warnings: p.warns}
}

type parser struct {
	toks   []token
	pos    int
	schema *Schema
	warns  []Warning
}

func (p *parser) atEnd() bool { return p.toks[p.pos].kind == tokEOF }

func (p *parser) peek() token { return p.toks[p.pos] }

func (p *parser) next() token {
	t := p.toks[p.pos]
	if t.kind != tokEOF {
		p.pos++
	}
	return t
}

func (p *parser) warnAt(msg string, start, end int) {
	p.warns = append(p.warns, Warning{Code: "syntax", Message: msg, Start: start, End: end})
}

func (p *parser) keywordAt(pos int) string {
	if pos >= len(p.toks) {
		return ""
	}
	t := p.toks[pos]
	if t.kind != tokWord {
		return ""
	}
	switch strings.ToLower(t.text) {
	case "and", "&":
		return "and"
	case "or", "|":
		return "or"
	case "not":
		return "not"
	case "no":
		return "no"
	default:
		return ""
	}
}

func (p *parser) startsPrimaryAt(pos int) bool {
	if pos >= len(p.toks) {
		return false
	}
	t := p.toks[pos]
	switch t.kind {
	case tokWord:
		kw := p.keywordAt(pos)
		return kw != "and" && kw != "or"
	case tokString, tokLParen, tokMinus:
		return true
	default:
		return false
	}
}

func (p *parser) parseOr() Expr {
	left := p.parseAnd()
	for {
		kw := p.keywordAt(p.pos)
		if kw != "or" {
			break
		}
		op := p.next()
		right := p.parseAnd()
		if right == nil {
			p.warnAt("expected a term after 'or'", op.start, op.end)
			continue
		}
		if left == nil {
			left = right
		} else {
			left = Or{Kids: []Expr{left, right}}
		}
	}
	return left
}

func (p *parser) parseAnd() Expr {
	var kids []Expr
	for {
		if p.atEnd() || p.peek().kind == tokRParen {
			break
		}
		kw := p.keywordAt(p.pos)
		if kw == "or" {
			break
		}
		if kw == "and" {
			op := p.next()
			if p.atEnd() || p.peek().kind == tokRParen || p.keywordAt(p.pos) == "or" {
				p.warnAt("expected a term after 'and'", op.start, op.end)
				break
			}
			continue
		}
		kid := p.parseUnary()
		if kid != nil {
			kids = append(kids, kid)
		}
	}
	switch len(kids) {
	case 0:
		return nil
	case 1:
		return kids[0]
	default:
		return And{Kids: kids}
	}
}

func (p *parser) parseUnary() Expr {
	if p.atEnd() || p.peek().kind == tokRParen {
		return nil
	}
	kw := p.keywordAt(p.pos)
	if kw == "not" || kw == "no" {
		t := p.peek()
		if p.startsPrimaryAt(p.pos + 1) {
			p.next()
			kid := p.parseUnary()
			if kid == nil {
				p.warnAt("expected a term after '"+kw+"'", t.start, t.end)
				return nil
			}
			return Not{Kid: kid}
		}
		// With nothing to negate, the word is ordinary text.
		p.next()
		return freeChunk(t.text)
	}
	if p.peek().kind == tokMinus {
		t := p.next()
		if !p.startsPrimaryAt(p.pos) {
			return freeChunk(t.text)
		}
		kid := p.parseUnary()
		if kid == nil {
			return nil
		}
		return Not{Kid: kid}
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() Expr {
	t := p.peek()
	switch t.kind {
	case tokLParen:
		open := p.next()
		e := p.parseOr()
		if p.peek().kind == tokRParen {
			p.next()
		} else {
			p.warnAt("missing ')'", open.start, open.end)
		}
		return e
	case tokWord, tokString:
		p.next()
		// A known field may be separated from its operator by spaces:
		// `name : josh`. Merge the operator chunk so the rest of the parser
		// sees the same shape as `name:josh`.
		if t.kind == tokWord && !strings.HasPrefix(t.text, `"`) {
			if _, _, _, _, _, found := splitSpecifier(t.text); !found && p.peek().kind == tokWord {
				if _, ok := p.schema.Lookup(t.text); ok {
					if _, _, _, opStart, _, ok := splitSpecifier(p.peek().text); ok && opStart == 0 {
						n := p.next()
						return p.parseChunk(token{kind: tokWord, text: t.text + n.text, start: t.start, end: n.end})
					}
				}
			}
		}
		return p.parseChunk(t)
	default:
		p.next()
		p.warnAt("unexpected token "+t.text, t.start, t.end)
		return nil
	}
}

func (p *parser) parseChunk(t token) Expr {
	raw := t.text
	if strings.HasPrefix(raw, `"`) {
		if v, ok := unquote(raw); ok {
			return Term{Op: OpContains, Value: Text(v)}
		}
		p.warnAt("unterminated quote", t.start, t.end)
		return Term{Op: OpContains, Value: Text(strings.TrimPrefix(raw, `"`))}
	}

	prefix, opText, value, opStart, opEnd, found := splitSpecifier(raw)
	if !found {
		return freeChunk(raw)
	}
	if prefix == "" {
		p.warnAt("missing field before operator", t.start, t.start+opEnd)
		return freeChunk(raw)
	}
	field, ok := p.schema.Lookup(prefix)
	if !ok {
		p.warnAt("unknown field '"+prefix+"'; searched as text", t.start, t.start+opStart)
		return freeChunk(raw)
	}
	// Tolerate a space after the operator: `name: josh`.
	if value == "" && p.peek().kind != tokLParen && p.peek().kind != tokMinus && p.startsPrimaryAt(p.pos) {
		value = p.next().text
	}
	if value == "" {
		p.warnAt("missing value for '"+field.Key+"'", t.start+opStart, t.end)
		return nil
	}

	quoted := strings.HasPrefix(value, `"`)
	val := value
	if quoted {
		v, ok := unquote(value)
		if !ok {
			p.warnAt("unterminated quote", t.start+opEnd, t.end)
			v = strings.TrimPrefix(value, `"`)
		}
		val = v
	}

	op, negate, ok := parseOp(opText)
	if !ok {
		p.warnAt("unknown operator '"+opText+"'", t.start+opStart, t.start+opEnd)
		return nil
	}
	switch {
	case !quoted && val == "*":
		op = OpExists
	case !quoted && strings.HasSuffix(val, "*") && len(val) > 1:
		op = OpPrefix
		val = strings.TrimSuffix(val, "*")
	}

	term := Term{Field: field.Key, Op: op, Value: Text(val)}
	if negate {
		return Not{Kid: term}
	}
	return term
}

func freeChunk(raw string) Expr {
	if raw == "" {
		return nil
	}
	op := OpContains
	val := raw
	if strings.HasSuffix(val, "*") && len(val) > 1 {
		op = OpPrefix
		val = strings.TrimSuffix(val, "*")
	}
	return Term{Op: op, Value: Text(val)}
}

func parseOp(s string) (Op, bool, bool) {
	switch s {
	case ":":
		return OpContains, false, true
	case "=":
		return OpEqual, false, true
	case "!=", "<>":
		return OpEqual, true, true
	case ">":
		return OpGt, false, true
	case ">=":
		return OpGe, false, true
	case "<":
		return OpLt, false, true
	case "<=":
		return OpLe, false, true
	default:
		return OpContains, false, false
	}
}
