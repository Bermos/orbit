package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Bermos/orbit/internal/agent"
)

// scripted replays canned model turns, one per call, and records requests.
type scripted struct {
	mu    sync.Mutex
	turns [][]agent.Block
	reqs  []agent.Request
}

func (p *scripted) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reqs = append(p.reqs, req)
	if len(p.turns) == 0 {
		return agent.Response{StopReason: "end_turn", Turn: agent.Turn{Role: "assistant", Blocks: []agent.Block{{Type: "text", Text: "done"}}}}, nil
	}
	blocks := p.turns[0]
	p.turns = p.turns[1:]
	stop := "end_turn"
	for _, b := range blocks {
		if b.Type == "tool_use" {
			stop = "tool_use"
		}
	}
	return agent.Response{StopReason: stop, Turn: agent.Turn{Role: "assistant", Blocks: blocks}}, nil
}

func toolUse(name string, input any) agent.Block {
	raw, _ := json.Marshal(input)
	return agent.Block{Type: "tool_use", ToolUseID: "call_" + name, Name: name, Input: raw}
}

// agentFixture is an instance with a ledger application, its #general
// channel and an assistant on the fake model.
type agentFixture struct {
	ts        *httptest.Server
	srv       *Server
	admin     *client
	adminID   string
	channelID string
}

func newAgentFixture(t *testing.T, defs []agent.Definition, providers agent.Providers) agentFixture {
	t.Helper()
	ts, srv := newTestServerWithAPI(t)
	admin, adminID := register(t, ts, "admin")
	if err := srv.EnableAgents(context.Background(), defs, providers); err != nil {
		t.Fatal(err)
	}
	team := admin.do("POST", "/teams", map[string]any{"slug": "payments", "name": "Payments"}, 201)
	admin.do("POST", "/apps", map[string]any{"key": "ledger", "name": "General Ledger", "owner_team_id": get(team, "team", "id")}, 201)
	admin.do("POST", "/apps", map[string]any{"key": "billing", "name": "Billing", "owner_team_id": get(team, "team", "id")}, 201)
	channels := admin.do("GET", "/channels?app=ledger", nil, 200)["items"].([]any)
	return agentFixture{ts: ts, srv: srv, admin: admin, adminID: adminID, channelID: channels[0].(map[string]any)["id"].(string)}
}

func fakeAssistant() []agent.Definition {
	return []agent.Definition{{Name: "assistant", DisplayName: "Assistant", Model: "fake", Role: "reader", Apps: []string{"*"}, MaxSteps: 4}}
}

// reply returns the agent's answer in the thread of root.
func (f agentFixture) reply(t *testing.T, c *client, root string) map[string]any {
	t.Helper()
	f.srv.WaitAgents()
	thread := c.do("GET", "/channels/"+f.channelID+"/messages?thread="+root, nil, 200)["items"].([]any)
	for _, m := range thread {
		mm := m.(map[string]any)
		if a, ok := mm["author"].(map[string]any); ok && a["kind"] == "agent" {
			return mm
		}
	}
	t.Fatalf("no agent reply in thread %v", thread)
	return nil
}

func attachment(m map[string]any, typ string) map[string]any {
	for _, a := range m["attachments"].([]any) {
		if am := a.(map[string]any); am["type"] == typ {
			return am
		}
	}
	return nil
}

func TestAgentAnswersMentionInThread(t *testing.T) {
	f := newAgentFixture(t, fakeAssistant(), agent.Providers{"fake": agent.Fake{}})
	f.admin.do("POST", "/apps/ledger/issues", map[string]any{"title": "Bookings are double counted", "body": "Seen after the month-end run."}, 201)

	msg := f.admin.do("POST", "/channels/"+f.channelID+"/messages", map[string]any{"body": "@assistant double counted bookings?"}, 201)
	reply := f.reply(t, f.admin, msg["id"].(string))

	if !strings.Contains(reply["body"].(string), "Bookings are double counted") {
		t.Fatalf("reply body = %q", reply["body"])
	}
	run := attachment(reply, "agent_run")["run"].(map[string]any)
	steps := run["steps"].([]any)
	if run["status"] != "done" || len(steps) != 1 || steps[0].(map[string]any)["tool"] != "search" {
		t.Fatalf("run = %v", run)
	}
	if run["asker_id"] != f.adminID {
		t.Fatalf("run asker = %v", run["asker_id"])
	}
	// The run is a linked entity of the message that started it.
	links := f.admin.do("GET", "/links?type=message&id="+msg["id"].(string), nil, 200)["items"].([]any)
	if len(links) != 1 || get(links[0].(map[string]any), "entity", "type") != "agent_run" {
		t.Fatalf("links = %v", links)
	}
	// Agents are not people: they cannot sign in and are not listed as users.
	c := &client{t: t, base: f.admin.base}
	c.do("POST", "/auth/login", map[string]string{"username": "assistant", "password": ""}, 401)
	for _, u := range f.admin.do("GET", "/users", nil, 200)["items"].([]any) {
		if u.(map[string]any)["username"] == "assistant" {
			t.Fatal("agent listed as a user")
		}
	}
}

