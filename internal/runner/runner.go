// Package runner pulls jobs from an Orbit server and runs them with an
// executor: a pod per job on Kubernetes, or local processes for development.
//
// The runner only talks to Orbit over HTTPS (see pipeline/protocol.go), so it
// can run anywhere that reaches Orbit: in the same cluster, in another one,
// or on a laptop.
package runner

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Bermos/orbit/internal/pipeline"
)

// Executor runs jobs somewhere.
type Executor interface {
	// Name identifies the executor to Orbit ("kubernetes", "local").
	Name() string
	// Prepare starts a workspace with the repository checked out at the job's
	// commit, writing what it does to spec.Log.
	Prepare(ctx context.Context, spec Spec) (Workspace, error)
}

// Spec is what an executor needs to set up a job.
type Spec struct {
	Assignment pipeline.Assignment
	// CloneURL is the repository's git URL and GitAuth the HTTP header that
	// authenticates with the job token ("Authorization: Basic ...").
	CloneURL string
	GitAuth  string
	Image    string
	Env      map[string]string
	Timeout  time.Duration
	Log      io.Writer
}

// Workspace is a prepared job.
type Workspace interface {
	// Exec runs a command in the root of the checkout and returns its exit
	// code. With Helper set it runs where git is available (it may be a
	// different container than the job's image).
	Exec(ctx context.Context, cmd Command) (int, error)
	// Close tears the workspace down.
	Close(ctx context.Context) error
}

// Command is one command to run in a workspace.
type Command struct {
	Argv   []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Helper bool
}

// Config configures a runner.
type Config struct {
	// URL is Orbit's base URL as the runner (and its jobs) reach it.
	URL   string
	Token string
	// Executor runs the jobs.
	Executor Executor
	// Concurrency is how many jobs run at once (default 1).
	Concurrency int
	// AgentImage runs agent jobs whose agent names no image.
	AgentImage string
	Version    string
	HTTP       *http.Client
}

// Run takes and runs jobs until ctx ends.
func Run(ctx context.Context, cfg Config) error {
	if cfg.URL == "" || cfg.Token == "" || cfg.Executor == nil {
		return errors.New("runner: URL, token and executor are required")
	}
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.AgentImage == "" {
		cfg.AgentImage = "golang:1.26"
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 2 * time.Minute}
	}
	c := &client{base: strings.TrimRight(cfg.URL, "/"), token: cfg.Token, http: cfg.HTTP, executor: cfg.Executor.Name(), version: cfg.Version}
	slog.Info("runner started", "url", c.base, "executor", cfg.Executor.Name(), "concurrency", cfg.Concurrency)
	var wg sync.WaitGroup
	for range cfg.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			backoff := time.Second
			for ctx.Err() == nil {
				a, err := c.next(ctx)
				if err != nil {
					if ctx.Err() != nil {
						break
					}
					slog.Warn("runner could not get work", "err", err, "retry_in", backoff)
					sleep(ctx, backoff)
					backoff = min(backoff*2, time.Minute)
					continue
				}
				backoff = time.Second
				if a == nil {
					continue
				}
				runJob(ctx, cfg, c, *a)
			}
		}()
	}
	wg.Wait()
	return nil
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// runJob runs one job and reports its outcome. It never panics the runner.
func runJob(ctx context.Context, cfg Config, c *client, a pipeline.Assignment) {
	log := slog.With("job", a.JobID, "name", a.Job.Name, "repo", a.App+"/"+a.Repo, "pipeline", a.Number)
	log.Info("job started")
	timeout := time.Duration(a.Job.Timeout)
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	jobCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	jc := &jobClient{client: c, id: a.JobID, token: a.Token}
	logs := newLogStream(jc, cancel)
	defer logs.Close()

	finish := func(f pipeline.Finish) {
		logs.Close()
		// Report even when the job's own context has ended.
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer rcancel()
		if err := jc.finish(rctx, f); err != nil {
			log.Error("report job outcome", "err", err)
		}
		log.Info("job finished", "status", f.Status)
	}

	f := func() (f pipeline.Finish) {
		defer func() {
			if p := recover(); p != nil {
				log.Error("job panicked", "panic", p)
				f = pipeline.Finish{Status: "error", Error: "the runner failed: " + fmt.Sprint(p)}
			}
		}()
		return execute(jobCtx, cfg, jc, logs, a)
	}()
	if jobCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
		fmt.Fprintf(logs, "\nThe job ran out of time (%s).\n", timeout)
		f = pipeline.Finish{Status: "failure", Error: "the job ran out of time"}
	} else if ctx.Err() != nil {
		f = pipeline.Finish{Status: "error", Error: "the runner stopped"}
	}
	finish(f)
}

