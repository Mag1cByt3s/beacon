// Command beacon is the beacon server: an HTTP JSON API on top of CalDAV
// that keeps the focus state (current task, skip order) in SQLite.
//
// All settings come from environment variables; see README.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/caldav"
	"github.com/Mag1cByt3s/beacon/internal/config"
	"github.com/Mag1cByt3s/beacon/internal/server"
	"github.com/Mag1cByt3s/beacon/internal/store"
)

func main() {
	// One "key=value" line per event on stderr.
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("beacon stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg := config.Load()
	if err := cfg.CheckServer(); err != nil {
		return err
	}

	password := ""
	if cfg.User != "" {
		var err error
		password, err = cfg.Password(context.Background(), os.Stderr)
		if err != nil {
			return err
		}
	}
	client, err := caldav.NewClient(cfg.CalDAVURL, cfg.User, password, cfg.Lists)
	if err != nil {
		return err
	}

	st, err := store.Open(cfg.DB)
	if err != nil {
		return err
	}
	defer st.Close()

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.New(client, st, cfg.Token, cfg.DefaultList, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	// ctx is cancelled on Ctrl-C or when the service manager stops us.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ListenAndServe blocks, so run it in a goroutine and wait for either
	// an error or a stop signal.
	errc := make(chan error, 1)
	go func() {
		log.Info("beacon listening", "addr", cfg.Listen, "db", cfg.DB, "lists", cfg.Lists, "default_list", cfg.DefaultList)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return fmt.Errorf("cannot serve on %s (check BEACON_LISTEN): %w", cfg.Listen, err)
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
