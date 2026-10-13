// Package server wires Orbit's HTTP API, git endpoints, WebSocket events and
// the web UI together.
package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Bermos/orbit/internal/access"
	"github.com/Bermos/orbit/internal/gitrepo"
	"github.com/Bermos/orbit/internal/realtime"
)

// Server holds Orbit's dependencies.
type Server struct {
	DB     *pgxpool.Pool
	Git    gitrepo.Store
	Hub    *realtime.Hub
	Access access.Resolver
	// Web is the built web UI. Nil disables UI serving (API only).
	Web fs.FS
	// BaseURL is the externally visible URL, used for clone URLs.
	BaseURL string

	agents *agentService
	// internal serves the authenticated API in-process, so agent tools and
	// confirmed proposals go through exactly the checks people go through.
	internal http.Handler
}

// New creates a Server.
func New(db *pgxpool.Pool, git gitrepo.Store, web fs.FS, baseURL string) *Server {
	s := &Server{
		DB:      db,
		Git:     git,
		Hub:     realtime.NewHub(),
		Access:  access.Resolver{DB: db},
		Web:     web,
		BaseURL: strings.TrimRight(baseURL, "/"),
	}
	internal := chi.NewRouter()
	internal.Route("/api/v1", s.userRoutes)
	s.internal = internal
	return s
}

// Routes builds the HTTP handler.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.DB.Ping(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})

	r.Handle("/git/*", http.HandlerFunc(s.serveGit))

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(middleware.Timeout(30 * time.Second))
		r.Post("/auth/register", s.handle(s.register))
		r.Post("/auth/login", s.handle(s.login))

		r.Group(func(r chi.Router) {
			r.Use(s.requireUser)
			s.userRoutes(r)
			r.Get("/events", s.serveEvents)
		})
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		})
	})

	if s.Web != nil {
		r.NotFound(s.serveWeb)
	}
	return r
}

// userRoutes are the API endpoints for a signed-in user.
func (s *Server) userRoutes(r chi.Router) {
	r.Post("/auth/logout", s.handle(s.logout))
	r.Get("/me", s.handle(s.me))
	r.Get("/me/teams", s.handle(s.myTeams))
	r.Get("/users", s.handle(s.listUsers))

	r.Get("/teams", s.handle(s.listTeams))
	r.Post("/teams", s.handle(s.createTeam))
	r.Get("/teams/{teamID}", s.handle(s.getTeam))
	r.Patch("/teams/{teamID}", s.handle(s.updateTeam))
	r.Get("/teams/{teamID}/space", s.handle(s.teamSpace))
	r.Put("/teams/{teamID}/members/{userID}", s.handle(s.putTeamMember))
	r.Delete("/teams/{teamID}/members/{userID}", s.handle(s.deleteTeamMember))

	r.Get("/apps", s.handle(s.listApps))
	r.Post("/apps", s.handle(s.createApp))
	r.Get("/apps/{app}", s.handle(s.getApp))
	r.Patch("/apps/{app}", s.handle(s.updateApp))
	r.Put("/apps/{app}/teams/{teamID}", s.handle(s.putAppTeam))
	r.Delete("/apps/{app}/teams/{teamID}", s.handle(s.deleteAppTeam))

	r.Post("/apps/{app}/repos", s.handle(s.createRepo))
	r.Get("/apps/{app}/repos/{repo}", s.handle(s.getRepo))
	r.Get("/apps/{app}/repos/{repo}/tree", s.handle(s.repoTree))
	r.Get("/apps/{app}/repos/{repo}/blob", s.handle(s.repoBlob))
	r.Get("/apps/{app}/repos/{repo}/commits", s.handle(s.repoCommits))

	r.Get("/apps/{app}/issues", s.handle(s.listIssues))
	r.Post("/apps/{app}/issues", s.handle(s.createIssue))
	r.Get("/apps/{app}/issues/{number}", s.handle(s.getIssue))
	r.Patch("/apps/{app}/issues/{number}", s.handle(s.updateIssue))

	r.Get("/channels", s.handle(s.listChannels))
	r.Post("/channels", s.handle(s.createChannel))
	r.Get("/channels/{channelID}", s.handle(s.getChannel))
	r.Get("/channels/{channelID}/messages", s.handle(s.listMessages))
	r.Post("/channels/{channelID}/messages", s.handle(s.postMessage))
	r.Get("/messages/{messageID}", s.handle(s.getMessage))

	r.Get("/todos", s.handle(s.listTodos))
	r.Post("/todos", s.handle(s.createTodo))
	r.Patch("/todos/{todoID}", s.handle(s.updateTodo))
	r.Delete("/todos/{todoID}", s.handle(s.deleteTodo))

	r.Get("/links", s.handle(s.listLinks))
	r.Post("/links", s.handle(s.createLink))

	r.Get("/search", s.handle(s.search))

	r.Get("/agents", s.handle(s.listAgents))
	r.Get("/agent-runs/{runID}", s.handle(s.getAgentRun))
	r.Post("/agent-proposals/{proposalID}/apply", s.handle(s.applyProposal))
	r.Post("/agent-proposals/{proposalID}/dismiss", s.handle(s.dismissProposal))
}

// serveWeb serves the single-page app, falling back to index.html for
// client-side routes.
func (s *Server) serveWeb(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p != "" {
		if f, err := fs.Stat(s.Web, p); err == nil && !f.IsDir() {
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeFileFS(w, r, s.Web, p)
			return
		}
	}
	index, err := fs.ReadFile(s.Web, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(index)
}

// apiError carries an HTTP status to the client.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func errStatus(status int, msg string) error { return &apiError{status: status, msg: msg} }

var (
	errForbidden = errStatus(http.StatusForbidden, "you do not have permission to do this")
	errNotFound  = errStatus(http.StatusNotFound, "not found")
)

func badRequest(msg string) error { return errStatus(http.StatusBadRequest, msg) }

// handle adapts a handler that returns an error into an http.HandlerFunc.
func (s *Server) handle(fn func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := fn(w, r)
		if err == nil {
			return
		}
		var ae *apiError
		var pgErr *pgconn.PgError
		switch {
		case errors.As(err, &ae):
			writeJSON(w, ae.status, map[string]string{"error": ae.msg})
		case errors.Is(err, pgx.ErrNoRows):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		case errors.As(err, &pgErr) && pgErr.Code == "23505":
			writeJSON(w, http.StatusConflict, map[string]string{"error": "already exists"})
		case errors.As(err, &pgErr) && (pgErr.Code == "23514" || pgErr.Code == "22P02"):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid value"})
		case errors.As(err, &pgErr) && pgErr.Code == "23503":
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "referenced item does not exist or is still in use"})
		default:
			slog.Error("request failed", "path", r.URL.Path, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encode response", "err", err)
	}
}

// readJSON decodes the request body. Requiring a JSON content type means a
// plain HTML form on another site cannot submit to the API with our cookie.
func readJSON(r *http.Request, v any) error {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "application/json" {
		return errStatus(http.StatusUnsupportedMediaType, "expected application/json")
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("invalid JSON: " + err.Error())
	}
	return nil
}

// requireRole checks the current user's role on an application.
func (s *Server) requireRole(r *http.Request, app string, min access.Role) (access.Role, error) {
	u := currentUser(r)
	role, err := s.Access.AppRole(r.Context(), u.ID, app)
	if err != nil {
		return access.None, err
	}
	if role < min {
		return role, errForbidden
	}
	return role, nil
}

func strings80(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > 80 {
		return string(r[:79]) + "…"
	}
	return s
}