func execute(ctx context.Context, cfg Config, jc *jobClient, logs io.Writer, a pipeline.Assignment) pipeline.Finish {
	image := a.Job.Image
	if image == "" && a.Kind == "agent" {
		image = cfg.AgentImage
	}
	env := map[string]string{
		"CI": "true", "ORBIT": "true",
		"ORBIT_APP": a.App, "ORBIT_REPO": a.Repo, "ORBIT_COMMIT": a.Commit, "ORBIT_REF": a.Ref,
		"ORBIT_PIPELINE_NUMBER": fmt.Sprint(a.Number), "ORBIT_JOB": a.Job.Name,
	}
	for k, v := range a.Job.Env {
		env[k] = v
	}
	spec := Spec{
		Assignment: a,
		CloneURL:   jc.base + a.ClonePath,
		GitAuth:    "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(pipeline.JobUser+":"+a.Token)),
		Image:      image,
		Env:        env,
		Timeout:    time.Duration(a.Job.Timeout),
		Log:        logs,
	}
	fmt.Fprintf(logs, "Running on %s with %s\n", cfg.Executor.Name(), image)
	ws, err := cfg.Executor.Prepare(ctx, spec)
	if err != nil {
		fmt.Fprintf(logs, "Could not prepare the job: %v\n", err)
		return pipeline.Finish{Status: "error", Error: "could not prepare the job: " + err.Error()}
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if err := ws.Close(cctx); err != nil {
			slog.Warn("clean up job", "job", a.JobID, "err", err)
		}
	}()

	if a.Kind == "agent" {
		return runAgent(ctx, jc, ws, logs, a)
	}
	code, err := ws.Exec(ctx, Command{Argv: []string{"sh", "-c", pipeline.Script(a.Job.Script)}, Stdout: logs, Stderr: logs})
	if err != nil {
		if ctx.Err() != nil {
			return pipeline.Finish{Status: "failure", Error: ctx.Err().Error()}
		}
		fmt.Fprintf(logs, "\n%v\n", err)
		return pipeline.Finish{Status: "error", Error: err.Error()}
	}
	if code != 0 {
		fmt.Fprintf(logs, "\nExited with code %d\n", code)
		return pipeline.Finish{Status: "failure", ExitCode: &code}
	}
	return pipeline.Finish{Status: "success", ExitCode: &code}
}

// runAgent serves the tool calls of an agent job, then hands back the
// changes the agent made as a binary git diff.
func runAgent(ctx context.Context, jc *jobClient, ws Workspace, logs io.Writer, a pipeline.Assignment) pipeline.Finish {
	var result *pipeline.ToolResult
	for {
		if ctx.Err() != nil {
			return pipeline.Finish{Status: "failure", Error: ctx.Err().Error()}
		}
		step, err := jc.agent(ctx, pipeline.AgentPoll{Result: result})
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			return pipeline.Finish{Status: "error", Error: "lost contact with Orbit: " + err.Error()}
		}
		result = nil
		if step.Done {
			if step.Failed != "" {
				return pipeline.Finish{Status: "failure", Error: step.Failed}
			}
			break
		}
		if step.Call != nil {
			out, err := runTool(ctx, ws, logs, *step.Call)
			r := pipeline.ToolResult{ID: step.Call.ID, Output: out}
			if err != nil {
				r.Error = err.Error()
			}
			result = &r
		}
	}

	var diff, stderr bytes.Buffer
	script := `git -c safe.directory='*' add -A && git -c safe.directory='*' -c core.quotepath=off diff --cached --binary "$1"`
	code, err := ws.Exec(ctx, Command{Argv: []string{"sh", "-c", script, "sh", a.Commit}, Stdout: &limitedWriter{w: &diff, n: 5 << 20}, Stderr: &stderr, Helper: true})
	if err != nil || code != 0 {
		return pipeline.Finish{Status: "error", Error: fmt.Sprintf("could not collect the changes: %v %s", err, strings.TrimSpace(stderr.String()))}
	}
	if diff.Len() > 0 {
		fmt.Fprintf(logs, "Changed %d file(s)\n", bytes.Count(diff.Bytes(), []byte("diff --git ")))
		if err := jc.patch(ctx, diff.Bytes()); err != nil {
			return pipeline.Finish{Status: "error", Error: "could not upload the changes: " + err.Error()}
		}
	} else {
		fmt.Fprintln(logs, "No files changed")
	}
	zero := 0
	return pipeline.Finish{Status: "success", ExitCode: &zero}
}

