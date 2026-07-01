package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"mnemo/internal/auth"
	"mnemo/internal/services"
	"mnemo/internal/views/pages"
)

func (r *Router) handleUsersList(w http.ResponseWriter, req *http.Request) {
	users, err := r.UserService.ListUsers(req.Context())
	if err != nil {
		http.Error(w, "Failed to load users", http.StatusInternalServerError)
		return
	}
	sess := auth.UserFromContext(req.Context())
	showAdmin := sess != nil && sess.Role == "admin"
	RenderPage(w, req, "Users", pages.UsersList(users, showAdmin))
}

func (r *Router) handleUsersNew(w http.ResponseWriter, req *http.Request) {
	// HTMX: render form in modal; full page otherwise
	if req.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Retarget", "#modal-container")
		pages.UserForm("").Render(req.Context(), w)
		return
	}
	RenderPage(w, req, "New User", pages.UserForm(""))
}

func (r *Router) handleUsersCreate(w http.ResponseWriter, req *http.Request) {
	if err := req.ParseForm(); err != nil {
		renderFormWithError(w, req, "Invalid form submission")
		return
	}

	username := req.FormValue("username")
	password := req.FormValue("password")
	role := req.FormValue("role")

	if username == "" || password == "" {
		renderFormWithError(w, req, "All fields are required")
		return
	}
	if len(password) < 8 {
		renderFormWithError(w, req, "Password must be at least 8 characters")
		return
	}

	_, err := r.UserService.CreateUser(req.Context(), username, password, role == "admin")
	if err != nil {
		if errors.Is(err, services.ErrUsernameTaken) {
			renderFormWithError(w, req, "Username already taken")
			return
		}
		renderFormWithError(w, req, "Failed to create user")
		return
	}

	if req.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/users")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, req, "/users", http.StatusSeeOther)
}

func (r *Router) handleUsersToggleActive(w http.ResponseWriter, req *http.Request) {
	userID := chi.URLParam(req, "id")

	user, err := r.UserService.ToggleUserActive(req.Context(), userID)
	if err != nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	pages.UserRow(user, true).Render(req.Context(), w)
}

func wrapSettings(active string, content templ.Component, modal bool) templ.Component {
	return pages.SettingsPage(active, modal, content)
}

func (r *Router) renderModalOrPage(w http.ResponseWriter, req *http.Request, title string, sizeClass string, active string, content templ.Component) {
	if req.Header.Get("HX-Target") == "modal-container" {
		w.Header().Set("HX-Push-Url", req.URL.RequestURI())
		pageContent := wrapSettings(active, content, true)
		pages.Modal(title, pages.ModalOpts{ShowClose: true, ShowExpand: true, ExpandURL: req.URL.RequestURI(), SizeClass: sizeClass, AspectRatio: "aspect-[16/10]", MaxHeight: "max-h-[min(960px,90vh)]"}).Render(templ.WithChildren(req.Context(), pageContent), w)
		return
	}
	RenderSettingsPage(w, req, title, wrapSettings(active, content, false))
}

func (r *Router) handleSettingsAccount(w http.ResponseWriter, req *http.Request) {
	sess := auth.UserFromContext(req.Context())
	if sess == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	r.renderModalOrPage(w, req, "Account", "max-w-[90vw]", "account", pages.SettingsAccount(sess.Username))
}

func (r *Router) handleDeviceKeys(w http.ResponseWriter, req *http.Request) {
	sess := auth.UserFromContext(req.Context())
	if sess == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	keys, err := r.UserService.ListDeviceTokens(req.Context(), sess.UserID)
	if err != nil {
		http.Error(w, "Failed to load device keys", http.StatusInternalServerError)
		return
	}

	r.renderModalOrPage(w, req, "Device Keys", "max-w-[90vw]", "keys", pages.DeviceKeysPage(keys, "", ""))
}