func TestAgentProposalIsConfirmedByTheAsker(t *testing.T) {
	f := newAgentFixture(t, fakeAssistant(), agent.Providers{"fake": agent.Fake{}})
	other, _ := register(t, f.ts, "other")

	msg := f.admin.do("POST", "/channels/"+f.channelID+"/messages", map[string]any{"body": "@assistant create issue: Fix rounding in totals"}, 201)
	reply := f.reply(t, f.admin, msg["id"].(string))
	p := attachment(reply, "proposal")["proposal"].(map[string]any)
	if p["status"] != "pending" || p["kind"] != "issue" || get(p, "input", "app") != "ledger" {
		t.Fatalf("proposal = %v", p)
	}
	// Nothing exists until the asker confirms.
	if issues := f.admin.do("GET", "/apps/ledger/issues", nil, 200)["items"].([]any); len(issues) != 0 {
		t.Fatalf("issues before confirming = %v", issues)
	}
	id := p["id"].(string)
	other.do("POST", "/agent-proposals/"+id+"/apply", nil, 403)
	res := f.admin.do("POST", "/agent-proposals/"+id+"/apply", nil, 200)
	if get(res, "result", "title") != "Fix rounding in totals" {
		t.Fatalf("result = %v", res)
	}
	f.admin.do("POST", "/agent-proposals/"+id+"/apply", nil, 409)

	// The issue links back to the conversation and to the agent run.
	issue := f.admin.do("GET", "/apps/ledger/issues/1", nil, 200)
	kinds := map[string]bool{}
	for _, l := range issue["links"].([]any) {
		kinds[l.(map[string]any)["kind"].(string)] = true
	}
	if !kinds[LinkCreatedFrom] || !kinds[LinkProposedBy] {
		t.Fatalf("issue links = %v", issue["links"])
	}
	if get(issue, "issue", "author", "username") != "admin" {
		t.Fatalf("issue author = %v", get(issue, "issue", "author"))
	}
	// The reply now shows the proposal as applied.
	reply = f.reply(t, f.admin, msg["id"].(string))
	if p := attachment(reply, "proposal")["proposal"].(map[string]any); p["status"] != "applied" {
		t.Fatalf("proposal after apply = %v", p)
	}
}

func TestPrivateAnswerUsesPersonalDataSharedAnswerDoesNot(t *testing.T) {
	f := newAgentFixture(t, fakeAssistant(), agent.Providers{"fake": agent.Fake{}})
	other, _ := register(t, f.ts, "other")
	f.admin.do("POST", "/todos", map[string]any{"title": "Renew the signing certificate"}, 201)

	// Shared: the to-do tool is not even offered, so nothing personal leaks.
	shared := f.admin.do("POST", "/channels/"+f.channelID+"/messages", map[string]any{"body": "@assistant what are my todos?"}, 201)
	reply := f.reply(t, f.admin, shared["id"].(string))
	if strings.Contains(reply["body"].(string), "certificate") {
		t.Fatalf("shared reply leaked a to-do: %q", reply["body"])
	}
	for _, st := range attachment(reply, "agent_run")["run"].(map[string]any)["steps"].([]any) {
		if st.(map[string]any)["tool"] == "list_my_todos" {
			t.Fatal("list_my_todos ran in a shared channel")
		}
	}

	// Private: only the asker sees the question and the answer.
	f.admin.do("POST", "/channels/"+f.channelID+"/messages", map[string]any{"body": "no agent here", "private": true}, 400)
	priv := f.admin.do("POST", "/channels/"+f.channelID+"/messages", map[string]any{"body": "@assistant what are my todos?", "private": true}, 201)
	privID := priv["id"].(string)
	reply = f.reply(t, f.admin, privID)
	if reply["private"] != true || !strings.Contains(reply["body"].(string), "Renew the signing certificate") {
		t.Fatalf("private reply = %v", reply)
	}
	runID := attachment(reply, "agent_run")["run"].(map[string]any)["id"].(string)

	for _, m := range other.do("GET", "/channels/"+f.channelID+"/messages", nil, 200)["items"].([]any) {
		if m.(map[string]any)["id"] == privID {
			t.Fatal("other user sees the private question")
		}
	}
	other.do("GET", "/messages/"+privID, nil, 404)
	other.do("GET", "/agent-runs/"+runID, nil, 404)
	other.do("POST", "/channels/"+f.channelID+"/messages", map[string]any{"body": "me too", "thread_id": privID}, 400)
	// A follow-up in the private thread stays private, mention or not.
	if fu := f.admin.do("POST", "/channels/"+f.channelID+"/messages", map[string]any{"body": "thanks", "thread_id": privID}, 201); fu["private"] != true {
		t.Fatalf("follow-up = %v", fu)
	}
	f.admin.do("GET", "/agent-runs/"+runID, nil, 200)

	// Search finds the private question for its author only.
	if hits := f.admin.do("GET", "/search?q=todos", nil, 200)["items"].([]any); len(hits) != 2 {
		t.Fatalf("admin search hits = %v", hits)
	}
	if hits := other.do("GET", "/search?q=todos", nil, 200)["items"].([]any); len(hits) != 1 {
		t.Fatalf("other search hits = %v", hits)
	}
}

