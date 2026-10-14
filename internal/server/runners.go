package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/Bermos/orbit/internal/access"
	"github.com/Bermos/orbit/internal/pipeline"
)

// Runners pull jobs from Orbit (see pipeline/protocol.go for the protocol).
// A shared runner takes jobs of every application; an application runner
// only that application's. Admins register shared runners, maintainers their
// application's. The runner token is shown once, when the runner is created.

const runnerPrefix = "orbit_rnr_"
const jobTokenPrefix = "orbit_job_"

// Runner is a registered runner.
type Runner struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	AppKey     *string    `json:"app_key"`
	Executor   string     `json:"executor"`
	Version    string     `json:"version"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

const runnerCols = `id, name, app_key, executor, version, last_seen_at, created_at`

// canManageRunners: admins manage shared runners, maintainers their app's.
func (s *Server) canManageRunners(r *http.Request, app *string) error {
	if app == nil {
		if !currentUser(r).IsAdmin {
			return errStatus(http.StatusForbidden, "only instance admins manage shared runners")
		}
		return nil
	}
	_, err := s.requireRole(r, *app, access.Maintainer)
	return err
}

func (s *Server) listRunners(w http.ResponseWriter, r *http.Request) error {
	var app *string
	if a := r.URL.Query().Get("app"); a != "" {
		app = &a
	}
	if err := s.canManageRunners(r, app); err != nil {
		return err
	}
	var rows pgx.Rows
	var err error
	if app == nil {
		rows, err = s.DB.Query(r.Context(), `select `+runnerCols+` from runners order by app_key nulls first, name`)
	} else {
		rows, err = s.DB.Query(r.Context(), `select `+runnerCols+` from runners where app_key = $1 order by name`, *app)
	}
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Runner])
	if err != nil {
		return err
	}
	if out == nil {
		out = []Runner{}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) createRunner(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name   string  `json:"name"`
		AppKey *string `json:"app_key"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return badRequest("name is required")
	}
	if in.AppKey != nil && *in.AppKey == "" {
		in.AppKey = nil
	}
	if err := s.canManageRunners(r, in.AppKey); err != nil {
		return err
	}
	runner, secret, err := s.registerRunner(r.Context(), in.Name, in.AppKey, currentUser(r).ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"runner": runner, "token": secret})
	return nil
}

// registerRunner creates a runner and returns its token.
func (s *Server) registerRunner(ctx context.Context, name string, app *string, by string) (Runner, string, error) {
	secret, err := newSecret(runnerPrefix)
	if err != nil {
		return Runner{}, "", err
	}
	var byArg any
	if by != "" {
		byArg = by
	}
	rows, err := s.DB.Query(ctx, `insert into runners (name, app_key, token_hash, created_by) values ($1, $2, $3, $4) returning `+runnerCols,
		name, app, hashToken(secret), byArg)
	if err != nil {
		return Runner{}, "", err
	}
	runner, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Runner])
	return runner, secret, err
}

// RegisterEmbeddedRunner replaces the runner the server runs itself (see
// ORBIT_EMBEDDED_RUNNER) and returns its token.
func (s *Server) RegisterEmbeddedRunner(ctx context.Context) (string, error) {
	if _, err := s.DB.Exec(ctx, `delete from runners where name = 'embedded' and created_by is null and app_key is null`); err != nil {
		return "", err
	}
	_, secret, err := s.registerRunner(ctx, "embedded", nil, "")
	return secret, err
}