func (r *Router) handleDeviceKeysCreate(w http.ResponseWriter, req *http.Request) {
	sess := auth.UserFromContext(req.Context())
	if sess == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if err := req.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	deviceName := req.FormValue("device_name")
	if deviceName == "" {
		keys, _ := r.UserService.ListDeviceTokens(req.Context(), sess.UserID)
		r.renderModalOrPage(w, req, "Device Keys", "max-w-[90vw]", "keys", pages.DeviceKeysPage(keys, "", "Device name is required"))
		return
	}

	_, rawKey, err := r.UserService.CreateDeviceToken(req.Context(), sess.UserID, deviceName)
	if err != nil {
		keys, _ := r.UserService.ListDeviceTokens(req.Context(), sess.UserID)
		r.renderModalOrPage(w, req, "Device Keys", "max-w-[90vw]", "keys", pages.DeviceKeysPage(keys, "", "Failed to create device key"))
		return
	}

	keys, _ := r.UserService.ListDeviceTokens(req.Context(), sess.UserID)
	r.renderModalOrPage(w, req, "Device Keys", "max-w-[90vw]", "keys", pages.DeviceKeysPage(keys, rawKey, ""))
}

func (r *Router) handleDeviceKeysDelete(w http.ResponseWriter, req *http.Request) {
	sess := auth.UserFromContext(req.Context())
	if sess == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	keyID := chi.URLParam(req, "keyId")

	if err := r.UserService.DeleteDeviceToken(req.Context(), keyID, sess.UserID); err != nil {
		http.Error(w, "Device key not found", http.StatusNotFound)
		return
	}

	http.Redirect(w, req, "/settings/keys", http.StatusSeeOther)
}

func (r *Router) handleInviteCreate(w http.ResponseWriter, req *http.Request) {

	sess := auth.UserFromContext(req.Context())
	if sess == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	invite, err := r.UserService.CreateInvite(req.Context(), sess.UserID, 7*24*time.Hour) // 7 days
	if err != nil {
		http.Error(w, "Failed to create invite", http.StatusInternalServerError)
		return
	}

	link := "/invite/" + invite.Token
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<div class="p-3 rounded bg-green-50 border border-green-200 text-green-700 text-sm"><strong>Invite link created:</strong> <a href="` + link + `" class="underline font-mono">` + link + `</a></div>`))
}

func (r *Router) handleInviteRegisterPage(w http.ResponseWriter, req *http.Request) {
	token := chi.URLParam(req, "token")

	_, err := r.UserService.GetInvite(req.Context(), token)
	if err != nil {
		auth.RenderPage(w, req, "Invalid Invite", pages.InviteRegister("This invite link is invalid or has expired."))
		return
	}

	auth.RenderPage(w, req, "Register", pages.InviteRegister(""))
}

func (r *Router) handleInviteRegister(w http.ResponseWriter, req *http.Request) {
	token := chi.URLParam(req, "token")

	if err := req.ParseForm(); err != nil {
		auth.RenderPage(w, req, "Register", pages.InviteRegister("Invalid form submission"))
		return
	}

	username := req.FormValue("username")
	password := req.FormValue("password")
	confirm := req.FormValue("confirm_password")

	if username == "" || password == "" {
		auth.RenderPage(w, req, "Register", pages.InviteRegister("All fields are required"))
		return
	}
	if password != confirm {
		auth.RenderPage(w, req, "Register", pages.InviteRegister("Passwords do not match"))
		return
	}
	if len(password) < 8 {
		auth.RenderPage(w, req, "Register", pages.InviteRegister("Password must be at least 8 characters"))
		return
	}

	_, err := r.UserService.GetInvite(req.Context(), token)
	if err != nil {
		auth.RenderPage(w, req, "Register", pages.InviteRegister("This invite link is invalid or has expired."))
		return
	}

	user, err := r.UserService.CreateUser(req.Context(), username, password, false)
	if err != nil {
		if errors.Is(err, services.ErrUsernameTaken) {
			auth.RenderPage(w, req, "Register", pages.InviteRegister("Username already taken"))
			return
		}
		auth.RenderPage(w, req, "Register", pages.InviteRegister("Failed to create account"))
		return
	}

	if err := r.UserService.UseInvite(req.Context(), token, user.ID); err != nil {
		// Non-fatal — user was created, just log
	}

	sessToken, _, err := r.SessionStore.Create(user.ID, user.Username, user.Role)
	if err == nil {
		auth.SetSessionCookie(w, sessToken)
	}

	http.Redirect(w, req, "/", http.StatusSeeOther)
}

func renderFormWithError(w http.ResponseWriter, req *http.Request, errMsg string) {
	if req.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Retarget", "#modal-container")
		pages.UserForm(errMsg).Render(req.Context(), w)
		return
	}
	RenderPage(w, req, "New User", pages.UserForm(errMsg))
}
