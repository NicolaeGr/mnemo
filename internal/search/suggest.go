package search

import "strings"

// ContextKind describes what kind of completion the caret is asking for.
type ContextKind int

const (
	ContextNone ContextKind = iota
	ContextWord
	ContextField
	ContextFieldPrefix
	ContextFieldValue
	ContextEmpty
)

// SuggestContext is the parser's read on a partial query at a caret. The store
// maps it to concrete suggestions (and supplies dynamic values such as lists).
type SuggestContext struct {
	Kind           ContextKind
	Field          Field
	Prefix         string // value or field prefix already typed
	Word           string // free-text word under the caret
	Negated        bool   // labels should read "Without …" / "Not …"
	InsertNegation bool   // the replacement span includes no/not and must emit it
	SpanStart      int    // byte offsets in the raw query
	SpanEnd        int
}

// Analyze inspects raw at caret and reports what completions make sense.
func Analyze(raw string, caret int, schema *Schema) SuggestContext {
	if caret < 0 {
		caret = 0
	}
	if caret > len(raw) {
		caret = len(raw)
	}

	if chunk, ok := ChunkAt(raw, caret); ok {
		prev, hasPrev := previousChunkBefore(raw, chunk.Start)
		negated, insertNegation := negationPrefix(prev, hasPrev)
		switch chunk.Kind {
		case ChunkWord:
			value := chunk.Value()
			lower := strings.ToLower(value)
			if lower == "no" || lower == "not" {
				return SuggestContext{
					Kind:           ContextFieldPrefix,
					Prefix:         "",
					Negated:        true,
					InsertNegation: true,
					SpanStart:      chunk.Start,
					SpanEnd:        chunk.End,
				}
			}
			if prefix, _, val, _, _, found := splitSpecifier(chunk.Text); found {
				field, ok := schema.Lookup(prefix)
				if !ok {
					return SuggestContext{Kind: ContextNone}
				}
				return SuggestContext{
					Kind:           ContextFieldValue,
					Field:          field,
					Prefix:         cleanPrefix(val),
					Negated:        negated,
					InsertNegation: insertNegation,
					SpanStart:      chunk.Start,
					SpanEnd:        chunk.End,
				}
			}
			if field, ok := schema.Lookup(value); ok {
				return SuggestContext{
					Kind:           ContextField,
					Field:          field,
					Negated:        negated,
					InsertNegation: insertNegation,
					SpanStart:      chunk.Start,
					SpanEnd:        chunk.End,
				}
			}
			if len(schema.MatchingFields(value)) > 0 {
				return SuggestContext{
					Kind:           ContextFieldPrefix,
					Prefix:         value,
					Negated:        negated,
					InsertNegation: insertNegation,
					SpanStart:      chunk.Start,
					SpanEnd:        chunk.End,
				}
			}
			return SuggestContext{
				Kind:           ContextWord,
				Word:           value,
				Negated:        negated,
				InsertNegation: insertNegation,
				SpanStart:      chunk.Start,
				SpanEnd:        chunk.End,
			}
		case ChunkString:
			return SuggestContext{
				Kind:           ContextWord,
				Word:           chunk.Value(),
				Negated:        negated,
				InsertNegation: insertNegation,
				SpanStart:      chunk.Start,
				SpanEnd:        chunk.End,
			}
		default:
			return SuggestContext{Kind: ContextNone}
		}
	}

	prev, ok := PreviousChunk(raw, caret)
	if !ok {
		return SuggestContext{Kind: ContextNone}
	}
	if prev.Kind == ChunkMinus {
		return SuggestContext{
			Kind:      ContextEmpty,
			Negated:   true,
			SpanStart: caret,
			SpanEnd:   caret,
		}
	}
	if prev.Kind == ChunkWord {
		switch strings.ToLower(prev.Text) {
		case "no", "not":
			return SuggestContext{
				Kind:      ContextEmpty,
				Negated:   true,
				SpanStart: caret,
				SpanEnd:   caret,
			}
		case "and", "or", "&", "|":
			return SuggestContext{
				Kind:      ContextEmpty,
				SpanStart: caret,
				SpanEnd:   caret,
			}
		}
	}
	return SuggestContext{Kind: ContextNone}
}

func previousChunkBefore(raw string, pos int) (Chunk, bool) {
	var last Chunk
	found := false
	for _, c := range Chunks(raw) {
		if c.End > pos {
			break
		}
		if c.Kind == ChunkParen {
			continue
		}
		last, found = c, true
	}
	return last, found
}

func negationPrefix(prev Chunk, ok bool) (negated, insert bool) {
	if !ok {
		return false, false
	}
	if prev.Kind == ChunkMinus {
		return true, false
	}
	if prev.Kind == ChunkWord {
		switch strings.ToLower(prev.Text) {
		case "no", "not":
			return true, false
		}
	}
	return false, false
}

// cleanPrefix turns the raw text after an operator into a matchable prefix. It
// removes a surrounding quote pair and decodes the two supported escapes.
func cleanPrefix(value string) string {
	value = strings.TrimPrefix(value, `"`)
	value = strings.TrimSuffix(value, `"`)
	value = strings.ReplaceAll(value, `\"`, `"`)
	value = strings.ReplaceAll(value, `\\`, `\`)
	return value
}
