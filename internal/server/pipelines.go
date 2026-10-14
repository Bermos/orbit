package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/Bermos/orbit/internal/access"
	"github.com/Bermos/orbit/internal/gitrepo"
	"github.com/Bermos/orbit/internal/pipeline"
)

// Orbit's own CI. A push to a repository with a .orbit/pipeline.yml creates
// a pipeline: one job per entry, queued until its needs have succeeded.
// Runners pull queued jobs (see runners.go) and run them with an executor
// (Kubernetes pods, or local processes for development). Every job also
// reports as a check named orbit/<job>, so Orbit's pipelines and external CI
// show up the same way on commits.

// Pipeline is a pipeline with its jobs.
type Pipeline struct {
	ID          string     `json:"id"`
	App         string     `json:"app"`
	Repo        string     `json:"repo"`
	Number      int        `json:"number"`
	Commit      string     `json:"commit"`
	Ref         string     `json:"ref"`
	Trigger     string     `json:"trigger"`
	TriggeredBy *Author    `json:"triggered_by"`
	Status      string     `json:"status"`
	Error       string     `json:"error"`
	CreatedAt   time.Time  `json:"created_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	Jobs        []Job      `json:"jobs"`

	repoID string
}

// Job is one job of a pipeline.
type Job struct {
	ID         string       `json:"id"`
	PipelineID string       `json:"pipeline_id"`
	Name       string       `json:"name"`
	Kind       string       `json:"kind"`
	Spec       pipeline.Job `json:"spec"`
	Needs      []string     `json:"needs"`
	Status     string       `json:"status"`
	ExitCode   *int         `json:"exit_code"`
	Error      string       `json:"error"`
	Runner     *string      `json:"runner"`
	HasPatch   bool         `json:"has_patch"`
	AgentRunID *string      `json:"agent_run_id"`
	CreatedAt  time.Time    `json:"created_at"`
	StartedAt  *time.Time   `json:"started_at"`
	FinishedAt *time.Time   `json:"finished_at"`
}

// terminal job and pipeline statuses.
func finished(status string) bool {
	switch status {
	case "success", "failure", "error", "cancelled", "skipped":
		return true
	}
	return false
}

// ciService holds the scheduler's in-process state.
type ciService struct {
	mu   sync.Mutex
	wake chan struct{} // closed and replaced whenever jobs may be ready

	sessions map[string]*agentSession // running agent jobs, by job ID
}

func newCI() *ciService {
	return &ciService{wake: make(chan struct{}), sessions: map[string]*agentSession{}}
}

// notify wakes runners waiting for work.
func (c *ciService) notify() {
	c.mu.Lock()
	close(c.wake)
	c.wake = make(chan struct{})
	c.mu.Unlock()
}

func (c *ciService) waitCh() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wake
}

const pipelineCols = `p.id, p.repo_id, r.app_key, r.name, p.number, p.commit_sha, p.ref, p.trigger,
	u.id, u.username, u.display_name, u.kind, p.status, p.error, p.created_at, p.finished_at`

const pipelineFrom = ` from pipelines p join repositories r on r.id = p.repo_id left join users u on u.id = p.triggered_by`

func scanPipelines(rows pgx.Rows) ([]Pipeline, error) {
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Pipeline, error) {
		var p Pipeline
		var uid, uname, dname, kind *string
		err := row.Scan(&p.ID, &p.repoID, &p.App, &p.Repo, &p.Number, &p.Commit, &p.Ref, &p.Trigger,
			&uid, &uname, &dname, &kind, &p.Status, &p.Error, &p.CreatedAt, &p.FinishedAt)
		if uid != nil {
			p.TriggeredBy = &Author{ID: *uid, Username: *uname, DisplayName: *dname, Kind: *kind}
		}
		p.Jobs = []Job{}
		return p, err
	})
	if out == nil {
		out = []Pipeline{}
	}
	return out, err
}

const jobCols = `j.id, j.pipeline_id, j.name, j.kind, j.spec, j.needs, j.status, j.exit_code, j.error, rn.name,
	j.patch is not null and length(j.patch) > 0, (select id from agent_runs a where a.job_id = j.id limit 1),
	j.created_at, j.started_at, j.finished_at`

func (s *Server) loadJobs(ctx context.Context, pipelineIDs ...string) (map[string][]Job, error) {
	rows, err := s.DB.Query(ctx, `select `+jobCols+` from jobs j left join runners rn on rn.id = j.runner_id
		where j.pipeline_id = any($1) order by j.created_at, j.name`, pipelineIDs)
	if err != nil {
		return nil, err
	}
	jobs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Job, error) {
		var j Job
		err := row.Scan(&j.ID, &j.PipelineID, &j.Name, &j.Kind, &j.Spec, &j.Needs, &j.Status, &j.ExitCode, &j.Error, &j.Runner,
			&j.HasPatch, &j.AgentRunID, &j.CreatedAt, &j.StartedAt, &j.FinishedAt)
		return j, err
	})
	if err != nil {
		return nil, err
	}
	out := map[string][]Job{}
	for _, j := range jobs {
		out[j.PipelineID] = append(out[j.PipelineID], j)
	}
	return out, nil
}

func (s *Server) loadPipelines(ctx context.Context, where string, args ...any) ([]Pipeline, error) {
	rows, err := s.DB.Query(ctx, `select `+pipelineCols+pipelineFrom+` `+where, args...)
	if err != nil {
		return nil, err
	}
	ps, err := scanPipelines(rows)
	if err != nil || len(ps) == 0 {
		return ps, err
	}
	ids := make([]string, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
	}
	jobs, err := s.loadJobs(ctx, ids...)
	if err != nil {
		return nil, err
	}
	for i := range ps {
		if js, ok := jobs[ps[i].ID]; ok {
			ps[i].Jobs = js
		}
	}
	return ps, nil
}

func (s *Server) pipelineByID(ctx context.Context, id string) (Pipeline, error) {
	ps, err := s.loadPipelines(ctx, `where p.id = $1`, id)
	if err != nil {
		return Pipeline{}, err
	}
	if len(ps) == 0 {
		return Pipeline{}, errNotFound
	}
	return ps[0], nil
}

func (p Pipeline) url() string {
	return fmt.Sprintf("/apps/%s/repos/%s/pipelines/%d", p.App, p.Repo, p.Number)
}

// publishPipeline sends the pipeline to everyone watching the repository.
func (s *Server) publishPipeline(ctx context.Context, id string) {
	p, err := s.pipelineByID(ctx, id)
	if err != nil {
		slog.Error("load pipeline", "pipeline", id, "err", err)
		return
	}
	s.Hub.Publish("repo:"+p.repoID, "pipeline.updated", p)
}

// createPipeline queues a pipeline. With specErr set it records a pipeline
// that failed to start, so a broken pipeline file shows up where people look.
// hook, if set, runs in the same transaction, before runners can see the
// jobs; it gets the pipeline's ID and its job IDs by name.
func (s *Server) createPipeline(ctx context.Context, repo Repo, commit, ref, trigger, by string, jobs []pipeline.Job, specErr string,
	hook func(tx pgx.Tx, pipelineID string, jobIDs map[string]string) error) (Pipeline, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Pipeline{}, err
	}
	defer tx.Rollback(ctx)
	var number int
	if err := tx.QueryRow(ctx, `update repositories set next_pipeline_number = next_pipeline_number + 1 where id = $1 returning next_pipeline_number - 1`, repo.ID).Scan(&number); err != nil {
		return Pipeline{}, err
	}
	status, finishedAt := "pending", (*time.Time)(nil)
	if specErr != "" {
		now := time.Now()
		status, finishedAt = "error", &now
	}
	var byArg any
	if by != "" {
		byArg = by
	}
	var id string
	if err := tx.QueryRow(ctx, `insert into pipelines (repo_id, number, commit_sha, ref, trigger, triggered_by, status, error, finished_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9) returning id`,
		repo.ID, number, commit, ref, trigger, byArg, status, specErr, finishedAt).Scan(&id); err != nil {
		return Pipeline{}, err
	}
	jobIDs := map[string]string{}
	for _, j := range jobs {
		needs := j.Needs
		if needs == nil {
			needs = []string{}
		}
		var jobID string
		if err := tx.QueryRow(ctx, `insert into jobs (pipeline_id, name, kind, spec, needs) values ($1, $2, $3, $4, $5) returning id`,
			id, j.Name, j.Kind(), j, needs).Scan(&jobID); err != nil {
			return Pipeline{}, err
		}
		jobIDs[j.Name] = jobID
	}
	if hook != nil {
		if err := hook(tx, id, jobIDs); err != nil {
			return Pipeline{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Pipeline{}, err
	}
	p, err := s.pipelineByID(ctx, id)
	if err != nil {
		return Pipeline{}, err
	}
	// Agent pipelines started from chat are not about the commit, so they do
	// not report on it.
	if trigger != "agent" {
		if specErr != "" {
			s.setCheck(ctx, repo, commit, CheckInput{Name: "orbit/pipeline", Status: "error", Summary: truncate(specErr, 1000), DetailsURL: s.BaseURL + p.url()}, nil, "")
		}
		for _, j := range p.Jobs {
			s.setCheck(ctx, repo, commit, CheckInput{Name: "orbit/" + j.Name, Status: "pending", DetailsURL: s.BaseURL + p.url()}, &j.ID, "")
		}
	}
	s.Hub.Publish("repo:"+repo.ID, "pipeline.updated", p)
	if specErr != "" {
		s.deliverWebhooks(ctx, repo, "pipeline", map[string]any{"pipeline": p})
	}
	s.ci.notify()
	return p, nil
}

// onPush reacts to new commits on a branch: webhooks first, then the
// branch's pipeline, if its file asks for one.
func (s *Server) onPush(ctx context.Context, repo Repo, pusher User, branch, before, after string) {
	s.deliverWebhooks(ctx, repo, "push", map[string]any{
		"ref": "refs/heads/" + branch, "branch": branch, "before": before, "after": after,
		"pusher": map[string]any{"id": pusher.ID, "username": pusher.Username, "display_name": pusher.DisplayName},
	})
	content, binary, err := s.Git.Blob(ctx, repo.AppKey, repo.Name, after, pipeline.File)
	if errors.Is(err, gitrepo.ErrNotFound) {
		return
	}
	if err != nil {
		slog.Error("read pipeline file", "repo", repo.AppKey+"/"+repo.Name, "err", err)
		return
	}
	if binary {
		content = ""
	}
	spec, perr := pipeline.Parse([]byte(content))
	if perr == nil && !spec.Runs(branch) {
		return
	}
	specErr := ""
	if perr == nil {
		perr = s.checkAgents(spec.Jobs)
	}
	if perr != nil {
		specErr = perr.Error()
		spec.Jobs = nil
	}
	if _, err := s.createPipeline(ctx, repo, after, branch, "push", pusher.ID, spec.Jobs, specErr, nil); err != nil {
		slog.Error("create pipeline", "repo", repo.AppKey+"/"+repo.Name, "err", err)
	}
}

// checkAgents rejects agent jobs for agents this instance does not have.
func (s *Server) checkAgents(jobs []pipeline.Job) error {
	for _, j := range jobs {
		if j.Agent == "" {
			continue
		}
		if s.agents == nil {
			return fmt.Errorf("job %s: agents are not enabled on this instance", j.Name)
		}
		if _, ok := s.agents.defs[j.Agent]; !ok {
			return fmt.Errorf("job %s: there is no agent named %q", j.Name, j.Agent)
		}
	}
	return nil
}

// finishJob records a job's outcome, skips jobs that can no longer run,
// and settles the pipeline once every job is done.
func (s *Server) finishJob(ctx context.Context, jobID, status string, exitCode *int, errText string) error {
	var pipelineID string
	err := s.DB.QueryRow(ctx, `update jobs set status = $2, exit_code = $3, error = $4, finished_at = now(), token_hash = null
		where id = $1 and status in ('pending', 'running') returning pipeline_id`, jobID, status, exitCode, truncate(errText, 2000)).Scan(&pipelineID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already finished
	}
	if err != nil {
		return err
	}
	if err := s.settlePipeline(ctx, pipelineID); err != nil {
		return err
	}
	s.ci.notify()
	return nil
}

// settlePipeline skips jobs whose needs failed, updates checks and sets the
// pipeline's status from its jobs.
func (s *Server) settlePipeline(ctx context.Context, pipelineID string) error {
	// Skipping can cascade along needs, one level per round.
	for {
		tag, err := s.DB.Exec(ctx, `update jobs j set status = 'skipped', finished_at = now()
			where j.pipeline_id = $1 and j.status = 'pending' and exists (
				select 1 from jobs d where d.pipeline_id = j.pipeline_id and d.name = any(j.needs)
				   and d.status in ('failure', 'error', 'cancelled', 'skipped'))`, pipelineID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			break
		}
	}
	p, err := s.pipelineByID(ctx, pipelineID)
	if err != nil {
		return err
	}
	status := pipelineStatus(p.Jobs)
	if status != p.Status && !finished(p.Status) {
		if finished(status) {
			_, err = s.DB.Exec(ctx, `update pipelines set status = $2, finished_at = now() where id = $1`, p.ID, status)
		} else {
			_, err = s.DB.Exec(ctx, `update pipelines set status = $2 where id = $1`, p.ID, status)
		}
		if err != nil {
			return err
		}
	}
	repo, err := s.repoByID(ctx, p.repoID)
	if err != nil {
		return err
	}
	if p.Trigger != "agent" {
		for _, j := range p.Jobs {
			s.syncJobCheck(ctx, repo, p, j)
		}
	}
	s.publishPipeline(ctx, p.ID)
	if finished(status) && !finished(p.Status) {
		p, _ = s.pipelineByID(ctx, p.ID)
		s.deliverWebhooks(ctx, repo, "pipeline", map[string]any{"pipeline": p})
	}
	return nil
}

// syncJobCheck mirrors a job's status onto its check, when it changed.
func (s *Server) syncJobCheck(ctx context.Context, repo Repo, p Pipeline, j Job) {
	want := j.Status
	summary := ""
	switch j.Status {
	case "failure":
		summary = "The job failed"
		if j.ExitCode != nil {
			summary = fmt.Sprintf("The job failed with exit code %d", *j.ExitCode)
		}
	case "error":
		summary = j.Error
	case "skipped":
		summary = "Skipped because a job it needs did not succeed"
	}
	var current string
	err := s.DB.QueryRow(ctx, `select status from checks where repo_id = $1 and commit_sha = $2 and name = $3`, repo.ID, p.Commit, "orbit/"+j.Name).Scan(&current)
	if err == nil && current == want {
		return
	}
	jobID := j.ID
	s.setCheck(ctx, repo, p.Commit, CheckInput{Name: "orbit/" + j.Name, Status: want, Summary: summary, DetailsURL: s.BaseURL + p.url()}, &jobID, "")
}

// pipelineStatus derives a pipeline's status from its jobs.
func pipelineStatus(jobs []Job) string {
	has := map[string]bool{}
	for _, j := range jobs {
		has[j.Status] = true
	}
	switch {
	case len(jobs) == 0:
		return "error"
	case has["pending"] && !has["running"] && !has["success"] && !has["failure"] && !has["error"] && !has["cancelled"]:
		return "pending"
	case has["pending"] || has["running"]:
		return "running"
	case has["error"]:
		return "error"
	case has["failure"]:
		return "failure"
	case has["cancelled"]:
		return "cancelled"
	case has["skipped"] && !has["success"]:
		return "cancelled"
	}
	return "success"
}

func (s *Server) repoByID(ctx context.Context, id string) (Repo, error) {
	rows, err := s.DB.Query(ctx, `select `+repoCols+` from repositories r where r.id = $1`, id)
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

// The API.

func (s *Server) listPipelines(w http.ResponseWriter, r *http.Request) error {
	repo, err := s.loadRepo(r, access.Reader)
	if err != nil {
		return err
	}
	ps, err := s.loadPipelines(r.Context(), `where p.repo_id = $1 order by p.number desc limit 50`, repo.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, ps)
	return nil
}

func (s *Server) pipelineParam(r *http.Request, min access.Role) (Repo, Pipeline, error) {
	repo, err := s.loadRepo(r, min)
	if err != nil {
		return repo, Pipeline{}, err
	}
	n, err := strconv.Atoi(chi.URLParam(r, "number"))
	if err != nil {
		return repo, Pipeline{}, errNotFound
	}
	ps, err := s.loadPipelines(r.Context(), `where p.repo_id = $1 and p.number = $2`, repo.ID, n)
	if err != nil {
		return repo, Pipeline{}, err
	}
	if len(ps) == 0 {
		return repo, Pipeline{}, errNotFound
	}
	return repo, ps[0], nil
}

func (s *Server) getPipeline(w http.ResponseWriter, r *http.Request) error {
	_, p, err := s.pipelineParam(r, access.Reader)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, p)
	return nil
}

// runPipeline starts the pipeline file of a branch or commit by hand.
func (s *Server) runPipeline(w http.ResponseWriter, r *http.Request) error {
	repo, err := s.loadRepo(r, access.Developer)
	if err != nil {
		return err
	}
	var in struct {
		Ref string `json:"ref"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Ref == "" {
		in.Ref = repo.DefaultBranch
	}
	ctx := r.Context()
	sha, err := s.Git.ResolveCommit(ctx, repo.AppKey, repo.Name, in.Ref)
	if err != nil {
		return errStatus(http.StatusNotFound, "ref not found")
	}
	content, binary, err := s.Git.Blob(ctx, repo.AppKey, repo.Name, sha, pipeline.File)
	if errors.Is(err, gitrepo.ErrNotFound) || binary {
		return badRequest("there is no " + pipeline.File + " at " + in.Ref)
	}
	if err != nil {
		return err
	}
	spec, perr := pipeline.Parse([]byte(content))
	if perr == nil {
		perr = s.checkAgents(spec.Jobs)
	}
	if perr != nil {
		return badRequest(perr.Error())
	}
	p, err := s.createPipeline(ctx, repo, sha, in.Ref, "manual", currentUser(r).ID, spec.Jobs, "", nil)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, p)
	return nil
}

