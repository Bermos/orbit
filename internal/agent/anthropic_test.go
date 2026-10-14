package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
)

// TestAnthropicWireFormat checks the request Orbit sends and that thinking
// blocks go back to the API unchanged on the next call.
func TestAnthropicWireFormat(t *testing.T) {
	var bodies []map[string]any
	var betas []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		bodies = append(bodies, body)
		betas = append(betas, r.Header.Get("anthropic-beta"))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","stop_reason":"tool_use",
				"content":[{"type":"thinking","thinking":"","signature":"sig"},{"type":"tool_use","id":"tu_1","name":"search","input":{"query":"x"}}],
				"usage":{"input_tokens":10,"output_tokens":5}}`)
			return
		}
		io.WriteString(w, `{"id":"msg_2","type":"message","role":"assistant","model":"claude-opus-5-5","stop_reason":"end_turn",
			"content":[{"type":"text","text":"Found it."}],"usage":{"input_tokens":20,"output_tokens":3}}`)
	}))
	defer api.Close()

	p := NewAnthropic("test-key", option.WithBaseURL(api.URL), option.WithMaxRetries(0))
	req := Request{Model: "claude-opus-5-5", System: "Be brief.", Effort: "low",
		Turns: []Turn{{Role: "user", Blocks: []Block{{Type: "text", Text: "hi"}}}}}
	res, err := Run(context.Background(), p, req, &echoTools{}, Limits{MaxSteps: 3}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Found it." || res.Usage.InputTokens != 30 || res.Usage.OutputTokens != 8 {
		t.Fatalf("result %+v", res)
	}

	first := bodies[0]
	if first["model"] != "claude-opus-5-5" || first["fallbacks"] != "default" || !strings.Contains(betas[0], "server-side-fallback") {
		t.Fatalf("first request %v, betas %q", first, betas[0])
	}
	if first["output_config"].(map[string]any)["effort"] != "low" {
		t.Fatalf("output_config = %v", first["output_config"])
	}
	tools := first["tools"].([]any)
	if len(tools) != 2 || tools[0].(map[string]any)["name"] != "search" {
		t.Fatalf("tools = %v", tools)
	}

	msgs := bodies[1]["messages"].([]any)
	assistant := msgs[1].(map[string]any)["content"].([]any)
	if assistant[0].(map[string]any)["type"] != "thinking" || assistant[0].(map[string]any)["signature"] != "sig" {
		t.Fatalf("thinking block not sent back: %v", assistant)
	}
	result := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if result["type"] != "tool_result" || result["tool_use_id"] != "tu_1" {
		t.Fatalf("tool result = %v", result)
	}
}
