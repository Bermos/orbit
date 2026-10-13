package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/Bermos/orbit/internal/access"
	"github.com/Bermos/orbit/internal/gitrepo"
)

type Repo struct {
	ID            string    `json:"id"`
	AppKey        string    `json:"app_key"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	DefaultBranch string    `json:"default_branch"`
	CreatedAt     time.Time `json:"created_at"`
}

const repoCols = `r.id, r.app_key, r.name, r.description, r.default_branch, r.created_at`

func scanRepos(rows pgx.Rows) ([]Repo, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Repo, error) {
		var x Repo
		err := row.Scan(&x.ID, &x.AppKey, &x.Name, &x.Description, &x.DefaultBranch, &x.CreatedAt)
		return x, err
	})
}

func (s *Server) loadRepo(r *http.Request, min access.Role) (Repo, error) {
	app, name := chi.URLParam(r, "app"), chi.URLParam(r, "repo")
	if _, err := s.requireRole(r, app, min); err != nil {
		return Repo{}, err
	}
	rows, err := s.DB.Query(r.Context(), `select `+repoCols+` from repositories r where r.app_key = $1 and r.name = $2`, app, name)
	if err != nil {
		return Repo{}, err
	}
	repos, err := scanRepos(rows)
	if err != nil {
		return Repo{}, err
	}
	if len(repos) == 0 {
		return Repo{}, errNotFound
	}
	return repos[0], nil
}

func (s *Server) createRepo(w http.ResponseWriter, r *http.Request) error {
	app := chi.URLParam(r, "app")
	var in struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	in.Name = strings.ToLower(strings.TrimSpace(in.Name))
	if in.DefaultBranch == "" {
		in.DefaultBranch = "main"
	}
	if strings.ContainsAny(in.DefaultBranch, " ~^:?*[\\") || strings.HasPrefix(in.DefaultBranch, "-") {
		return badRequest("invalid branch name")
	}
	if _, err := s.requireRole(r, app, access.Maintainer); err != nil {
		return err
	}
	ctx := r.Context()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `insert into repositories (app_key, name, description, default_branch) values ($1, $2, $3, $4)
		returning `+strings.ReplaceAll(repoCols, "r.", ""), app, in.Name, in.Description, in.DefaultBranch)
	if err != nil {
		return err
	}
	repos, err := scanRepos(rows)
	if err != nil {
		return err
	}
	if err := s.Git.Create(ctx, app, in.Name, in.DefaultBranch); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, s.repoView(r, repos[0], nil))
	return nil
}

func (s *Server) repoView(r *http.Request, repo Repo, branches []gitrepo.Branch) map[string]any {
	if branches == nil {
		branches = []gitrepo.Branch{}
	}
	return map[string]any{
		"repository": repo,
		"clone_url":  s.BaseURL + "/git/" + repo.AppKey + "/" + repo.Name + ".git",
		"branches":   branches,
		"empty":      len(branches) == 0,
	}
}

func (s *Server) getRepo(w http.ResponseWriter, r *http.Request) error {
	repo, err := s.loadRepo(r, access.Reader)
	if err != nil {
		return err
	}
	branches, err := s.Git.Branches(r.Context(), repo.AppKey, repo.Name)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.repoView(r, repo, branches))
	return nil
}

// resolveRef picks the requested ref, or the default branch.
func (s *Server) resolveRef(r *http.Request, repo Repo) (string, error) {
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = repo.DefaultBranch
	}
	sha, err := s.Git.ResolveCommit(r.Context(), repo.AppKey, repo.Name, ref)
	if errors.Is(err, gitrepo.ErrNotFound) {
		return "", errStatus(http.StatusNotFound, "ref not found")
	}
	return sha, err
}

func gitErr(err error) error {
	if errors.Is(err, gitrepo.ErrNotFound) {
		return errNotFound
	}
	return err
}

func (s *Server) repoTree(w http.ResponseWriter, r *http.Request) error {
	repo, err := s.loadRepo(r, access.Reader)
	if err != nil {
		return err
	}
	sha, err := s.resolveRef(r, repo)
	if err != nil {
		return err
	}
	path := r.URL.Query().Get("path")
	entries, err := s.Git.Tree(r.Context(), repo.AppKey, repo.Name, sha, path)
	if err != nil {
		return gitErr(err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"commit": sha, "path": strings.Trim(path, "/"), "entries": entries})
	return nil
}

func (s *Server) repoBlob(w http.ResponseWriter, r *http.Request) error {
	repo, err := s.loadRepo(r, access.Reader)
	if err != nil {
		return err
	}
	sha, err := s.resolveRef(r, repo)
	if err != nil {
		return err
	}
	path := strings.Trim(r.URL.Query().Get("path"), "/")
	content, binary, err := s.Git.Blob(r.Context(), repo.AppKey, repo.Name, sha, path)
	if err != nil {
		return gitErr(err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"commit": sha, "path": path, "content": content, "binary": binary})
	return nil
}

func (s *Server) repoCommits(w http.ResponseWriter, r *http.Request) error {
	repo, err := s.loadRepo(r, access.Reader)
	if err != nil {
		return err
	}
	sha, err := s.resolveRef(r, repo)
	if err != nil {
		return err
	}
	commits, err := s.Git.Log(r.Context(), repo.AppKey, repo.Name, sha, 50)
	if err != nil {
		return gitErr(err)
	}
	writeJSON(w, http.StatusOK, commits)
	return nil
}

// serveGit handles clone, fetch and push over HTTPS at
// /git/<app>/<repo>.git/... with basic auth (username and password, or
// username and a session token).
func (s *Server) serveGit(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/git/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 3 || !strings.HasSuffix(parts[1], ".git") {
		http.NotFound(w, r)
		return
	}
	app, name := parts[0], strings.TrimSuffix(parts[1], ".git")

	username, password, ok := r.BasicAuth()
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="Orbit"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	u, err := s.userByPassword(r.Context(), username, password)
	if err != nil {
		if su, serr := s.userBySession(r.Context(), password); serr == nil && su.Username == strings.ToLower(username) {
			u, err = su, nil
		}
	}
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="Orbit"`)
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	var exists bool
	if err := s.DB.QueryRow(r.Context(), `select exists (select 1 from repositories where app_key = $1 and name = $2)`, app, name).Scan(&exists); err != nil || !exists {
		http.NotFound(w, r)
		return
	}

	push := r.URL.Query().Get("service") == "git-receive-pack" || strings.HasSuffix(r.URL.Path, "/git-receive-pack")
	need := access.Reader
	if push {
		need = access.Developer
	}
	role, err := s.Access.AppRole(r.Context(), u.ID, app)
	if err != nil || role < need {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	s.Git.HTTPBackend(w, r, "/"+app+"/"+name+".git/"+parts[2], u.Username)
}