func (s *Server) deleteRunner(w http.ResponseWriter, r *http.Request) error {
	var app *string
	err := s.DB.QueryRow(r.Context(), `select app_key from runners where id = $1`, chi.URLParam(r, "runnerID")).Scan(&app)
	if err != nil {
		return err
	}
	if err := s.canManageRunners(r, app); err != nil {
		return err
	}
	if _, err := s.DB.Exec(r.Context(), `delete from runners where id = $1`, chi.URLParam(r, "runnerID")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// The runner protocol.

type runnerKey struct{}
type jobKey struct{}

// runningJob is the job a job token belongs to.
type runningJob struct {
	ID         string
	PipelineID string
	Kind       string
	Spec       pipeline.Job
	App        string
	Repo       string
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(h, "Bearer "); ok {
		return t
	}
	return ""
}

func (s *Server) requireRunner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rn Runner
		rows, err := s.DB.Query(r.Context(), `update runners set last_seen_at = now(), executor = $2, version = $3 where token_hash = $1 returning `+runnerCols,
			hashToken(bearer(r)), r.Header.Get("X-Orbit-Executor"), r.Header.Get("X-Orbit-Runner-Version"))
		if err == nil {
			rn, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Runner])
		}
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid runner token"})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), runnerKey{}, rn)))
	})
}

// jobByToken finds the running job a job token belongs to.
func (s *Server) jobByToken(ctx context.Context, token string) (runningJob, error) {
	var j runningJob
	if token == "" {
		return j, pgx.ErrNoRows
	}
	err := s.DB.QueryRow(ctx, `
		select j.id, j.pipeline_id, j.kind, j.spec, r.app_key, r.name
		  from jobs j join pipelines p on p.id = j.pipeline_id join repositories r on r.id = p.repo_id
		 where j.token_hash = $1 and j.status = 'running'`, hashToken(token)).Scan(&j.ID, &j.PipelineID, &j.Kind, &j.Spec, &j.App, &j.Repo)
	return j, err
}

func (s *Server) requireJob(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		j, err := s.jobByToken(r.Context(), bearer(r))
		if err != nil || j.ID != chi.URLParam(r, "jobID") {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired job token"})
			return
		}
		s.DB.Exec(r.Context(), `update jobs set heartbeat_at = now() where id = $1`, j.ID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), jobKey{}, j)))
	})
}

func currentJob(r *http.Request) runningJob {
	j, _ := r.Context().Value(jobKey{}).(runningJob)
	return j
}

func (s *Server) runnerRoutes(r chi.Router) {
	r.With(s.requireRunner).Post("/jobs/next", s.handle(s.nextJob))
	r.Group(func(r chi.Router) {
		r.Use(s.requireJob)
		r.Post("/jobs/{jobID}/logs", s.handle(s.appendLog))
		r.Post("/jobs/{jobID}/agent", s.handle(s.agentPoll))
		r.Put("/jobs/{jobID}/patch", s.handle(s.uploadPatch))
		r.Post("/jobs/{jobID}/finish", s.handle(s.runnerFinish))
	})
}

// longPoll is how long a runner waits for work in one request.
var longPoll = 25 * time.Second

func (s *Server) nextJob(w http.ResponseWriter, r *http.Request) error {
	rn, _ := r.Context().Value(runnerKey{}).(Runner)
	deadline := time.NewTimer(longPoll)
	defer deadline.Stop()
	for {
		wake := s.ci.waitCh()
		a, err := s.claimJob(r.Context(), rn)
		if err != nil {
			return err
		}
		if a != nil {
			writeJSON(w, http.StatusOK, a)
			return nil
		}
		select {
		case <-wake:
		case <-time.After(5 * time.Second):
		case <-deadline.C:
			w.WriteHeader(http.StatusNoContent)
			return nil
		case <-r.Context().Done():
			return nil
		}
	}
}

