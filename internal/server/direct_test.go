package server

import (
	"strings"
	"testing"

	"github.com/Bermos/orbit/internal/agent"
)

// chatMessages returns a private chat's top-level messages once every agent
// run has finished.
func (f agentFixture) chatMessages(t *testing.T, c *client, chatID string) []map[string]any {
	t.Helper()
	f.srv.WaitAgents()
	var out []map[string]any
	for _, m := range c.do("GET", "/channels/"+chatID+"/messages", nil, 200)["items"].([]any) {
		out = append(out, m.(map[string]any))
	}
	return out
}

func TestPrivateChatWithAgent(t *testing.T) {
	f := newAgentFixture(t, fakeAssistant(), agent.Providers{"fake": agent.Fake{}})
	other, _ := register(t, f.ts, "other")
	f.admin.do("POST", "/todos", map[string]any{"title": "Renew the signing certificate"}, 201)

	chat := f.admin.do("POST", "/direct-chats", map[string]any{"agent": "assistant"}, 201)
	chatID := chat["id"].(string)
	if chat["kind"] != "direct" || chat["agent"] != "assistant" || chat["owner_id"] != f.adminID || chat["name"] != "" {
		t.Fatalf("chat = %v", chat)
	}

	// No mention needed: the chat's agent answers every message, in the
	// chat rather than in a thread, and may read the owner's to-dos.
	msg := f.admin.do("POST", "/channels/"+chatID+"/messages", map[string]any{"body": "What are my todos?"}, 201)
	if msg["private"] != true {
		t.Fatalf("message = %v", msg)
	}
	msgs := f.chatMessages(t, f.admin, chatID)
	if len(msgs) != 2 || get(msgs[1], "author", "username") != "assistant" || msgs[1]["thread_id"] != nil || msgs[1]["private"] != true {
		t.Fatalf("messages = %v", msgs)
	}
	if !strings.Contains(msgs[1]["body"].(string), "Renew the signing certificate") {
		t.Fatalf("reply body = %q", msgs[1]["body"])
	}
	run := attachment(msgs[1], "agent_run")["run"].(map[string]any)
	if run["private"] != true || run["steps"].([]any)[0].(map[string]any)["tool"] != "list_my_todos" {
		t.Fatalf("run = %v", run)
	}

	// The chat takes its title from the first message and is listed for
	// its owner only, never among channels.
	chats := f.admin.do("GET", "/direct-chats", nil, 200)["items"].([]any)
	if len(chats) != 1 || chats[0].(map[string]any)["name"] != "What are my todos?" {
		t.Fatalf("chats = %v", chats)
	}
	for _, c := range f.admin.do("GET", "/channels", nil, 200)["items"].([]any) {
		if c.(map[string]any)["id"] == chatID {
			t.Fatal("private chat listed as a channel")
		}
	}

	// Nobody else can see it, post in it, find it or delete it.
	if chats := other.do("GET", "/direct-chats", nil, 200)["items"].([]any); len(chats) != 0 {
		t.Fatalf("other's chats = %v", chats)
	}
	other.do("GET", "/channels/"+chatID, nil, 404)
	other.do("GET", "/channels/"+chatID+"/messages", nil, 404)
	other.do("POST", "/channels/"+chatID+"/messages", map[string]any{"body": "hello"}, 404)
	other.do("GET", "/messages/"+msg["id"].(string), nil, 404)
	other.do("DELETE", "/direct-chats/"+chatID, nil, 404)
	if hits := other.do("GET", "/search?q=todos", nil, 200)["items"].([]any); len(hits) != 0 {
		t.Fatalf("other's search = %v", hits)
	}

	// Several chats per agent; deleting one leaves the rest.
	second := f.admin.do("POST", "/direct-chats", map[string]any{"agent": "@assistant"}, 201)
	f.admin.do("DELETE", "/direct-chats/"+chatID, nil, 204)
	chats = f.admin.do("GET", "/direct-chats", nil, 200)["items"].([]any)
	if len(chats) != 1 || chats[0].(map[string]any)["id"] != second["id"] {
		t.Fatalf("chats after delete = %v", chats)
	}
	f.admin.do("POST", "/direct-chats", map[string]any{"agent": "nobody"}, 400)
	// Ordinary channels are not private chats.
	f.admin.do("DELETE", "/direct-chats/"+f.channelID, nil, 404)
}

func TestPrivateChatKeepsTheConversation(t *testing.T) {
	p := &scripted{turns: [][]agent.Block{
		{{Type: "text", Text: "The ledger closes on the 3rd."}},
		{{Type: "text", Text: "It moves to the 4th when that is a holiday."}},
	}}
	f := newAgentFixture(t, fakeAssistant(), agent.Providers{"fake": p})
	chatID := f.admin.do("POST", "/direct-chats", map[string]any{"agent": "assistant"}, 201)["id"].(string)

	f.admin.do("POST", "/channels/"+chatID+"/messages", map[string]any{"body": "When does the ledger close?"}, 201)
	f.srv.WaitAgents()
	f.admin.do("POST", "/channels/"+chatID+"/messages", map[string]any{"body": "And on holidays?"}, 201)
	f.srv.WaitAgents()

	if len(p.reqs) != 2 {
		t.Fatalf("requests = %d", len(p.reqs))
	}
	req := p.reqs[1]
	history := req.Turns[0].Blocks[0].Text
	for _, want := range []string{"When does the ledger close?", "The ledger closes on the 3rd."} {
		if !strings.Contains(history, want) {
			t.Fatalf("history misses %q:\n%s", want, history)
		}
	}
	if !strings.Contains(req.System, "a private chat with you") {
		t.Fatalf("system prompt = %s", req.System)
	}
	if names := toolNamesOf(req); !names["list_my_todos"] {
		t.Fatalf("personal tools missing in a private chat: %v", names)
	}
	// The thread name stays the first question.
	chats := f.admin.do("GET", "/direct-chats", nil, 200)["items"].([]any)
	if chats[0].(map[string]any)["name"] != "When does the ledger close?" {
		t.Fatalf("chats = %v", chats)
	}
}

func toolNamesOf(req agent.Request) map[string]bool {
	out := map[string]bool{}
	for _, t := range req.Tools {
		out[t.Name] = true
	}
	return out
}
