package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bermos/orbit/internal/agent"
	"github.com/Bermos/orbit/internal/runner"
)

// ciFixture is an instance with a ledger application, an empty ledger/api
// repository and a local working copy to push from.
type ciFixture struct {
	ts      *httptest.Server
	srv     *Server
	admin   *client
	adminID string
	work    string
	repoID  string
}

func newCIFixture(t *testing.T, defs []agent.Definition) ciFixture {
	t.Helper()
	ts, srv := newTestServerWithAPI(t)
	admin, adminID := register(t, ts, "admin")
	if defs != nil {
		if err := srv.EnableAgents(context.Background(), defs, agent.Providers{"fake": agent.Fake{}}); err != nil {
			t.Fatal(err)
		}
	}
	team := admin.do("POST", "/teams", map[string]any{"slug": "payments", "name": "Payments"}, 201)
	admin.do("POST", "/apps", map[string]any{"key": "ledger", "name": "General Ledger", "owner_team_id": get(team, "team", "id")}, 201)
	repo := admin.do("POST", "/apps/ledger/repos", map[string]any{"name": "api"}, 201)
	f := ciFixture{ts: ts, srv: srv, admin: admin, adminID: adminID, work: t.TempDir(), repoID: get(repo, "repository", "id").(string)}
	f.git(t, "init", "--quiet", "--initial-branch=main")
	u, _ := url.Parse(ts.URL)
	u.User = url.UserPassword("admin", "password123")
	u.Path = "/git/ledger/api.git"
	f.git(t, "remote", "add", "origin", u.String())
	return f
}

