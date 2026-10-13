// Command orbit runs the Orbit server.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Bermos/orbit/internal/db"
	"github.com/Bermos/orbit/internal/gitrepo"
	"github.com/Bermos/orbit/internal/server"
	"github.com/Bermos/orbit/web"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("orbit stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	addr := env("ORBIT_ADDR", ":8080")
	dbURL := env("ORBIT_DATABASE_URL", "postgres://orbit:orbit@localhost:5432/orbit?sslmode=disable")
	dataDir := env("ORBIT_DATA_DIR", "./data")
	baseURL := env("ORBIT_BASE_URL", "http://localhost:8080")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	repoRoot, err := filepath.Abs(filepath.Join(dataDir, "repos"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		return err
	}

	ui := web.Dist()
	if ui == nil {
		slog.Warn("web UI not built; serving the API only (run `npm run build` in web/)")
	}
	srv := server.New(pool, gitrepo.Store{Root: repoRoot}, ui, baseURL)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("orbit listening", "addr", addr, "base_url", baseURL)
		errc <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdown)
	}
	return nil
}
