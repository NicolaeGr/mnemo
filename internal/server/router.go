package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"mnemo/internal/auth"
	"mnemo/internal/db"
	"mnemo/internal/services"
	"mnemo/internal/views/pages"
)

type Router struct {
	mux                *chi.Mux
	DB                 *db.Store
	AuthMW             *auth.Middleware
	AuthHandlers       *auth.Handlers
	SessionStore       *auth.SessionStore
	UserService        *services.UserService
	AddressBookService *services.AddressBookService
	ContactService     *services.ContactService
}

func NewRouter(store *db.Store) *Router {
	r := &Router{
		mux:                chi.NewRouter(),
		DB:                 store,
		UserService:        services.NewUserService(store),
		SessionStore:       auth.NewSessionStore(),
		AddressBookService: services.NewAddressBookService(store),
		ContactService:     services.NewContactService(store),
	}

	r.AuthMW = auth.NewMiddleware(r.SessionStore, r.UserService)
	r.AuthHandlers = auth.NewHandlers(r.SessionStore, r.UserService)

	r.registerMiddleware()
	r.registerRoutes()

	return r
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

func (r *Router) registerMiddleware() {
	r.mux.Use(middleware.Logger)
	r.mux.Use(middleware.Recoverer)
	r.mux.Use(middleware.RealIP)
	r.mux.Use(HTMXMiddleware)

	// Redirect to /setup when no users exist (exclude /setup itself)
	r.mux.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path == "/setup" {
				next.ServeHTTP(w, req)
				return
			}
			r.AuthMW.RequireSetup(next).ServeHTTP(w, req)
		})
	})
}

func (r *Router) registerRoutes() {
	r.mux.Group(func(gr chi.Router) {
		gr.Use(r.AuthMW.RedirectIfAuthed)
		gr.Get("/login", r.AuthHandlers.LoginPage)
		gr.Post("/login", r.AuthHandlers.Login)
		gr.Get("/setup", r.AuthHandlers.SetupPage)
		gr.Post("/setup", r.AuthHandlers.Setup)
	})

	r.mux.Get("/logout", r.AuthHandlers.Logout)

	r.mux.Get("/invite/{token}", r.handleInviteRegisterPage)
	r.mux.Post("/invite/{token}", r.handleInviteRegister)

	// Requires valid session
	r.mux.Group(func(gr chi.Router) {
		gr.Use(r.AuthMW.RequireAuth)

		gr.Get("/", r.handleIndex)
		gr.Get("/users", r.handleUsersList)

		// Self-service settings
		gr.Get("/settings/account", r.handleSettingsAccount)
		gr.Get("/settings/keys", r.handleDeviceKeys)
		gr.Post("/settings/keys", r.handleDeviceKeysCreate)
		gr.Post("/settings/keys/{keyId}/delete", r.handleDeviceKeysDelete)

		// Address Books
		gr.Get("/address-books", r.handleAddressBooksList)
		gr.Get("/address-books/new", r.handleAddressBookNew)
		gr.Post("/address-books", r.handleAddressBookCreate)
		gr.Delete("/address-books/{id}", r.handleAddressBookDelete)

		// Contacts
		gr.Get("/address-books/{id}/contacts", r.handleContactsList)
		gr.Get("/address-books/{id}/contacts/new", r.handleContactNew)
		gr.Post("/address-books/{id}/contacts", r.handleContactCreate)
		gr.Get("/address-books/{id}/contacts/{contactId}", r.handleContactView)
		gr.Get("/address-books/{id}/contacts/{contactId}/edit", r.handleContactEdit)
		gr.Post("/address-books/{id}/contacts/{contactId}", r.handleContactUpdate)
		gr.Delete("/address-books/{id}/contacts/{contactId}", r.handleContactDelete)
		gr.Get("/address-books/{id}/export", r.handleContactsExport)
		gr.Get("/address-books/{id}/import", r.handleContactsImportForm)
		gr.Post("/address-books/{id}/import", r.handleContactsImport)

		// Admin only
		gr.Group(func(admin chi.Router) {
			admin.Use(r.AuthMW.RequireAdmin)

			// User management (admin actions)
			admin.Get("/users/new", r.handleUsersNew)
			admin.Post("/users", r.handleUsersCreate)
			admin.Patch("/users/{id}/toggle-active", r.handleUsersToggleActive)

			// Invite management
			admin.Post("/invites", r.handleInviteCreate)
		})
	})
}

func (r *Router) handleIndex(w http.ResponseWriter, req *http.Request) {
	RenderPage(w, req, "Dashboard", pages.Dashboard())
}
