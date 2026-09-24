// Command sharecodex-server is the central server: the sync API used by the
// desktop app and the web admin console under /admin.
//
// Configuration comes from the environment:
//
//	DATABASE_URL    Postgres connection string (required)
//	ADMIN_PASSWORD  password for the web admin console, at least 12 characters (required)
//	PUBLIC_URL      base URL members reach the server at, used in join links (required)
//	LISTEN_ADDR     address to listen on (default :8080)
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

	"github.com/KoukeNeko/ShareCodex/internal/server/admin"
	"github.com/KoukeNeko/ShareCodex/internal/server/httpapi"
	"github.com/KoukeNeko/ShareCodex/internal/server/storage"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "sharecodex-server:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is not set")
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	store, err := storage.Open(ctx, url)
	if err != nil {
		return err
	}
	defer store.Close()

	console, err := admin.New(store, log, admin.Config{
		Password:  os.Getenv("ADMIN_PASSWORD"),
		PublicURL: os.Getenv("PUBLIC_URL"),
	})
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/admin/", console)
	mux.Handle("GET /{$}", http.RedirectHandler("/admin/", http.StatusSeeOther))
	mux.Handle("/", httpapi.New(store, log))

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	log.Info("listening", "addr", addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