func TestSharedAnswerLeavesOutPrivateMessages(t *testing.T) {
	f := newAgentFixture(t, fakeAssistant(), agent.Providers{"fake": agent.Fake{}})
	priv := f.admin.do("POST", "/channels/"+f.channelID+"/messages", map[string]any{"body": "@assistant is the zebra migration safe?", "private": true}, 201)
	f.reply(t, f.admin, priv["id"].(string))

	// The asker could find their private message, but a shared answer must
	// only use what everyone in the channel can read.
	shared := f.admin.do("POST", "/channels/"+f.channelID+"/messages", map[string]any{"body": "@assistant zebra"}, 201)
	reply := f.reply(t, f.admin, shared["id"].(string))
	if reply["body"] != "I didn't find anything about that." {
		t.Fatalf("shared reply = %q", reply["body"])
	}
}

func TestAgentPermissionIsMinimumOfUserAndAgent(t *testing.T) {
	model := &scripted{turns: [][]agent.Block{
		{toolUse("get_application", map[string]string{"app": "billing"}), toolUse("get_application", map[string]string{"app": "ledger"})},
		{{Type: "text", Text: "ok"}},
	}}
	defs := []agent.Definition{{Name: "ledgerbot", DisplayName: "Ledger bot", Model: "scripted/x", Role: "reader", Apps: []string{"ledger"}, MaxSteps: 3,
		Tools: []string{"get_application"}, Instructions: "Help with the ledger."}}
	f := newAgentFixture(t, defs, agent.Providers{"scripted": model})

	// The admin may read billing, but the agent is limited to ledger.
	msg := f.admin.do("POST", "/channels/"+f.channelID+"/messages", map[string]any{"body": "@ledgerbot compare billing and ledger"}, 201)
	reply := f.reply(t, f.admin, msg["id"].(string))
	steps := attachment(reply, "agent_run")["run"].(map[string]any)["steps"].([]any)
	if len(steps) != 2 {
		t.Fatalf("steps = %v", steps)
	}
	if e, _ := steps[0].(map[string]any)["error"].(string); !strings.Contains(e, "not allowed to use application") {
		t.Fatalf("billing step = %v", steps[0])
	}
	if e := steps[1].(map[string]any)["error"]; e != nil {
		t.Fatalf("ledger step = %v", steps[1])
	}

	// Only the tools the definition lists are offered, and the prompt
	// carries the instructions, the asker and the shared-channel rule.
	req := model.reqs[0]
	if len(req.Tools) != 1 || req.Tools[0].Name != "get_application" {
		t.Fatalf("tools = %v", req.Tools)
	}
	for _, want := range []string{"Help with the ledger.", "@admin", "visible to everyone", "only use these applications: ledger"} {
		if !strings.Contains(req.System, want) {
			t.Fatalf("system prompt lacks %q:\n%s", want, req.System)
		}
	}
	if got := req.Turns[0].Blocks[1].Text; got != "@ledgerbot compare billing and ledger" {
		t.Fatalf("question = %q", got)
	}
}

func TestAgentWithoutCredentialsFallsBackToTestModel(t *testing.T) {
	defs := []agent.Definition{{Name: "assistant", DisplayName: "Assistant", Model: "anthropic/claude-opus-5-5", Role: "reader", Apps: []string{"*"}, MaxSteps: 4}}
	f := newAgentFixture(t, defs, agent.Providers{"fake": agent.Fake{}})
	msg := f.admin.do("POST", "/channels/"+f.channelID+"/messages", map[string]any{"body": "@assistant hello"}, 201)
	reply := f.reply(t, f.admin, msg["id"].(string))
	if !strings.Contains(reply["body"].(string), "built-in test model") {
		t.Fatalf("reply = %q", reply["body"])
	}
}