// claimJob hands the oldest ready job the runner may take to it.
func (s *Server) claimJob(ctx context.Context, rn Runner) (*pipeline.Assignment, error) {
	token, err := newSecret(jobTokenPrefix)
	if err != nil {
		return nil, err
	}
	var a pipeline.Assignment
	err = s.DB.QueryRow(ctx, `
		with candidate as (
			select j.id from jobs j
			  join pipelines p on p.id = j.pipeline_id
			  join repositories r on r.id = p.repo_id
			 where j.status = 'pending'
			   and ($2::text is null or r.app_key = $2)
			   and not exists (select 1 from jobs d where d.pipeline_id = j.pipeline_id and d.name = any(j.needs) and d.status <> 'success')
			 order by j.created_at
			 limit 1
			 for update of j skip locked
		)
		update jobs j set status = 'running', runner_id = $1, token_hash = $3, started_at = now(), heartbeat_at = now()
		  from candidate c, pipelines p, repositories r
		 where j.id = c.id and p.id = j.pipeline_id and r.id = p.repo_id
		returning j.id, p.id, p.number, r.app_key, r.name, p.commit_sha, p.ref, j.kind, j.spec`,
		rn.ID, rn.AppKey, hashToken(token)).Scan(&a.JobID, &a.PipelineID, &a.Number, &a.App, &a.Repo, &a.Commit, &a.Ref, &a.Kind, &a.Job)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.Token = token
	a.ClonePath = "/git/" + a.App + "/" + a.Repo + ".git"
	if _, err := s.DB.Exec(ctx, `update pipelines set status = 'running' where id = $1 and status = 'pending'`, a.PipelineID); err != nil {
		return nil, err
	}
	if a.Kind == "agent" {
		if err := s.beginAgentJob(ctx, a); err != nil {
			slog.Warn("agent job could not start", "job", a.JobID, "err", err)
			if err := s.finishJob(ctx, a.JobID, "error", nil, err.Error()); err != nil {
				return nil, err
			}
			return nil, nil
		}
	}
	if err := s.settlePipeline(ctx, a.PipelineID); err != nil {
		return nil, err
	}
	return &a, nil
}

const maxLogChunk = 1 << 20

func (s *Server) appendLog(w http.ResponseWriter, r *http.Request) error {
	j := currentJob(r)
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLogChunk))
	if err != nil {
		return badRequest("log chunk too large")
	}
	ctx := r.Context()
	if len(data) > 0 {
		var seq int
		err := s.DB.QueryRow(ctx, `insert into job_logs (job_id, seq, data)
			select $1, coalesce(max(seq), 0) + 1, $2 from job_logs where job_id = $1 returning seq`, j.ID, strings.ToValidUTF8(string(data), "�")).Scan(&seq)
		if err != nil {
			return err
		}
		s.Hub.Publish("job:"+j.ID, "job.log", LogChunk{Seq: seq, Data: string(data)})
	}
	var cancel bool
	if err := s.DB.QueryRow(ctx, `select cancel_requested from jobs where id = $1`, j.ID).Scan(&cancel); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, pipeline.LogAck{Cancel: cancel})
	return nil
}

func (s *Server) runnerFinish(w http.ResponseWriter, r *http.Request) error {
	j := currentJob(r)
	var in pipeline.Finish
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		return badRequest("invalid JSON")
	}
	ctx := r.Context()
	var cancelled bool
	if err := s.DB.QueryRow(ctx, `select cancel_requested from jobs where id = $1`, j.ID).Scan(&cancelled); err != nil {
		return err
	}
	switch {
	case cancelled:
		in.Status = "cancelled"
	case in.Status != "success" && in.Status != "failure":
		in.Status = "error"
	}
	if j.Kind == "agent" {
		s.endAgentJob(ctx, j, in)
		s.afterAgentJob(ctx, j, in)
	}
	if err := s.finishJob(ctx, j.ID, in.Status, in.ExitCode, in.Error); err != nil {
		return err
	}
	if j.Kind == "agent" {
		// Show the job's final status under the reply.
		var runID string
		if err := s.DB.QueryRow(ctx, `select id from agent_runs where job_id = $1 and reply_message_id is not null`, j.ID).Scan(&runID); err == nil {
			if err := s.refreshReply(ctx, runID, "message.updated"); err != nil {
				slog.Error("refresh agent reply", "run", runID, "err", err)
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
