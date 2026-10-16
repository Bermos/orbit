package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Bermos/orbit/internal/db"
	"github.com/Bermos/orbit/internal/gitrepo"
)

// These tests run against a real Postgres. Point ORBIT_TEST_DATABASE_URL at
// an empty, disposable database; the tests drop and recreate its schema.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts, _ := newTestServerWithAPI(t)
	return ts
}

func newTestServerWithAPI(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	url := os.Getenv("ORBIT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("ORBIT_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `drop schema public cascade; create schema public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	srv := New(pool, gitrepo.Store{Root: t.TempDir()}, nil, "")
	// Tests sign up freely and send webhooks to local receivers.
	srv.Registration = RegistrationOpen
	srv.WebhookAllowedNetworks = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	srv.limits.register = newLimiter(1000, time.Hour)
	ts := httptest.NewServer(srv.Routes())
	srv.BaseURL = ts.URL
	t.Cleanup(ts.Close)
	t.Cleanup(srv.WaitAgents)
	return ts, srv
}

type client struct {
	t     *testing.T
	base  string
	token string
}

func (c *client) do(method, path string, in any, wantStatus int) map[string]any {
	c.t.Helper()
	var body bytes.Buffer
	if in != nil {
		json.NewEncoder(&body).Encode(in)
	}
	req, _ := http.NewRequest(method, c.base+"/api/v1"+path, &body)
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	var out any
	json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != wantStatus {
		c.t.Fatalf("%s %s: status %d, want %d: %v", method, path, res.StatusCode, wantStatus, out)
	}
	if m, ok := out.(map[string]any); ok {
		return m
	}
	return map[string]any{"items": out}
}

func register(t *testing.T, ts *httptest.Server, name string) (*client, string) {
	c := &client{t: t, base: ts.URL}
	out := c.do("POST", "/auth/register", map[string]string{"username": name, "password": "password123"}, 200)
	c.token = out["token"].(string)
	return c, out["user"].(map[string]any)["id"].(string)
}

func get(m map[string]any, keys ...string) any {
	var v any = m
	for _, k := range keys {
		v = v.(map[string]any)[k]
	}
	return v
}

func TestOwnershipMappingDrivesAccessNotPaths(t *testing.T) {
	ts := newTestServer(t)
	admin, _ := register(t, ts, "admin")
	dev, devID := register(t, ts, "dev")

	team := admin.do("POST", "/teams", map[string]any{"slug": "payments", "name": "Payments"}, 201)
	teamID := get(team, "team", "id").(string)
	sub := admin.do("POST", "/teams", map[string]any{"slug": "booking", "name": "Booking", "parent_id": teamID}, 201)
	subID := get(sub, "team", "id").(string)
	admin.do("POST", "/apps", map[string]any{"key": "ledger", "name": "General Ledger", "owner_team_id": teamID}, 201)

	// Everyone signed in can read, but only the mapping grants write access.
	if role := get(dev.do("GET", "/apps/ledger", nil, 200), "my_role"); role != "reader" {
		t.Fatalf("dev role = %v, want reader", role)
	}
	dev.do("POST", "/channels", map[string]any{"app_key": "ledger", "name": "dev-chat"}, 403)

	admin.do("PUT", "/teams/"+subID+"/members/"+devID, map[string]any{"role": "member"}, 200)
	admin.do("PUT", "/apps/ledger/teams/"+subID, map[string]any{"role": "developer"}, 200)
	if role := get(dev.do("GET", "/apps/ledger", nil, 200), "my_role"); role != "developer" {
		t.Fatalf("dev role = %v, want developer", role)
	}

	// Reorganise: rename the sub-team and move it to the top level. The
	// application keeps its key and the dev keeps access.
	admin.do("PATCH", "/teams/"+subID, map[string]any{"slug": "settlement", "name": "Settlement", "parent_id": ""}, 200)
	app := dev.do("GET", "/apps/ledger", nil, 200)
	if get(app, "application", "key") != "ledger" || get(app, "my_role") != "developer" {
		t.Fatalf("after reorg: %v", app)
	}

	// The team space still collects the application.
	space := dev.do("GET", "/teams/"+subID+"/space", nil, 200)
	apps := space["applications"].([]any)
	if len(apps) != 1 || apps[0].(map[string]any)["key"] != "ledger" {
		t.Fatalf("space apps = %v", apps)
	}

	// A team cannot be moved under its own descendant.
	admin.do("PATCH", "/teams/"+teamID, map[string]any{"parent_id": teamID}, 400)
}

func TestIssueAndTodoFromMessageAreLinkedBothWays(t *testing.T) {
	ts := newTestServer(t)
	admin, _ := register(t, ts, "admin")
	team := admin.do("POST", "/teams", map[string]any{"slug": "payments", "name": "Payments"}, 201)
	admin.do("POST", "/apps", map[string]any{"key": "ledger", "name": "General Ledger", "owner_team_id": get(team, "team", "id")}, 201)
	channels := admin.do("GET", "/channels?app=ledger", nil, 200)["items"].([]any)
	chID := channels[0].(map[string]any)["id"].(string)

	msg := admin.do("POST", "/channels/"+chID+"/messages", map[string]any{"body": "Bookings are double counted"}, 201)
	msgID := msg["id"].(string)

	todo := admin.do("POST", "/todos", map[string]any{"source": map[string]string{"type": "message", "id": msgID}}, 201)
	if todo["title"] != "Bookings are double counted" {
		t.Fatalf("todo title = %v", todo["title"])
	}
	if url := get(todo, "source", "url").(string); url != "/channels/"+chID+"?message="+msgID {
		t.Fatalf("todo source url = %v", url)
	}

	issue := admin.do("POST", "/apps/ledger/issues", map[string]any{
		"title": "Double counted bookings", "source": map[string]string{"type": "message", "id": msgID},
	}, 201)
	if get(issue, "issue", "number").(float64) != 1 {
		t.Fatalf("issue number = %v", get(issue, "issue", "number"))
	}

	// The source thread now holds a note pointing at the issue.
	thread := admin.do("GET", "/channels/"+chID+"/messages?thread="+msgID, nil, 200)["items"].([]any)
	if len(thread) != 2 || thread[1].(map[string]any)["author"] != nil {
		t.Fatalf("thread = %v", thread)
	}

	// And the message's links show both the todo and the issue.
	links := admin.do("GET", "/links?type=message&id="+msgID, nil, 200)["items"].([]any)
	kinds := map[string]bool{}
	for _, l := range links {
		lm := l.(map[string]any)
		kinds[get(lm, "entity", "type").(string)] = lm["direction"] == "in"
	}
	if !kinds["todo"] || !kinds["issue"] {
		t.Fatalf("links = %v", links)
	}
}

func TestDiscussCodePinsCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ts := newTestServer(t)
	admin, _ := register(t, ts, "admin")
	team := admin.do("POST", "/teams", map[string]any{"slug": "payments", "name": "Payments"}, 201)
	admin.do("POST", "/apps", map[string]any{"key": "ledger", "name": "General Ledger", "owner_team_id": get(team, "team", "id")}, 201)
	repo := admin.do("POST", "/apps/ledger/repos", map[string]any{"name": "svc"}, 201)

	work := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.com", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\ntwo\nthree\nfour\n"), 0o644)
	run("add", ".")
	run("commit", "-qm", "init")
	cloneURL := repo["clone_url"].(string)
	authURL := "http://admin:password123@" + cloneURL[len("http://"):]
	run("push", "-q", authURL, "main")

	channels := admin.do("GET", "/channels?app=ledger", nil, 200)["items"].([]any)
	chID := channels[0].(map[string]any)["id"].(string)
	msg := admin.do("POST", "/channels/"+chID+"/messages", map[string]any{
		"body": "why?",
		"code": map[string]any{"app": "ledger", "repo": "svc", "ref": "main", "path": "a.txt", "start": 2, "end": 3},
	}, 201)
	att := msg["attachments"].([]any)[0].(map[string]any)
	lines := att["lines"].([]any)
	if len(lines) != 2 || lines[0] != "two" || lines[1] != "three" {
		t.Fatalf("lines = %v", lines)
	}
	ref := get(att, "ref", "id").(string)
	c, err := parseCodeRange(ref)
	if err != nil || c.Path != "a.txt" || c.Start != 2 || c.End != 3 || len(c.Commit) != 40 {
		t.Fatalf("code ref %q parsed to %+v, %v", ref, c, err)
	}
}

func TestParseCodeRange(t *testing.T) {
	in := CodeRange{App: "ledger", Repo: "svc", Commit: "abc123", Path: "dir/with#hash/file.go", Start: 3, End: 9}
	out, err := parseCodeRange(in.ID())
	if err != nil || out != in {
		t.Fatalf("round trip: got %+v, %v", out, err)
	}
}
