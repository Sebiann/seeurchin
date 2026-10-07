// Command seeurchin runs the group movie/show picker server: it serves the
// REST + SSE API and the embedded SvelteKit frontend, backed by SQLite and a
// Jellyfin library.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/enderu/seeurchin/internal/auth"
	"github.com/enderu/seeurchin/internal/config"
	"github.com/enderu/seeurchin/internal/httpapi"
	"github.com/enderu/seeurchin/internal/jellyfin"
	"github.com/enderu/seeurchin/internal/poll"
	"github.com/enderu/seeurchin/internal/seerr"
	"github.com/enderu/seeurchin/internal/store"
)

// itemResolver adapts the Jellyfin client to the poll.ItemResolver interface,
// keeping the domain package free of a Jellyfin dependency.
type itemResolver struct{ jf *jellyfin.Client }

func (r itemResolver) GetItem(ctx context.Context, id string) (*poll.ResolvedItem, error) {
	it, err := r.jf.GetItem(ctx, id)
	if err != nil || it == nil {
		return nil, err
	}
	return &poll.ResolvedItem{
		ID:       it.ID,
		Title:    it.Name,
		Year:     it.ProductionYear,
		Type:     it.Type,
		Runtime:  it.RuntimeMinutes(),
		Overview: it.Overview,
		ImageTag: it.PrimaryImageTag(),
		Genres:   it.Genres,
	}, nil
}

func main() {
	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.SessionSecretGenerated {
		log.Printf("warning: SEEURCHIN_SESSION_SECRET not set; using an ephemeral secret (sessions reset on restart)")
	}

	if err := dropPrivileges(cfg); err != nil {
		log.Fatalf("drop privileges: %v", err)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open database %q: %v", cfg.DBPath, err)
	}
	defer st.Close()

	jf := jellyfin.New(cfg.Jellyfin.URL, cfg.Jellyfin.APIKey)
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := jf.Ping(pingCtx); err != nil {
		log.Printf("warning: cannot reach Jellyfin at %s: %v", cfg.Jellyfin.URL, err)
	}
	cancel()

	var sr *seerr.Client
	if cfg.Seerr.Enabled() {
		sr = seerr.New(cfg.Seerr.URL, cfg.Seerr.APIKey)
		log.Printf("Seerr enabled at %s — write-ins and winner auto-request available", cfg.Seerr.URL)
	}

	svc := poll.NewService(st, itemResolver{jf}, 0)
	sessions := auth.NewSessions(cfg.SessionSecret)
	hub := httpapi.NewHub()
	srv := httpapi.NewServer(cfg, svc, st, jf, sr, sessions, hub)

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Background sweeper: advances polls whose round timers have run out. Runs
	// once per process (the SSE hub isn't multi-instance safe).
	sweepCtx, stopSweeper := context.WithCancel(context.Background())
	defer stopSweeper()
	go srv.RunTimerSweeper(sweepCtx, time.Second)

	if cfg.AdminEnabled() {
		log.Printf("admin dashboard enabled at %s/admin", cfg.BaseURL)
	}
	// Retention sweeper: opt-in, purges closed polls older than the configured
	// window. Off by default (history is kept forever).
	if cfg.PollRetentionDays > 0 {
		log.Printf("poll retention: purging closed polls older than %d days", cfg.PollRetentionDays)
		go srv.RunRetentionSweeper(sweepCtx, time.Hour)
	}

	go func() {
		log.Printf("seeurchin listening on %s (public base URL %s)", cfg.Addr, cfg.BaseURL)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
}
