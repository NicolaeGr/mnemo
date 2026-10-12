// Package seg implements a server-driven "segment stack": layouts are
// segments (stable ID + outlet). Middleware records the stack a route sits
// under; the renderer diffs it against the client's mounted segments
// (X-Mounted-Segments) and returns only the missing tail.
package seg

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/a-h/templ"
)

const headerMounted = "X-Mounted-Segments"

type Segment struct {
	ID     string
	Render func(ctx context.Context, data any, child templ.Component) templ.Component
	// Load runs for every request under this segment; its result is placed in
	// the request context so any leaf can read it.
	Load func(ctx context.Context) (any, error)
}

type ctxKey int

const (
	keyStack ctxKey = iota
	keyData
	keyTitle
	keyStackIDs
	keyPath
)

func pushSegment(ctx context.Context, s Segment) context.Context {
	stack, _ := ctx.Value(keyStack).([]Segment)
	return context.WithValue(ctx, keyStack, append(stack, s))
}

func withData(ctx context.Context, id string, data any) context.Context {
	m, _ := ctx.Value(keyData).(map[string]any)
	if m == nil {
		m = make(map[string]any)
	}
	m[id] = data
	return context.WithValue(ctx, keyData, m)
}

func withTitle(ctx context.Context, title string) context.Context {
	return context.WithValue(ctx, keyTitle, title)
}

func withStackIDs(ctx context.Context, ids []string) context.Context {
	return context.WithValue(ctx, keyStackIDs, ids)
}

func withPath(ctx context.Context, p string) context.Context {
	return context.WithValue(ctx, keyPath, p)
}

func Data(ctx context.Context, id string) any {
	m, _ := ctx.Value(keyData).(map[string]any)
	return m[id]
}

func Path(ctx context.Context) string {
	p, _ := ctx.Value(keyPath).(string)
	return p
}

func PageTitle(ctx context.Context) string {
	t, _ := ctx.Value(keyTitle).(string)
	return t
}

func StackIDs(ctx context.Context) []string {
	ids, _ := ctx.Value(keyStackIDs).([]string)
	return ids
}

// Use runs a segment's middleware on every request in the subtree, full load
// or fragment alike. Load's data is always placed in the request context for
// any leaf to read regardless of whether this response renders this segment's
// HTML.
func Use(s Segment) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.Load != nil {
				data, err := s.Load(r.Context())
				if err != nil {
					http.Error(w, "load segment "+s.ID+": "+err.Error(), http.StatusInternalServerError)
					return
				}
				r = r.WithContext(withData(r.Context(), s.ID, data))
			}
			next.ServeHTTP(w, r.WithContext(pushSegment(r.Context(), s)))
		})
	}
}

func Page(w http.ResponseWriter, r *http.Request, title string, leaf templ.Component) {
	PageWith(w, r, title, leaf)
}

func PageWith(w http.ResponseWriter, r *http.Request, title string, leaf templ.Component, extra ...templ.Component) {
	stack := stackFrom(r)
	if len(stack) == 0 {
		_ = leaf.Render(r.Context(), w)
		return
	}

	mounted := mountedSet(r)
	i := commonPrefixLen(stack, mounted)

	if !isHX(r) || i == 0 {
		renderFull(w, r, title, stack, leaf)
		return
	}
	renderTail(w, r, title, stack, i, leaf, extra)
}

// renderFull renders the whole tree. A fragment with no shared prefix forces
// a full reload (HX-Refresh) rather than rebuilding the body (which would
// destroy #outlet-root).
func renderFull(w http.ResponseWriter, r *http.Request, title string, stack []Segment, leaf templ.Component) {
	ids := stackIDs(stack)
	ctx := withTitle(withPath(withStackIDs(r.Context(), ids), r.URL.Path), title)

	w.Header().Set("HX-Trigger", fmt.Sprintf(`{"segments":%q}`, strings.Join(ids, ",")))

	if isHX(r) {
		w.Header().Set("HX-Refresh", "true")
		return
	}

	comp := leaf
	for j := len(stack) - 1; j >= 0; j-- {
		s := stack[j]
		comp = s.Render(ctx, Data(ctx, s.ID), comp)
	}
	_ = comp.Render(ctx, w)
}

// renderTail renders only the missing tail of the stack plus the leaf.
// Fragment navigations clear an open page-modal (ClearModal OOB).
func renderTail(w http.ResponseWriter, r *http.Request, title string, stack []Segment, i int, leaf templ.Component, extra []templ.Component) {
	ids := stackIDs(stack)
	ctx := withPath(withStackIDs(r.Context(), ids), r.URL.Path)

	w.Header().Set("HX-Retarget", "#outlet-"+stack[i-1].ID)
	w.Header().Set("HX-Reswap", "innerHTML")
	w.Header().Set("HX-Push-Url", r.URL.Path)
	w.Header().Set("HX-Trigger", fmt.Sprintf(`{"segments":%q}`, strings.Join(ids, ",")))

	comp := leaf
	for j := len(stack) - 1; j >= i; j-- {
		s := stack[j]
		comp = s.Render(ctx, Data(ctx, s.ID), comp)
	}

	parts := []templ.Component{comp}
	if title != "" {
		parts = append(parts, OOBTitle(title))
	}
	parts = append(parts, ClearModal())
	parts = append(parts, extra...)
	_ = templ.Join(parts...).Render(ctx, w)
}

func OOBTitle(title string) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		_, err := io.WriteString(w, "<title hx-swap-oob=\"true\">"+templ.EscapeString(title)+"</title>")
		return err
	})
}

func ClearModal() templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		_, err := io.WriteString(w, `<div id="modal-root" hx-swap-oob="innerHTML"></div>`)
		return err
	})
}

func isHX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

func stackFrom(r *http.Request) []Segment {
	s, _ := r.Context().Value(keyStack).([]Segment)
	return s
}

func mountedSet(r *http.Request) map[string]bool {
	set := make(map[string]bool)
	for _, id := range strings.Split(r.Header.Get(headerMounted), ",") {
		if id != "" {
			set[id] = true
		}
	}
	return set
}

func commonPrefixLen(stack []Segment, mounted map[string]bool) int {
	i := 0
	for i < len(stack) && mounted[stack[i].ID] {
		i++
	}
	return i
}

func stackIDs(stack []Segment) []string {
	ids := make([]string, len(stack))
	for i, s := range stack {
		ids[i] = s.ID
	}
	return ids
}
