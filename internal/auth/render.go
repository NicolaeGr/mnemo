package auth

import (
	"net/http"

	"github.com/a-h/templ"

	"mnemo/internal/views"
)

func RenderPage(w http.ResponseWriter, r *http.Request, title string, content templ.Component) {
	if r.Header.Get("HX-Request") == "true" {
		views.PageWrapper(content).Render(r.Context(), w)
	} else {
		views.AuthLayout(title, content).Render(r.Context(), w)
	}
}
