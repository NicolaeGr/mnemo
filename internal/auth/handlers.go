package auth

import (
	"errors"
	"net/http"

	"mnemo/internal/services"
	"mnemo/internal/views/pages"
)

type Handlers struct {
	Sessions    *SessionStore
	UserService *services.UserService
}

func NewHandlers(sessions *SessionStore, userSvc *services.UserService) *Handlers {
	return &Handlers{Sessions: sessions, UserService: userSvc}
}

func (h *Handlers) renderOrRedirect(w http.ResponseWriter, r *http.Request, dest string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", dest)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func (h *Handlers) LoginPage(w http.ResponseWriter, r *http.Request) {
	RenderPage(w, r, "Login", pages.LoginForm(""))
}

func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		RenderPage(w, r, "Login", pages.LoginForm("Invalid form submission"))
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	if username == "" || password == "" {
		RenderPage(w, r, "Login", pages.LoginForm("Username and password are required"))
		return
	}

	user, err := h.UserService.AuthenticateUser(r.Context(), username, password)
	if err != nil {
		if errors.Is(err, services.ErrInvalidCreds) || errors.Is(err, services.ErrUserInactive) {
			RenderPage(w, r, "Login", pages.LoginForm("Invalid username or password"))
			return
		}
		RenderPage(w, r, "Login", pages.LoginForm("An error occurred. Please try again."))
		return
	}

	token, _, err := h.Sessions.Create(user.ID, user.Username, user.Role)
	if err != nil {
		RenderPage(w, r, "Login", pages.LoginForm("Failed to create session"))
		return
	}

	SetSessionCookie(w, token)
	h.renderOrRedirect(w, r, "/")
}

func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	token, err := GetSessionToken(r)
	if err == nil && token != "" {
		h.Sessions.Delete(token)
	}
	ClearSessionCookie(w)
	h.renderOrRedirect(w, r, "/login")
}

func (h *Handlers) SetupPage(w http.ResponseWriter, r *http.Request) {
	count, err := h.UserService.CountUsers(r.Context())
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if count > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	RenderPage(w, r, "Setup", pages.SetupForm(""))
}

func (h *Handlers) Setup(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		RenderPage(w, r, "Setup", pages.SetupForm("Invalid form submission"))
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")
	confirm := r.FormValue("confirm_password")

	if username == "" || password == "" {
		RenderPage(w, r, "Setup", pages.SetupForm("All fields are required"))
		return
	}

	if password != confirm {
		RenderPage(w, r, "Setup", pages.SetupForm("Passwords do not match"))
		return
	}

	if len(password) < 8 {
		RenderPage(w, r, "Setup", pages.SetupForm("Password must be at least 8 characters"))
		return
	}

	user, err := h.UserService.CreateUser(r.Context(), username, password, true)
	if err != nil {
		if errors.Is(err, services.ErrUsernameTaken) {
			RenderPage(w, r, "Setup", pages.SetupForm("Username already taken"))
			return
		}
		RenderPage(w, r, "Setup", pages.SetupForm("Failed to create user"))
		return
	}

	token, _, err := h.Sessions.Create(user.ID, user.Username, user.Role)
	if err != nil {
		RenderPage(w, r, "Setup", pages.SetupForm("Account created but login failed"))
		return
	}

	SetSessionCookie(w, token)
	h.renderOrRedirect(w, r, "/")
}
