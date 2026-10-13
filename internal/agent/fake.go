package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Fake is a deterministic stand-in for a model. It needs no credentials, so
// tests and local runs exercise the whole flow: mentions, tool calls,
// permissions and proposals. It reads the question (the last text block of
// the first turn), picks one tool by simple rules, and summarises the result.
type Fake struct{}

var (
	fakeIssue   = regexp.MustCompile(`(?i)\b(?:create|file|open|draft)\s+(?:an?\s+)?issue\b[:\s-]*(.*)`)
	fakeTodo    = regexp.MustCompile(`(?i)\b(?:remind me to|add (?:a )?to-?do)\b[:\s-]*(.*)`)
	fakeMyTodos = regexp.MustCompile(`(?i)\bmy\s+to-?dos\b`)
	fakeMention = regexp.MustCompile(`@[a-z0-9][a-z0-9._-]*`)
)

func (Fake) Complete(ctx context.Context, req Request) (Response, error) {
	if len(req.Turns) == 0 {
		return Response{}, fmt.Errorf("fake: empty conversation")
	}
	has := func(tool string) bool {
		return slices.ContainsFunc(req.Tools, func(t ToolSpec) bool { return t.Name == tool })
	}
	text := func(s string) Response {
		return Response{StopReason: "end_turn", Turn: Turn{Role: "assistant", Blocks: []Block{{Type: "text", Text: s}}}}
	}
	call := func(tool string, input any) Response {
		raw, _ := json.Marshal(input)
		return Response{StopReason: "tool_use", Turn: Turn{Role: "assistant", Blocks: []Block{
			{Type: "tool_use", ToolUseID: fmt.Sprintf("fake_%d", len(req.Turns)), Name: tool, Input: raw},
		}}}
	}

	last := req.Turns[len(req.Turns)-1]
	if len(req.Turns) > 1 && last.Role == "user" && len(last.Blocks) > 0 && last.Blocks[0].Type == "tool_result" {
		return text(summarise(last.Blocks[0])), nil
	}

	first := req.Turns[0].Blocks
	question := ""
	if len(first) > 0 {
		question = first[len(first)-1].Text
	}
	question = strings.TrimSpace(fakeMention.ReplaceAllString(question, ""))

	switch {
	case req.NoMoreTools:
	case fakeIssue.MatchString(question) && has("propose_issue"):
		title := strings.TrimSpace(fakeIssue.FindStringSubmatch(question)[1])
		if title == "" {
			title = "Follow up on this conversation"
		}
		return call("propose_issue", map[string]string{"title": title, "body": "Drafted from the conversation."}), nil
	case fakeTodo.MatchString(question) && has("propose_todo"):
		return call("propose_todo", map[string]string{"title": strings.TrimSpace(fakeTodo.FindStringSubmatch(question)[1])}), nil
	case fakeMyTodos.MatchString(question) && has("list_my_todos"):
		return call("list_my_todos", map[string]any{}), nil
	case question != "" && has("search"):
		return call("search", map[string]string{"query": question}), nil
	}
	return text("I'm Orbit's test model, so I can only search, list your to-dos and draft issues or to-dos."), nil
}

// summarise turns a tool result into a short answer.
func summarise(r Block) string {
	if r.IsError {
		return "I couldn't do that: " + r.Text
	}
	var v any
	if err := json.Unmarshal([]byte(r.Text), &v); err != nil {
		return r.Text
	}
	if m, ok := v.(map[string]any); ok {
		if p, ok := m["proposal"].(map[string]any); ok {
			return fmt.Sprintf("I drafted this %v for you. Confirm it below to create it.", p["kind"])
		}
		for _, key := range []string{"results", "todos", "issues"} {
			if items, ok := m[key].([]any); ok {
				v = items
			}
		}
	}
	items, ok := v.([]any)
	if !ok {
		return "Done."
	}
	if len(items) == 0 {
		return "I didn't find anything about that."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "I found %d %s:", len(items), plural(len(items), "match", "matches"))
	for i, it := range items {
		if i == 5 {
			fmt.Fprintf(&b, "\n- and %d more", len(items)-5)
			break
		}
		m, _ := it.(map[string]any)
		title := m["title"]
		if e, ok := m["entity"].(map[string]any); ok {
			title = fmt.Sprintf("%v (%v)", e["title"], e["subtitle"])
		}
		fmt.Fprintf(&b, "\n- %v", title)
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
