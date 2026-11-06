package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nicolaegr/mnemo/internal/auth"
	"github.com/nicolaegr/mnemo/internal/config"
	"github.com/nicolaegr/mnemo/internal/jobs"
	"github.com/nicolaegr/mnemo/internal/store"
	"github.com/nicolaegr/mnemo/internal/web"
	"github.com/nicolaegr/mnemo/internal/websession"
)

func main() {
	setPassword := flag.String("set-password", "", "update the password for this login, then exit")
	flag.Parse()

	cfg := config.Load()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer st.Close()

	if err := store.Migrate(ctx, st.PG); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	users := store.NewUsers(st.PG)

	if *setPassword != "" {
		if err := resetPassword(ctx, users, *setPassword, cfg.BcryptCost); err != nil {
			log.Fatalf("set password: %v", err)
		}
		return
	}

	var count int
	if err := st.PG.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		log.Fatalf("count users: %v", err)
	}
	if count == 0 {
		log.Printf("no accounts yet; the first visit to /register creates the owner")
	}

	authCfg, err := authConfig(cfg)
	if err != nil {
		log.Fatalf("auth config: %v", err)
	}

	go jobs.Run(ctx, st.PG)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           web.New(web.Deps{Store: st, Session: sessionManager(cfg), Auth: authCfg}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("listening on http://localhost%s", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// resetPassword reads the new password from NEW_PASSWORD, or from stdin when it
// is unset.
func resetPassword(ctx context.Context, users *store.Users, login string, cost int) error {
	password := os.Getenv("NEW_PASSWORD")
	if password == "" {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		password = strings.TrimRight(line, "\r\n")
	}
	if len(password) < auth.MinPasswordLength {
		return fmt.Errorf("password is shorter than %d characters", auth.MinPasswordLength)
	}
	if err := users.SetPassword(ctx, login, password, cost); err != nil {
		return err
	}
	log.Printf("password updated for %q", login)
	return nil
}

func authConfig(cfg config.Config) (auth.Config, error) {
	ips, err := auth.NewClientIPs(cfg.TrustedProxies)
	if err != nil {
		return auth.Config{}, err
	}
	return auth.Config{
		IPs:               ips,
		FailuresPerMinute: cfg.AuthFailureLimit,
		TokenPassword:     cfg.DAVTokenPassword,
	}, nil
}

// sessionManager signs UI session cookies. Without SESSION_SECRET it falls back
// to an ephemeral key, so dev works but sessions reset on restart.
func sessionManager(cfg config.Config) *websession.Manager {
	secret := []byte(cfg.SessionSecret)
	if len(secret) == 0 {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			log.Fatalf("session secret: %v", err)
		}
		log.Printf("SESSION_SECRET unset; using an ephemeral key (sessions reset on restart)")
	}
	return websession.New(cfg.SessionName, secret, cfg.SessionTTL, cfg.CookieSecure, sameSite(cfg.CookieSameSite))
}

func sameSite(v string) http.SameSite {
	switch v {
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}