// limitedWriter fails once more than n bytes were written.
type limitedWriter struct {
	w io.Writer
	n int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.n {
		return 0, errors.New("output too large")
	}
	l.n -= int64(len(p))
	return l.w.Write(p)
}

// client talks to Orbit as the runner.
type client struct {
	base, token       string
	http              *http.Client
	executor, version string
}

func (c *client) do(ctx context.Context, method, path, token, contentType string, body io.Reader, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Orbit-Executor", c.executor)
	req.Header.Set("X-Orbit-Runner-Version", c.version)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&e)
		if e.Error == "" {
			e.Error = res.Status
		}
		return res.StatusCode, fmt.Errorf("%s %s: %s", method, path, e.Error)
	}
	if out != nil && res.StatusCode != http.StatusNoContent {
		return res.StatusCode, json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode, nil
}

func (c *client) next(ctx context.Context) (*pipeline.Assignment, error) {
	var a pipeline.Assignment
	status, err := c.do(ctx, "POST", "/api/runner/v1/jobs/next", c.token, "", nil, &a)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	return &a, nil
}

// jobClient talks to Orbit about one job, with its job token.
type jobClient struct {
	*client
	id, token string
}

func (j *jobClient) path(p string) string { return "/api/runner/v1/jobs/" + j.id + p }

func (j *jobClient) logs(ctx context.Context, data []byte) (pipeline.LogAck, error) {
	var ack pipeline.LogAck
	_, err := j.do(ctx, "POST", j.path("/logs"), j.token, "text/plain; charset=utf-8", bytes.NewReader(data), &ack)
	return ack, err
}

func (j *jobClient) agent(ctx context.Context, poll pipeline.AgentPoll) (pipeline.AgentStep, error) {
	var step pipeline.AgentStep
	body, _ := json.Marshal(poll)
	_, err := j.do(ctx, "POST", j.path("/agent"), j.token, "application/json", bytes.NewReader(body), &step)
	return step, err
}

func (j *jobClient) patch(ctx context.Context, diff []byte) error {
	_, err := j.do(ctx, "PUT", j.path("/patch"), j.token, "text/x-diff", bytes.NewReader(diff), nil)
	return err
}

func (j *jobClient) finish(ctx context.Context, f pipeline.Finish) error {
	body, _ := json.Marshal(f)
	_, err := j.do(ctx, "POST", j.path("/finish"), j.token, "application/json", bytes.NewReader(body), nil)
	return err
}

// logStream batches a job's output to Orbit about once a second, sends a
// heartbeat when there is nothing to say, and cancels the job when Orbit
// asks for it.
type logStream struct {
	jc     *jobClient
	cancel context.CancelFunc

	mu     sync.Mutex
	buf    bytes.Buffer
	total  int
	closed bool
	stop   chan struct{}
	done   chan struct{}
}

const (
	maxJobLog   = 20 << 20
	logInterval = time.Second
	heartbeat   = 20 * time.Second
)

func newLogStream(jc *jobClient, cancel context.CancelFunc) *logStream {
	l := &logStream{jc: jc, cancel: cancel, stop: make(chan struct{}), done: make(chan struct{})}
	go l.loop()
	return l
}

func (l *logStream) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.total >= maxJobLog:
	case l.total+len(p) > maxJobLog:
		l.buf.WriteString("\n[the log is too long; the rest is not kept]\n")
		l.total = maxJobLog
	default:
		l.buf.Write(p)
		l.total += len(p)
	}
	return len(p), nil
}

func (l *logStream) loop() {
	defer close(l.done)
	t := time.NewTicker(logInterval)
	defer t.Stop()
	last := time.Now()
	for {
		select {
		case <-l.stop:
			l.flush(true)
			return
		case <-t.C:
			if l.flush(time.Since(last) > heartbeat) {
				last = time.Now()
			}
		}
	}
}

// flush sends what is buffered (or a heartbeat when force is set) and
// reports whether it sent anything.
func (l *logStream) flush(force bool) bool {
	l.mu.Lock()
	data := bytes.Clone(l.buf.Bytes())
	l.buf.Reset()
	l.mu.Unlock()
	if len(data) == 0 && !force {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ack, err := l.jc.logs(ctx, data)
	if err != nil {
		slog.Warn("send job log", "job", l.jc.id, "err", err)
		return false
	}
	if ack.Cancel {
		l.cancel()
	}
	return true
}

// Close sends the rest of the log. It is safe to call more than once.
func (l *logStream) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()
	close(l.stop)
	<-l.done
	return nil
}