func (s *Server) cancelPipeline(w http.ResponseWriter, r *http.Request) error {
	_, p, err := s.pipelineParam(r, access.Developer)
	if err != nil {
		return err
	}
	ctx := r.Context()
	if _, err := s.DB.Exec(ctx, `update jobs set status = 'cancelled', finished_at = now() where pipeline_id = $1 and status = 'pending'`, p.ID); err != nil {
		return err
	}
	// Running jobs stop when their runner next reports in.
	if _, err := s.DB.Exec(ctx, `update jobs set cancel_requested = true where pipeline_id = $1 and status = 'running'`, p.ID); err != nil {
		return err
	}
	s.ci.mu.Lock()
	for _, j := range p.Jobs {
		if sess := s.ci.sessions[j.ID]; sess != nil {
			sess.cancel()
		}
	}
	s.ci.mu.Unlock()
	if err := s.settlePipeline(ctx, p.ID); err != nil {
		return err
	}
	p, err = s.pipelineByID(ctx, p.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, p)
	return nil
}

// jobForReader loads a job and checks the user may read its repository.
func (s *Server) jobForReader(r *http.Request) (Job, Pipeline, error) {
	var pipelineID string
	if err := s.DB.QueryRow(r.Context(), `select pipeline_id from jobs where id = $1`, chi.URLParam(r, "jobID")).Scan(&pipelineID); err != nil {
		return Job{}, Pipeline{}, err
	}
	p, err := s.pipelineByID(r.Context(), pipelineID)
	if err != nil {
		return Job{}, p, err
	}
	if _, err := s.requireRole(r, p.App, access.Reader); err != nil {
		return Job{}, p, err
	}
	for _, j := range p.Jobs {
		if j.ID == chi.URLParam(r, "jobID") {
			return j, p, nil
		}
	}
	return Job{}, p, errNotFound
}

