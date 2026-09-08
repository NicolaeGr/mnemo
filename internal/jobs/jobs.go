// Package jobs runs the in-process workers: an outbox dispatcher (force_resync)
// and the tombstone purge loop.
package jobs

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"example.com/segments/internal/store"
)

const (
	pollInterval   = 250 * time.Millisecond
	purgeInterval  = time.Hour
	tombstoneAge   = 48 * time.Hour
	pendingWarnAge = 60 * time.Second
	backlogWarn    = 5
)

// Run starts the dispatcher and purge workers and blocks until ctx is done.
func Run(ctx context.Context, pool *pgxpool.Pool) {
	go dispatch(ctx, pool)
	go purge(ctx, pool)
	<-ctx.Done()
}

func dispatch(ctx context.Context, pool *pgxpool.Pool) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if n, err := store.PendingEventCount(ctx, pool, pendingWarnAge); err != nil {
			log.Printf("dispatch: watchdog: %v", err)
		} else if n > backlogWarn {
			log.Printf("dispatch: %d events pending over %s", n, pendingWarnAge)
		}
		batch, err := store.ClaimEvents(ctx, pool, 16)
		if err != nil {
			log.Printf("dispatch: claim: %v", err)
			continue
		}
		for _, e := range batch {
			if err := store.ApplyEvent(ctx, pool, e); err != nil {
				// Left pending on purpose; the next poll retries it.
				log.Printf("dispatch: event %d (%s): %v", e.ID, e.Kind, err)
			}
		}
	}
}

func purge(ctx context.Context, pool *pgxpool.Pool) {
	ticker := time.NewTicker(purgeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		cutoff := time.Now().Add(-tombstoneAge)
		for {
			n, err := store.PurgeTombstones(ctx, pool, cutoff, 1000)
			if err != nil {
				log.Printf("purge: %v", err)
				break
			}
			if n < 1000 {
				break
			}
		}
	}
}
