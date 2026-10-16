package server

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// devOnLedger signs up a user with the developer role on ledger.
func devOnLedger(t *testing.T, f ciFixture, name string) *client {
	t.Helper()
	c, id := register(t, f.ts, name)
	team := f.admin.do("POST", "/teams", map[string]any{"slug": name + "-team", "name": name}, 201)
	teamID := get(team, "team", "id").(string)
	f.admin.do("PUT", "/teams/"+teamID+"/members/"+id, map[string]any{"role": "member"}, 200)
	f.admin.do("PUT", "/apps/ledger/teams/"+teamID, map[string]any{"role": "developer"}, 200)
	return c
}

func messages(c *client, channelID string) []map[string]any {
	var out []map[string]any
	for _, m := range c.do("GET", "/channels/"+channelID+"/messages", nil, 200)["items"].([]any) {
		out = append(out, m.(map[string]any))
	}
	return out
}

func lastBody(c *client, channelID string) string {
	ms := messages(c, channelID)
	if len(ms) == 0 {
		return ""
	}
	return ms[len(ms)-1]["body"].(string)
}

func TestMergeRequestReviewAndMerge(t *testing.T) {
	f := newCIFixture(t, nil)
	reviewer := devOnLedger(t, f, "reviewer")
	f.push(t, "main", map[string]string{"calc.go": "package calc\n\nfunc Add(a, b int) int {\n\treturn a - b\n}\n", "README.md": "# api\n"})
	issue := f.admin.do("POST", "/apps/ledger/issues", map[string]any{"title": "Add subtracts"}, 201)
	issueChannel := get(issue, "issue", "channel_id").(string)

	f.git(t, "checkout", "--quiet", "-b", "fix-add")
	head1 := f.push(t, "fix-add", map[string]string{"calc.go": "package calc\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n"})

	// Nothing to merge, unknown branches and readers are refused.
	f.admin.do("POST", "/apps/ledger/merge-requests", map[string]any{"repo": "api", "source_branch": "main"}, 400)
	f.admin.do("POST", "/apps/ledger/merge-requests", map[string]any{"repo": "api", "source_branch": "nope"}, 400)
	reader, _ := register(t, f.ts, "reader")
	reader.do("POST", "/apps/ledger/merge-requests", map[string]any{"repo": "api", "source_branch": "fix-add"}, 403)

	created := f.admin.do("POST", "/apps/ledger/merge-requests", map[string]any{
		"repo": "api", "source_branch": "fix-add", "body": "Fixes #1.",
	}, 201)
	mr := created["merge_request"].(map[string]any)
	if mr["number"].(float64) != 1 || mr["target_branch"] != "main" || mr["title"] != "change" || mr["head_sha"] != head1 {
		t.Fatalf("merge request = %v", mr)
	}
	f.admin.do("POST", "/apps/ledger/merge-requests", map[string]any{"repo": "api", "source_branch": "fix-add"}, 409)
	if !get(created, "merge_status", "mergeable").(bool) {
		t.Fatalf("merge status = %v", created["merge_status"])
	}
	// The issue it closes hears about it, and links back.
	if body := lastBody(f.admin, issueChannel); !strings.Contains(body, "merge request ledger!1") {
		t.Fatalf("issue note = %q", body)
	}
	links := f.admin.do("GET", "/apps/ledger/issues/1", nil, 200)["links"].([]any)
	if len(links) != 1 || get(links[0].(map[string]any), "entity", "type") != "merge_request" || links[0].(map[string]any)["kind"] != "closes" {
		t.Fatalf("issue links = %v", links)
	}

	diff := f.admin.do("GET", "/apps/ledger/merge-requests/1/diff", nil, 200)
	files := diff["files"].([]any)
	file := files[0].(map[string]any)
	if len(files) != 1 || file["new_path"] != "calc.go" || file["additions"].(float64) != 1 || file["deletions"].(float64) != 1 {
		t.Fatalf("diff = %v", diff)
	}
	var added map[string]any
	for _, l := range file["hunks"].([]any)[0].(map[string]any)["lines"].([]any) {
		if l.(map[string]any)["kind"] == "add" {
			added = l.(map[string]any)
		}
	}
	if added["text"] != "\treturn a + b" || added["new"].(float64) != 4 {
		t.Fatalf("added line = %v", added)
	}

	// An inline comment is a message in the review discussion, pinned to the
	// line at the source tip, and links the merge request to the code.
	channel := mr["channel_id"].(string)
	comment := reviewer.do("POST", "/channels/"+channel+"/messages", map[string]any{
		"body": "Add a test for this?",
		"code": map[string]any{"app": "ledger", "repo": "api", "ref": head1, "path": "calc.go", "start": 4, "end": 4},
	}, 201)
	if a := comment["attachments"].([]any)[0].(map[string]any); get(a, "code", "commit") != head1 || a["lines"].([]any)[0] != "\treturn a + b" {
		t.Fatalf("comment attachment = %v", a)
	}
	got := f.admin.do("GET", "/apps/ledger/merge-requests/1", nil, 200)
	var codeLink bool
	for _, l := range got["links"].([]any) {
		codeLink = codeLink || get(l.(map[string]any), "entity", "type") == "code"
	}
	if !codeLink {
		t.Fatalf("merge request links = %v", got["links"])
	}
	msgEntity := f.admin.do("GET", "/links?type=code&id="+fmt.Sprintf("ledger/api@%s:calc.go%%23L4-L4", head1), nil, 200)["items"].([]any)
	if len(msgEntity) != 2 {
		t.Fatalf("code links = %v", msgEntity)
	}

	// Failing checks and requested changes block the merge; reviewers cannot
	// be the author.
	f.admin.do("POST", "/apps/ledger/merge-requests/1/reviews", map[string]any{"state": "approved"}, 403)
	reviewer.do("POST", "/apps/ledger/merge-requests/1/reviews", map[string]any{"state": "changes_requested", "body": "needs a test"}, 200)
	f.admin.do("POST", "/apps/ledger/repos/api/commits/"+head1+"/checks", map[string]any{"name": "ext/test", "status": "failure"}, 201)
	got = f.admin.do("GET", "/apps/ledger/merge-requests/1", nil, 200)
	if over := get(got, "merge_status", "overridable").([]any); len(over) != 2 {
		t.Fatalf("overridable = %v", over)
	}
	reviewer.do("POST", "/apps/ledger/merge-requests/1/merge", map[string]any{}, 409)
	reviewer.do("POST", "/apps/ledger/merge-requests/1/merge", map[string]any{"force": true}, 403)

	// A push to the source branch updates the merge request.
	head2 := f.push(t, "fix-add", map[string]string{"calc_test.go": "package calc\n"})
	got = f.admin.do("GET", "/apps/ledger/merge-requests/1", nil, 200)
	if get(got, "merge_request", "head_sha") != head2 || !get(got, "reviews").([]any)[0].(map[string]any)["outdated"].(bool) {
		t.Fatalf("after push = %v", got)
	}
	if body := lastBody(f.admin, channel); body != "admin pushed 1 commit" {
		t.Fatalf("push note = %q", body)
	}
	// The comment was made on the previous version; its line did not change,
	// so the diff still places it.
	moved := f.admin.do("GET", "/apps/ledger/merge-requests/1/diff", nil, 200)["moved"].([]any)
	if len(moved) != 1 || moved[0].(map[string]any)["message_id"] != comment["id"] || moved[0].(map[string]any)["end"].(float64) != 4 ||
		moved[0].(map[string]any)["side"] != "new" {
		t.Fatalf("moved = %v", moved)
	}
	commits := f.admin.do("GET", "/apps/ledger/merge-requests/1/commits", nil, 200)["items"].([]any)
	if len(commits) != 2 {
		t.Fatalf("commits = %v", commits)
	}
	reviewer.do("POST", "/apps/ledger/merge-requests/1/reviews", map[string]any{"state": "approved"}, 200)
	f.admin.do("POST", "/apps/ledger/repos/api/commits/"+head2+"/checks", map[string]any{"name": "ext/test", "status": "success"}, 201)

	merged := reviewer.do("POST", "/apps/ledger/merge-requests/1/merge", map[string]any{"strategy": "squash", "delete_source_branch": true}, 200)
	m := merged["merge_request"].(map[string]any)
	if m["status"] != "merged" || get(m, "merged_by", "username") != "reviewer" {
		t.Fatalf("merged = %v", m)
	}
	blob := f.admin.do("GET", "/apps/ledger/repos/api/blob?ref=main&path=calc.go", nil, 200)
	if !strings.Contains(blob["content"].(string), "a + b") {
		t.Fatalf("main calc.go = %v", blob["content"])
	}
	log := f.admin.do("GET", "/apps/ledger/repos/api/commits?ref=main", nil, 200)["items"].([]any)
	if c := log[0].(map[string]any); c["sha"] != *strPtr(m["merge_commit_sha"]) || c["subject"] != "change (ledger!1)" || c["author"] != "admin" {
		t.Fatalf("squash commit = %v", c)
	}
	branches := get(f.admin.do("GET", "/apps/ledger/repos/api", nil, 200), "branches").([]any)
	if len(branches) != 1 {
		t.Fatalf("branches after delete = %v", branches)
	}
	if st := get(f.admin.do("GET", "/apps/ledger/issues/1", nil, 200), "issue", "status"); st != "done" {
		t.Fatalf("issue status = %v", st)
	}
	if body := lastBody(f.admin, issueChannel); body != "reviewer closed this by merging ledger!1" {
		t.Fatalf("issue note = %q", body)
	}
	// The reviewed diff stays readable after the branch is gone.
	if files := f.admin.do("GET", "/apps/ledger/merge-requests/1/diff", nil, 200)["files"].([]any); len(files) != 2 {
		t.Fatalf("diff after merge = %v", files)
	}
	reviewer.do("POST", "/apps/ledger/merge-requests/1/merge", map[string]any{}, 409)
	list := f.admin.do("GET", "/apps/ledger/merge-requests?status=merged", nil, 200)["items"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["checks_state"] != "success" {
		t.Fatalf("merged list = %v", list)
	}
}

func strPtr(v any) *string {
	s, _ := v.(string)
	return &s
}

func TestMergeRequestConflictsAndMergeByPush(t *testing.T) {
	f := newCIFixture(t, nil)
	f.push(t, "main", map[string]string{"a.txt": "one\n"})
	f.git(t, "checkout", "--quiet", "-b", "left")
	f.push(t, "left", map[string]string{"a.txt": "left\n"})
	f.admin.do("POST", "/apps/ledger/merge-requests", map[string]any{"repo": "api", "source_branch": "left", "draft": true}, 201)
	got := f.admin.do("GET", "/apps/ledger/merge-requests/1", nil, 200)
	if get(got, "merge_status", "mergeable") != false || !strings.Contains(get(got, "merge_status", "reason").(string), "draft") {
		t.Fatalf("draft status = %v", got["merge_status"])
	}
	f.admin.do("PATCH", "/apps/ledger/merge-requests/1", map[string]any{"draft": false}, 200)

	// Someone changes the same line on main.
	f.git(t, "checkout", "--quiet", "main")
	f.push(t, "main", map[string]string{"a.txt": "main\n"})
	got = f.admin.do("GET", "/apps/ledger/merge-requests/1", nil, 200)
	if c := get(got, "merge_status", "conflicts").([]any); len(c) != 1 || c[0] != "a.txt" {
		t.Fatalf("conflicts = %v", got["merge_status"])
	}
	f.admin.do("POST", "/apps/ledger/merge-requests/1/merge", map[string]any{}, 409)

	// Closing and reopening.
	f.admin.do("PATCH", "/apps/ledger/merge-requests/1", map[string]any{"status": "closed"}, 200)
	if l := f.admin.do("GET", "/apps/ledger/merge-requests", nil, 200)["items"].([]any); len(l) != 0 {
		t.Fatalf("open list = %v", l)
	}
	f.admin.do("PATCH", "/apps/ledger/merge-requests/1", map[string]any{"status": "open"}, 200)

	// Resolving the conflict on the branch and pushing it to main by hand
	// marks the merge request merged.
	f.git(t, "checkout", "--quiet", "left")
	f.git(t, "merge", "--quiet", "-X", "ours", "main", "-m", "merge main")
	f.git(t, "push", "--quiet", "origin", "left", "left:main")
	got = f.admin.do("GET", "/apps/ledger/merge-requests/1", nil, 200)
	if get(got, "merge_request", "status") != "merged" {
		t.Fatalf("after push to main = %v", got["merge_request"])
	}
}

func TestCoderAgentPushesToTheMergeRequestItWasAskedIn(t *testing.T) {
	f := newCIFixture(t, append(fakeAssistant(), coderAgent()))
	f.startRunner(t)
	f.push(t, "main", map[string]string{"README.md": "# api\n"})
	f.git(t, "checkout", "--quiet", "-b", "docs")
	head := f.push(t, "docs", map[string]string{"README.md": "# api\n\nThe ledger API.\n"})
	mr := f.admin.do("POST", "/apps/ledger/merge-requests", map[string]any{"repo": "api", "source_branch": "docs", "title": "Document the API"}, 201)
	channel := get(mr, "merge_request", "channel_id").(string)

	q := f.admin.do("POST", "/channels/"+channel+"/messages", map[string]any{"body": "@coder add a file NOTES.md saying reviewed"}, 201)
	p := f.waitPipeline(t, "1")
	if p["ref"] != "docs" || p["commit"] != head || p["status"] != "success" {
		t.Fatalf("agent pipeline = %v", p)
	}
	f.srv.WaitAgents()
	var proposal map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for proposal == nil && time.Now().Before(deadline) {
		thread := f.admin.do("GET", "/channels/"+channel+"/messages?thread="+q["id"].(string), nil, 200)["items"].([]any)
		for _, a := range thread[len(thread)-1].(map[string]any)["attachments"].([]any) {
			if a.(map[string]any)["type"] == "proposal" {
				proposal = a.(map[string]any)["proposal"].(map[string]any)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if proposal == nil || proposal["kind"] != "merge_request_push" || !strings.Contains(proposal["summary"].(string), "ledger!1") {
		t.Fatalf("proposal = %v", proposal)
	}
	applied := f.admin.do("POST", "/agent-proposals/"+proposal["id"].(string)+"/apply", nil, 200)
	if get(applied, "result", "type") != "merge_request" {
		t.Fatalf("applied = %v", applied)
	}
	got := f.admin.do("GET", "/apps/ledger/merge-requests/1", nil, 200)
	if get(got, "merge_request", "head_sha") == head {
		t.Fatalf("merge request head did not move")
	}
	blob := f.admin.do("GET", "/apps/ledger/repos/api/blob?ref=docs&path=NOTES.md", nil, 200)
	if blob["content"] != "reviewed\n" {
		t.Fatalf("NOTES.md = %v", blob)
	}
	if body := lastBody(f.admin, channel); body != "admin pushed 1 commit" {
		t.Fatalf("push note = %q", body)
	}
}
