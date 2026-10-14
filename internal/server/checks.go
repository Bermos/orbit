package server

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/Bermos/orbit/internal/access"
	"github.com/Bermos/orbit/internal/gitrepo"
)

// The checks API is how any CI reports on a commit: an external system
// (GitLab CI, Jenkins, GitHub Actions, ...) with a personal access token, and
// Orbit's own pipelines, which write one check per job. A check is keyed by
// its name, so reporting "build" again updates the same check.

// Check is one named result for one commit.
type Check struct {
	ID         string    `json:"id"`
	CommitSHA  string    `json:"commit"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Summary    string    `json:"summary"`
	DetailsURL string    `json:"details_url"`
	JobID      *string   `json:"job_id"`
	Author     *Author   `json:"author"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

var checkStatuses = map[string]bool{"pending": true, "running": true, "success": true, "failure": true, "error": true, "cancelled": true, "skipped": true}

var checkNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._:/()-]{0,99}$`)

const checkCols = `c.id, c.commit_sha, c.name, c.status, c.summary, c.details_url, c.job_id, u.id, u.username, u.display_name, u.kind, c.created_at, c.updated_at`

func scanChecks(rows pgx.Rows) ([]Check, error) {
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Check, error) {
		var c Check
		var uid, uname, dname, kind *string
		err := row.Scan(&c.ID, &c.CommitSHA, &c.Name, &c.Status, &c.Summary, &c.DetailsURL, &c.JobID, &uid, &uname, &dname, &kind, &c.CreatedAt, &c.UpdatedAt)
		if uid != nil {
			c.Author = &Author{ID: *uid, Username: *uname, DisplayName: *dname, Kind: *kind}
		}
		return c, err
	})
	if out == nil {
		out = []Check{}
	}
	return out, err
}

// combinedState sums up a commit's checks the way a commit list shows them:
// "" without checks, otherwise failure, pending, cancelled or success.
func combinedState(checks []Check) string {
	if len(checks) == 0 {
		return ""
	}
	has := map[string]bool{}
	for _, c := range checks {
		has[c.Status] = true
	}
	switch {
	case has["failure"] || has["error"]:
		return "failure"
	case has["pending"] || has["running"]:
		return "pending"
	case has["cancelled"]:
		return "cancelled"
	}
	return "success"
}

func (s *Server) commitChecks(ctx context.Context, repoID, sha string) ([]Check, error) {
	rows, err := s.DB.Query(ctx, `select `+checkCols+` from checks c left join users u on u.id = c.created_by
		where c.repo_id = $1 and c.commit_sha = $2 order by c.name`, repoID, sha)
	if err != nil {
		return nil, err
	}
	return scanChecks(rows)
}

// commitStates returns the combined state of each commit that has checks.
func (s *Server) commitStates(ctx context.Context, repoID string, shas []string) (map[string]string, error) {
	rows, err := s.DB.Query(ctx, `select `+checkCols+` from checks c left join users u on u.id = c.created_by
		where c.repo_id = $1 and c.commit_sha = any($2)`, repoID, shas)
	if err != nil {
		return nil, err
	}
	checks, err := scanChecks(rows)
	if err != nil {
		return nil, err
	}
	by := map[string][]Check{}
	for _, c := range checks {
		by[c.CommitSHA] = append(by[c.CommitSHA], c)
	}
	out := map[string]string{}
	for sha, cs := range by {
		out[sha] = combinedState(cs)
	}
	return out, nil
}

// commitParam resolves the {sha} URL parameter (a commit SHA or any ref) to
// a full commit SHA in the repository.
func (s *Server) commitParam(r *http.Request, repo Repo) (string, error) {
	ref := chi.URLParam(r, "sha")
	sha, err := s.Git.ResolveCommit(r.Context(), repo.AppKey, repo.Name, ref)
	if err != nil {
		return "", errStatus(http.StatusNotFound, "commit not found")
	}
	return sha, nil
}

func (s *Server) listChecks(w http.ResponseWriter, r *http.Request) error {
	repo, err := s.loadRepo(r, access.Reader)
	if err != nil {
		return err
	}
	sha, err := s.commitParam(r, repo)
	if err != nil {
		return err
	}
	checks, err := s.commitChecks(r.Context(), repo.ID, sha)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"commit": sha, "state": combinedState(checks), "checks": checks})
	return nil
}

// CheckInput is what a CI system reports.
type CheckInput struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Summary    string `json:"summary"`
	DetailsURL string `json:"details_url"`
}

func (in *CheckInput) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if !checkNameRE.MatchString(in.Name) {
		return badRequest("name must be 1 to 100 letters, digits, spaces or ._:/()-")
	}
	if strings.HasPrefix(in.Name, "orbit/") {
		return badRequest("check names starting with orbit/ are reserved for Orbit's pipelines")
	}
	if !checkStatuses[in.Status] {
		return badRequest("status must be one of pending, running, success, failure, error, cancelled, skipped")
	}
	if len(in.Summary) > 4000 {
		return badRequest("summary is limited to 4000 characters")
	}
	if in.DetailsURL != "" {
		u, err := url.Parse(in.DetailsURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return badRequest("details_url must be an http or https URL")
		}
	}
	return nil
}

func (s *Server) reportCheck(w http.ResponseWriter, r *http.Request) error {
	repo, err := s.loadRepo(r, access.Developer)
	if err != nil {
		return err
	}
	var in CheckInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	// Checks belong to exact commits: a branch name would move.
	sha := chi.URLParam(r, "sha")
	if resolved, err := s.Git.ResolveCommit(r.Context(), repo.AppKey, repo.Name, sha); err != nil || resolved != sha {
		return errStatus(http.StatusNotFound, "commit not found (use the full 40-character SHA)")
	}
	c, created, err := s.setCheck(r.Context(), repo, sha, in, nil, currentUser(r).ID)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, c)
	return nil
}

// setCheck creates or updates a check and tells subscribers and webhooks.
func (s *Server) setCheck(ctx context.Context, repo Repo, sha string, in CheckInput, jobID *string, by string) (Check, bool, error) {
	var id string
	var created bool
	var byArg any
	if by != "" {
		byArg = by
	}
	err := s.DB.QueryRow(ctx, `
		insert into checks (repo_id, commit_sha, name, status, summary, details_url, job_id, created_by)
		values ($1, $2, $3, $4, $5, $6, $7, $8)
		on conflict (repo_id, commit_sha, name) do update
		   set status = excluded.status, summary = excluded.summary, details_url = excluded.details_url,
		       job_id = excluded.job_id, created_by = excluded.created_by, updated_at = now()
		returning id, xmax = 0`, repo.ID, sha, in.Name, in.Status, in.Summary, in.DetailsURL, jobID, byArg).Scan(&id, &created)
	if err != nil {
		return Check{}, false, err
	}
	rows, err := s.DB.Query(ctx, `select `+checkCols+` from checks c left join users u on u.id = c.created_by where c.id = $1`, id)
	if err != nil {
		return Check{}, false, err
	}
	checks, err := scanChecks(rows)
	if err != nil || len(checks) == 0 {
		return Check{}, false, err
	}
	c := checks[0]
	s.Hub.Publish("repo:"+repo.ID, "check.updated", c)
	s.deliverWebhooks(ctx, repo, "check", map[string]any{"check": c})
	return c, created, nil
}

// commitsWithState adds each commit's combined check state to a log.
func (s *Server) commitsWithState(ctx context.Context, repo Repo, commits []gitrepo.Commit) ([]map[string]any, error) {
	shas := make([]string, len(commits))
	for i, c := range commits {
		shas[i] = c.SHA
	}
	states, err := s.commitStates(ctx, repo.ID, shas)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, len(commits))
	for i, c := range commits {
		out[i] = map[string]any{"sha": c.SHA, "author": c.Author, "email": c.Email, "date": c.Date, "subject": c.Subject, "checks_state": states[c.SHA]}
	}
	return out, nil
}
