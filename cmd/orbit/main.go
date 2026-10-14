// Command orbit runs the Orbit server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Bermos/orbit/internal/agent"
	"github.com/Bermos/orbit/internal/db"
	"github.com/Bermos/orbit/internal/gitrepo"
	"github.com/Bermos/orbit/internal/runner"
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
	if err := enableAgents(ctx, srv, os.Getenv("ORBIT_AGENTS_DIR")); err != nil {
		return err
	}

	srv.StartCI(ctx)
	if err := startEmbeddedRunner(ctx, srv, os.Getenv("ORBIT_EMBEDDED_RUNNER"), addr); err != nil {
		return err
	}

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
		err := httpSrv.Shutdown(shutdown)
		// ctx is done, so running agent runs are being cancelled; let them
		// record that before the database pool closes.
		srv.WaitAgents()
		return err
	}
	return nil
}

// enableAgents loads the agent catalog (built-in agents plus files in dir)
// and the model providers. Without ANTHROPIC_API_KEY, agents that use
// Anthropic answer with the built-in test model so the flow still works.
func enableAgents(ctx context.Context, srv *server.Server, dir string) error {
	defs, problems := agent.LoadCatalog(dir)
	for _, p := range problems {
		slog.Warn("agent definition skipped", "err", p)
	}
	providers := agent.Providers{"fake": agent.Fake{}}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		providers["anthropic"] = agent.NewAnthropic("")
	} else {
		slog.Warn("ANTHROPIC_API_KEY not set; agents answer with the built-in test model")
	}
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	slog.Info("agents loaded", "agents", names)
	return srv.EnableAgents(ctx, defs, providers)
}

// startEmbeddedRunner runs a runner inside the server process, so a
// development instance runs pipelines without any setup. Only the local
// executor is supported: jobs run as processes next to Orbit, unisolated.
func startEmbeddedRunner(ctx context.Context, srv *server.Server, executor, addr string) error {
	switch executor {
	case "":
		return nil
	case "local":
	default:
		return fmt.Errorf("ORBIT_EMBEDDED_RUNNER must be local, not %q", executor)
	}
	token, err := srv.RegisterEmbeddedRunner(ctx)
	if err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("ORBIT_ADDR: %w", err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	slog.Warn("embedded runner enabled: pipeline jobs run as processes on this machine without isolation; use it for development only")
	go runner.Run(ctx, runner.Config{
		URL:      "http://" + net.JoinHostPort(host, port),
		Token:    token,
		Executor: runner.Local{},
		Version:  "embedded",
	})
	return nil
}
