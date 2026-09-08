package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nicolaegr/mnemo/internal/config"
	"github.com/nicolaegr/mnemo/internal/jobs"
	"github.com/nicolaegr/mnemo/internal/store"
	"github.com/nicolaegr/mnemo/internal/web"
)

func main() {
	cfg := config.Load()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL, cfg.RedisAddr)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer st.Close()

	if err := store.Migrate(ctx, st.PG); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	users := store.NewUsers(st.PG)
	var count int
	if err := st.PG.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		log.Fatalf("count users: %v", err)
	}
	if count == 0 {
		if _, _, err := users.Signup(ctx, "admin", "admin@example.com", "Admin", "password", cfg.BcryptCost); err != nil {
			log.Fatalf("bootstrap signup: %v", err)
		}
	}

	go jobs.Run(ctx, st.PG)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           web.New(web.Deps{Store: st}),
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
