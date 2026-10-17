package search

import "strings"

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokWord
	tokString
	tokLParen
	tokRParen
	tokMinus
)

type token struct {
	kind       tokenKind
	text       string
	start, end int
}

// lex splits raw text into chunks. A chunk is a whitespace-separated run, with
// quoted segments allowed to contain whitespace. Operators are not separate
// tokens: `name:josh` stays one chunk so an unknown prefix such as a URL can
// fall back to free text without breaking apart.
func lex(raw string) ([]token, []Warning) {
	var toks []token
	var warns []Warning

	for i := 0; i < len(raw); {
		c := raw[i]
		if isSpace(c) {
			i++
			continue
		}
		switch c {
		case '(':
			toks = append(toks, token{kind: tokLParen, text: "(", start: i, end: i + 1})
			i++
			continue
		case ')':
			toks = append(toks, token{kind: tokRParen, text: ")", start: i, end: i + 1})
			i++
			continue
		}

		// A leading '-' negates the next primary. A '-' inside a word
		// (anne-marie) is ordinary text.
		if c == '-' && (i == 0 || isSpace(raw[i-1]) || raw[i-1] == '(') {
			toks = append(toks, token{kind: tokMinus, text: "-", start: i, end: i + 1})
			i++
			continue
		}

		start := i
		i = scanChunk(raw, i)
		text := raw[start:i]
		kind := tokWord
		if strings.HasPrefix(text, `"`) {
			if _, ok := unquote(text); ok {
				kind = tokString
			} else {
				warns = append(warns, Warning{
					Code:    "quote",
					Message: "unterminated quote",
					Start:   start,
					End:     i,
				})
			}
		}
		toks = append(toks, token{kind: kind, text: text, start: start, end: i})
	}

	toks = append(toks, token{kind: tokEOF, start: len(raw), end: len(raw)})
	return toks, warns
}

// scanChunk returns the end of the chunk beginning at i. Whitespace and
// parentheses end a chunk unless they are inside a quoted segment.
func scanChunk(raw string, i int) int {
	inQuote := false
	for i < len(raw) {
		c := raw[i]
		if inQuote {
			switch c {
			case '\\':
				if i+1 < len(raw) {
					i += 2
					continue
				}
			case '"':
				inQuote = false
			}
			i++
			continue
		}
		switch c {
		case '"':
			inQuote = true
			i++
		case '(', ')':
			return i
		default:
			if isSpace(c) {
				return i
			}
			i++
		}
	}
	return i
}

// unquote decodes a fully quoted string. It returns false when s is not a
// single quoted value or contains an unescaped interior quote.
func unquote(s string) (string, bool) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", false
	}
	var b strings.Builder
	for i := 1; i < len(s)-1; i++ {
		if s[i] == '\\' && i+1 < len(s)-1 {
			switch s[i+1] {
			case '"', '\\':
				b.WriteByte(s[i+1])
				i++
			default:
				b.WriteByte('\\')
			}
			continue
		}
		if s[i] == '"' {
			return "", false
		}
		b.WriteByte(s[i])
	}
	return b.String(), true
}

// splitSpecifier finds the first query operator outside quotes. The returned
// op is one of : = != <> > >= < <=.
func splitSpecifier(s string) (prefix, op, value string, opStart, opEnd int, found bool) {
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote {
			switch c {
			case '\\':
				i++
			case '"':
				inQuote = false
			}
			continue
		}
		if c == '"' {
			inQuote = true
			continue
		}
		if op, n := matchOp(s[i:]); n > 0 {
			return s[:i], op, s[i+n:], i, i + n, true
		}
	}
	return "", "", "", 0, 0, false
}

func matchOp(s string) (string, int) {
	switch {
	case strings.HasPrefix(s, "!="):
		return "!=", 2
	case strings.HasPrefix(s, "<>"):
		return "<>", 2
	case strings.HasPrefix(s, ">="):
		return ">=", 2
	case strings.HasPrefix(s, "<="):
		return "<=", 2
	case strings.HasPrefix(s, ":"):
		return ":", 1
	case strings.HasPrefix(s, "="):
		return "=", 1
	case strings.HasPrefix(s, ">"):
		return ">", 1
	case strings.HasPrefix(s, "<"):
		return "<", 1
	default:
		return "", 0
	}
}

// ChunkKind classifies a lexed chunk for the suggestion engine.
type ChunkKind int

const (
	ChunkWord ChunkKind = iota
	ChunkString
	ChunkParen
	ChunkMinus
)

// Chunk is a lexed span of the raw query. Offsets are byte offsets.
type Chunk struct {
	Kind       ChunkKind
	Text       string
	Start, End int
}

// Value is the chunk's text with a surrounding quote pair removed.
func (c Chunk) Value() string {
	if c.Kind == ChunkString {
		if v, ok := unquote(c.Text); ok {
			return v
		}
	}
	return c.Text
}

// Chunks returns the word, string, parenthesis, and negation spans of raw.
func Chunks(raw string) []Chunk {
	toks, _ := lex(raw)
	out := make([]Chunk, 0, len(toks))
	for _, t := range toks {
		var kind ChunkKind
		switch t.kind {
		case tokEOF:
			continue
		case tokWord:
			kind = ChunkWord
		case tokString:
			kind = ChunkString
		case tokLParen, tokRParen:
			kind = ChunkParen
		case tokMinus:
			kind = ChunkMinus
		}
		out = append(out, Chunk{Kind: kind, Text: t.text, Start: t.start, End: t.end})
	}
	return out
}

// ChunkAt returns the chunk containing the caret. A caret at a chunk boundary
// belongs to the chunk on its left, so completing the word just typed works.
func ChunkAt(raw string, caret int) (Chunk, bool) {
	for _, c := range Chunks(raw) {
		if c.Kind == ChunkParen {
			continue
		}
		if caret == c.Start || (caret > c.Start && caret <= c.End) {
			return c, true
		}
	}
	return Chunk{}, false
}

// PreviousChunk returns the nearest non-parenthesis chunk ending at or before
// caret. It is used to detect `no `, `not `, and `-` prefixes.
func PreviousChunk(raw string, caret int) (Chunk, bool) {
	var last Chunk
	found := false
	for _, c := range Chunks(raw) {
		if c.End > caret {
			break
		}
		if c.Kind == ChunkParen {
			continue
		}
		last, found = c, true
	}
	return last, found
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