func (f ciFixture) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Admin", "-c", "user.email=admin@example.com"}, args...)...)
	cmd.Dir = f.work
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// push commits files to a branch and pushes it, returning the commit.
func (f ciFixture) push(t *testing.T, branch string, files map[string]string) string {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(f.work, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.git(t, "add", "-A")
	f.git(t, "commit", "--quiet", "--allow-empty", "-m", "change")
	f.git(t, "push", "--quiet", "origin", "HEAD:refs/heads/"+branch)
	return f.git(t, "rev-parse", "HEAD")
}

// startRunner runs a local runner against the test server until the test
// ends.
func (f ciFixture) startRunner(t *testing.T) {
	t.Helper()
	token, err := f.srv.RegisterEmbeddedRunner(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.Run(ctx, runner.Config{URL: f.ts.URL, Token: token, Executor: runner.Local{Dir: t.TempDir()}, Concurrency: 2})
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// waitPipeline waits until pipeline number n of ledger/api has finished.
func (f ciFixture) waitPipeline(t *testing.T, n string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		p := f.admin.do("GET", "/apps/ledger/repos/api/pipelines/"+n, nil, 200)
		switch p["status"] {
		case "success", "failure", "error", "cancelled":
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("pipeline %s did not finish: %v", n, p)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func jobsByName(p map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, j := range p["jobs"].([]any) {
		jm := j.(map[string]any)
		out[jm["name"].(string)] = jm
	}
	return out
}

func (f ciFixture) jobLog(t *testing.T, jobID string) string {
	t.Helper()
	var b strings.Builder
	for _, c := range f.admin.do("GET", "/jobs/"+jobID+"/logs", nil, 200)["chunks"].([]any) {
		b.WriteString(c.(map[string]any)["data"].(string))
	}
	return b.String()
}

// hookRecorder is a webhook receiver that checks signatures.
type hookRecorder struct {
	mu     sync.Mutex
	secret string
	events []map[string]any
	t      *testing.T
}

func (h *hookRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	mac := hmac.New(sha256.New, []byte(h.secret))
	mac.Write(body)
	if r.Header.Get("X-Orbit-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		h.t.Errorf("bad webhook signature for %s", r.Header.Get("X-Orbit-Event"))
	}
	var ev map[string]any
	json.Unmarshal(body, &ev)
	h.mu.Lock()
	h.events = append(h.events, ev)
	h.mu.Unlock()
}

func (h *hookRecorder) kinds() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, e := range h.events {
		out = append(out, e["event"].(string))
	}
	return out
}

func (h *hookRecorder) find(event string) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.events {
		if e["event"] == event {
			return e
		}
	}
	return nil
}

func TestChecksAPIWithAccessTokenAndWebhooks(t *testing.T) {
	f := newCIFixture(t, nil)
	reader, _ := register(t, f.ts, "reader")

	hooks := &hookRecorder{t: t}
	receiver := httptest.NewServer(hooks)
	defer receiver.Close()
	created := f.admin.do("POST", "/apps/ledger/webhooks", map[string]any{"url": receiver.URL, "events": []string{"push", "check"}}, 201)
	hooks.secret = created["secret"].(string)
	reader.do("GET", "/apps/ledger/webhooks", nil, 403)

	sha := f.push(t, "main", map[string]string{"README.md": "# api\n"})
	f.srv.WaitAgents()
	push := hooks.find("push")
	if push == nil || push["after"] != sha || push["branch"] != "main" || get(push, "repository", "name") != "api" {
		t.Fatalf("push event = %v", push)
	}

	// An external CI reports with a personal access token.
	tok := f.admin.do("POST", "/me/tokens", map[string]any{"name": "jenkins"}, 201)
	ci := &client{t: t, base: f.ts.URL, token: tok["secret"].(string)}
	check := map[string]any{"name": "jenkins/build", "status": "running", "details_url": "https://ci.example.com/42"}
	ci.do("POST", "/apps/ledger/repos/api/commits/"+sha+"/checks", check, 201)
	check["status"] = "failure"
	check["summary"] = "2 tests failed"
	got := ci.do("POST", "/apps/ledger/repos/api/commits/"+sha+"/checks", check, 200)
	if got["status"] != "failure" || get(got, "author", "username") != "admin" {
		t.Fatalf("check = %v", got)
	}
	ci.do("POST", "/apps/ledger/repos/api/commits/main/checks", check, 404) // branch names move; checks need a SHA
	ci.do("POST", "/apps/ledger/repos/api/commits/"+sha+"/checks", map[string]any{"name": "orbit/test", "status": "success"}, 400)
	ci.do("POST", "/apps/ledger/repos/api/commits/"+sha+"/checks", map[string]any{"name": "x", "status": "great"}, 400)
	reader.do("POST", "/apps/ledger/repos/api/commits/"+sha+"/checks", check, 403)
	ci.do("POST", "/apps/ledger/repos/api/commits/"+sha+"/checks", map[string]any{"name": "lint", "status": "success"}, 201)

	list := reader.do("GET", "/apps/ledger/repos/api/commits/main/checks", nil, 200)
	if list["state"] != "failure" || len(list["checks"].([]any)) != 2 || list["commit"] != sha {
		t.Fatalf("checks = %v", list)
	}
	commits := reader.do("GET", "/apps/ledger/repos/api/commits", nil, 200)["items"].([]any)
	if commits[0].(map[string]any)["checks_state"] != "failure" {
		t.Fatalf("commits = %v", commits)
	}

	f.srv.WaitAgents()
	if ev := hooks.find("check"); ev == nil || get(ev, "check", "name") == nil {
		t.Fatalf("webhook events = %v", hooks.kinds())
	}
	deliveries := f.admin.do("GET", "/apps/ledger/webhooks/"+get(created, "webhook", "id").(string)+"/deliveries", nil, 200)["items"].([]any)
	if len(deliveries) < 4 || deliveries[0].(map[string]any)["status_code"].(float64) != 200 {
		t.Fatalf("deliveries = %v", deliveries)
	}

	// Revoked tokens stop working at once.
	f.admin.do("DELETE", "/me/tokens/"+get(tok, "token", "id").(string), nil, 204)
	ci.do("GET", "/me", nil, 401)
}

const testPipeline = `jobs:
  test:
    script:
      - echo "testing $ORBIT_REPO at $ORBIT_REF"
      - test -f README.md
  lint:
    script: [exit 3]
  build:
    needs: [test, lint]
    script: [echo never]
  docs:
    needs: [test]
    script: [echo docs]
`

func TestPipelineRunsOnPushAndReportsChecks(t *testing.T) {
	f := newCIFixture(t, nil)
	hooks := &hookRecorder{t: t}
	receiver := httptest.NewServer(hooks)
	defer receiver.Close()
	hooks.secret = f.admin.do("POST", "/apps/ledger/webhooks", map[string]any{"url": receiver.URL, "repo": "api", "events": []string{"pipeline"}}, 201)["secret"].(string)

	f.startRunner(t)
	sha := f.push(t, "main", map[string]string{"README.md": "# api\n", ".orbit/pipeline.yml": testPipeline})

	p := f.waitPipeline(t, "1")
	jobs := jobsByName(p)
	if p["status"] != "failure" || p["commit"] != sha || p["trigger"] != "push" || get(p, "triggered_by", "username") != "admin" {
		t.Fatalf("pipeline = %v", p)
	}
	for name, want := range map[string]string{"test": "success", "lint": "failure", "build": "skipped", "docs": "success"} {
		if jobs[name]["status"] != want {
			t.Errorf("job %s = %v, want %s", name, jobs[name]["status"], want)
		}
	}
	if jobs["lint"]["exit_code"].(float64) != 3 {
		t.Errorf("lint exit code = %v", jobs["lint"]["exit_code"])
	}
	if log := f.jobLog(t, jobs["test"]["id"].(string)); !strings.Contains(log, "testing api at main") || !strings.Contains(log, "$ test -f README.md") {
		t.Errorf("test log:\n%s", log)
	}

	checks := f.admin.do("GET", "/apps/ledger/repos/api/commits/"+sha+"/checks", nil, 200)
	states := map[string]string{}
	for _, c := range checks["checks"].([]any) {
		states[c.(map[string]any)["name"].(string)] = c.(map[string]any)["status"].(string)
	}
	if states["orbit/test"] != "success" || states["orbit/lint"] != "failure" || states["orbit/build"] != "skipped" || checks["state"] != "failure" {
		t.Fatalf("checks = %v", states)
	}

	f.srv.WaitAgents()
	if ev := hooks.find("pipeline"); ev == nil || get(ev, "pipeline", "status") != "failure" {
		t.Fatalf("pipeline webhook = %v (events %v)", ev, hooks.kinds())
	}

	// A broken pipeline file shows up as a failed pipeline, not silence.
	f.push(t, "main", map[string]string{".orbit/pipeline.yml": "jobs:\n  a:\n    needs: [b]\n    script: x\n"})
	if broken := f.waitPipeline(t, "2"); broken["status"] != "error" || !strings.Contains(broken["error"].(string), "does not exist") {
		t.Fatalf("broken pipeline = %v", broken)
	}

	// Manual runs and cancelling.
	f.push(t, "main", map[string]string{".orbit/pipeline.yml": "when:\n  branches: [release/*]\njobs:\n  slow:\n    script: [sleep 30]\n"})
	f.admin.do("GET", "/apps/ledger/repos/api/pipelines/3", nil, 404) // main does not match the branch filter
	run := f.admin.do("POST", "/apps/ledger/repos/api/pipelines", map[string]any{"ref": "main"}, 201)
	if run["number"].(float64) != 3 || run["trigger"] != "manual" {
		t.Fatalf("manual run = %v", run)
	}
	deadline := time.Now().Add(20 * time.Second)
	for jobsByName(f.admin.do("GET", "/apps/ledger/repos/api/pipelines/3", nil, 200))["slow"]["status"] != "running" {
		if time.Now().After(deadline) {
			t.Fatal("slow job did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	f.admin.do("POST", "/apps/ledger/repos/api/pipelines/3/cancel", nil, 200)
	if p := f.waitPipeline(t, "3"); p["status"] != "cancelled" {
		t.Fatalf("cancelled pipeline = %v", p)
	}
}

func TestRunnerRegistrationNeedsTheRightRole(t *testing.T) {
	f := newCIFixture(t, nil)
	dev, _ := register(t, f.ts, "dev")
	dev.do("POST", "/runners", map[string]any{"name": "shared"}, 403)
	dev.do("POST", "/runners", map[string]any{"name": "ledger", "app_key": "ledger"}, 403)
	out := f.admin.do("POST", "/runners", map[string]any{"name": "ledger", "app_key": "ledger"}, 201)
	if !strings.HasPrefix(out["token"].(string), runnerPrefix) {
		t.Fatalf("runner = %v", out)
	}
	list := f.admin.do("GET", "/runners?app=ledger", nil, 200)["items"].([]any)
	if len(list) != 1 {
		t.Fatalf("runners = %v", list)
	}

	// Runner and job endpoints need their tokens.
	rc := &client{t: t, base: f.ts.URL, token: "nope"}
	req, _ := http.NewRequest("POST", f.ts.URL+"/api/runner/v1/jobs/next", nil)
	req.Header.Set("Authorization", "Bearer "+rc.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("next with a bad token: %d", res.StatusCode)
	}
}

func coderAgent() agent.Definition {
	return agent.Definition{Name: "coder", DisplayName: "Coder", Model: "fake", Role: "developer", Apps: []string{"*"}, MaxSteps: 4, Runs: "job"}
}

func TestAgentJobFromChatProposesABranch(t *testing.T) {
	f := newCIFixture(t, append(fakeAssistant(), coderAgent()))
	f.startRunner(t)
	f.push(t, "main", map[string]string{"README.md": "# api\n", ".orbit/pipeline.yml": "jobs:\n  check:\n    script: [cat NOTES.md]\n"})
	first := f.waitPipeline(t, "1")
	if first["status"] != "failure" {
		t.Fatalf("pipeline on main = %v", first) // NOTES.md does not exist yet
	}

	channels := f.admin.do("GET", "/channels?app=ledger", nil, 200)["items"].([]any)
	channelID := channels[0].(map[string]any)["id"].(string)

	// Someone who may only read cannot start jobs.
	reader, _ := register(t, f.ts, "reader")
	q := reader.do("POST", "/channels/"+channelID+"/messages", map[string]any{"body": "@coder add a file X.md"}, 201)
	thread := reader.do("GET", "/channels/"+channelID+"/messages?thread="+q["id"].(string), nil, 200)["items"].([]any)
	if len(thread) != 2 || !strings.Contains(thread[1].(map[string]any)["body"].(string), "needs the developer role") {
		t.Fatalf("thread = %v", thread)
	}

	msg := f.admin.do("POST", "/channels/"+channelID+"/messages", map[string]any{"body": "@coder add a file NOTES.md saying hello from the agent"}, 201)
	p := f.waitPipeline(t, "2")
	job := p["jobs"].([]any)[0].(map[string]any)
	if p["status"] != "success" || p["trigger"] != "agent" || job["kind"] != "agent" || job["has_patch"] != true {
		t.Fatalf("agent pipeline = %v", p)
	}
	if log := f.jobLog(t, job["id"].(string)); !strings.Contains(log, "→ write_file") || !strings.Contains(log, "Changed 1 file") {
		t.Errorf("agent log:\n%s", log)
	}
	// Agent pipelines do not report checks on the commit they started from.
	if checks := f.admin.do("GET", "/apps/ledger/repos/api/commits/main/checks", nil, 200)["checks"].([]any); len(checks) != 1 {
		t.Errorf("checks on main = %v", checks)
	}

	f.srv.WaitAgents()
	var reply map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for {
		thread := f.admin.do("GET", "/channels/"+channelID+"/messages?thread="+msg["id"].(string), nil, 200)["items"].([]any)
		reply = thread[len(thread)-1].(map[string]any)
		if len(reply["attachments"].([]any)) == 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	var proposal map[string]any
	var jobAtt map[string]any
	for _, a := range reply["attachments"].([]any) {
		switch a.(map[string]any)["type"] {
		case "proposal":
			proposal = a.(map[string]any)["proposal"].(map[string]any)
		case "job":
			jobAtt = a.(map[string]any)["job"].(map[string]any)
		}
	}
	if proposal == nil || proposal["kind"] != "branch" || proposal["status"] != "pending" || jobAtt == nil || jobAtt["status"] != "success" {
		t.Fatalf("reply = %v", reply)
	}
	if !strings.Contains(proposal["summary"].(string), "agent/coder-2") || !strings.Contains(reply["body"].(string), "NOTES.md") {
		t.Fatalf("proposal = %v, reply %q", proposal, reply["body"])
	}
	patch, _ := http.Get(f.ts.URL + "/api/v1/jobs/" + job["id"].(string) + "/patch")
	patch.Body.Close()
	if patch.StatusCode != 401 {
		t.Errorf("patch without sign-in: %d", patch.StatusCode)
	}

	// Only the asker pushes the branch; it runs the branch's pipeline and
	// gets a merge request into the branch the job started from.
	reader.do("POST", "/agent-proposals/"+proposal["id"].(string)+"/apply", nil, 403)
	applied := f.admin.do("POST", "/agent-proposals/"+proposal["id"].(string)+"/apply", nil, 200)
	if get(applied, "result", "type") != "merge_request" || get(applied, "result", "subtitle") != "ledger!1 · open" {
		t.Fatalf("applied = %v", applied)
	}
	mr := f.admin.do("GET", "/apps/ledger/merge-requests/1", nil, 200)
	if get(mr, "merge_request", "source_branch") != "agent/coder-2" || get(mr, "merge_request", "target_branch") != "main" {
		t.Fatalf("merge request = %v", mr["merge_request"])
	}
	blob := f.admin.do("GET", "/apps/ledger/repos/api/blob?ref=agent/coder-2&path=NOTES.md", nil, 200)
	if blob["content"] != "hello from the agent\n" {
		t.Fatalf("NOTES.md = %v", blob)
	}
	branchRun := f.waitPipeline(t, "3")
	if branchRun["ref"] != "agent/coder-2" || branchRun["status"] != "success" {
		t.Fatalf("branch pipeline = %v", branchRun)
	}
	commits := f.admin.do("GET", "/apps/ledger/repos/api/commits?ref=agent/coder-2", nil, 200)["items"].([]any)
	if c := commits[0].(map[string]any); c["author"] != "Coder" || c["checks_state"] != "success" {
		t.Fatalf("agent commit = %v", c)
	}
	f.admin.do("POST", "/agent-proposals/"+proposal["id"].(string)+"/apply", nil, 409)
}

func TestAgentJobInPipelineFile(t *testing.T) {
	f := newCIFixture(t, []agent.Definition{coderAgent()})
	f.startRunner(t)
	f.push(t, "main", map[string]string{".orbit/pipeline.yml": `jobs:
  docs:
    agent: coder
    prompt: add a file CHANGELOG.md saying nothing yet
  unknown:
    script: [true]
`})
	p := f.waitPipeline(t, "1")
	jobs := jobsByName(p)
	if p["status"] != "success" || jobs["docs"]["has_patch"] != true || jobs["docs"]["agent_run_id"] == nil {
		t.Fatalf("pipeline = %v", p)
	}
	run := f.admin.do("GET", "/agent-runs/"+jobs["docs"]["agent_run_id"].(string), nil, 200)
	if get(run, "run", "status") != "done" || len(run["proposals"].([]any)) != 1 {
		t.Fatalf("run = %v", run)
	}

	f.push(t, "main", map[string]string{".orbit/pipeline.yml": "jobs:\n  x:\n    agent: nobody\n    prompt: hi\n"})
	if p := f.waitPipeline(t, "2"); p["status"] != "error" || !strings.Contains(p["error"].(string), "no agent named") {
		t.Fatalf("pipeline with an unknown agent = %v", p)
	}
}