// LogChunk is a piece of a job's log.
type LogChunk struct {
	Seq  int    `json:"seq"`
	Data string `json:"data"`
}

func (s *Server) jobLogs(w http.ResponseWriter, r *http.Request) error {
	j, _, err := s.jobForReader(r)
	if err != nil {
		return err
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	rows, err := s.DB.Query(r.Context(), `select seq, data from job_logs where job_id = $1 and seq > $2 order by seq limit 2000`, j.ID, after)
	if err != nil {
		return err
	}
	chunks, err := pgx.CollectRows(rows, pgx.RowToStructByPos[LogChunk])
	if err != nil {
		return err
	}
	if chunks == nil {
		chunks = []LogChunk{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": j, "chunks": chunks})
	return nil
}

// jobPatch returns the changes an agent job made, as a git diff.
func (s *Server) jobPatch(w http.ResponseWriter, r *http.Request) error {
	j, _, err := s.jobForReader(r)
	if err != nil {
		return err
	}
	var patch []byte
	if err := s.DB.QueryRow(r.Context(), `select coalesce(patch, '') from jobs where id = $1`, j.ID).Scan(&patch); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
	w.Write(patch)
	return nil
}

// reapJobs fails jobs whose runner stopped reporting, and agent jobs whose
// session this process lost (it restarted).
func (s *Server) reapJobs(ctx context.Context) {
	rows, err := s.DB.Query(ctx, `select id from jobs where status = 'running' and heartbeat_at < now() - interval '2 minutes'`)
	if err != nil {
		slog.Error("find stale jobs", "err", err)
		return
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return
	}
	for _, id := range ids {
		if err := s.finishJob(ctx, id, "error", nil, "the runner stopped responding"); err != nil {
			slog.Error("reap job", "job", id, "err", err)
		}
	}
}

// StartCI runs the CI housekeeping until ctx ends.
func (s *Server) StartCI(ctx context.Context) {
	s.background.Add(1)
	go func() {
		defer s.background.Done()
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.reapJobs(ctx)
			}
		}
	}()
}

// pushedRefs compares branch heads before and after a push.
func pushedRefs(before, after []gitrepo.Branch) [][3]string {
	old := map[string]string{}
	for _, b := range before {
		old[b.Name] = b.Commit
	}
	var out [][3]string
	for _, b := range after {
		if old[b.Name] != b.Commit {
			prev := old[b.Name]
			if prev == "" {
				prev = strings.Repeat("0", 40)
			}
			out = append(out, [3]string{b.Name, prev, b.Commit})
		}
	}
	return out
}
