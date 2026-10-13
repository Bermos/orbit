package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Provider-neutral conversation types. Providers translate them to and from
// their own wire format, so the loop, the tools and the tests never depend on
// one vendor's API.

// Block is one piece of a turn.
type Block struct {
	Type string `json:"type"` // "text", "tool_use" or "tool_result"
	Text string `json:"text,omitempty"`
	// Tool calls and results.
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// Turn is one message in the conversation with the model.
type Turn struct {
	Role   string  `json:"role"` // "user" or "assistant"
	Blocks []Block `json:"blocks"`
	// Raw is the provider's own form of an assistant turn. Providers that must
	// send their output back unchanged (for example with thinking blocks) set
	// it on responses and prefer it over Blocks when it is set.
	Raw any `json:"-"`
}

// ToolSpec describes a tool to the model.
type ToolSpec struct {
	Name        string
	Description string
	// Schema is the JSON schema of the input object.
	Schema map[string]any
}

// Request is one model call.
type Request struct {
	Model     string
	System    string
	Turns     []Turn
	Tools     []ToolSpec
	Effort    string
	MaxTokens int
	// NoMoreTools asks the model to answer without calling tools.
	NoMoreTools bool
}

// Usage counts tokens for one or more calls.
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// Response is the model's turn.
type Response struct {
	Turn       Turn
	StopReason string // "end_turn", "tool_use", "max_tokens", "refusal", ...
	Usage      Usage
}

// Provider is a model backend.
type Provider interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// Providers maps provider names ("anthropic", "fake") to providers.
type Providers map[string]Provider

// Resolve picks the provider for a definition. If the definition's provider
// is not configured it falls back to "fake" and says so, so an instance
// without model credentials still shows the whole flow end to end.
func (p Providers) Resolve(d Definition) (prov Provider, model string, fallback bool, err error) {
	name, model := d.ProviderAndModel()
	if prov, ok := p[name]; ok {
		return prov, model, false, nil
	}
	if prov, ok := p["fake"]; ok {
		return prov, "fake", true, nil
	}
	names := make([]string, 0, len(p))
	for n := range p {
		names = append(names, n)
	}
	sort.Strings(names)
	return nil, "", false, fmt.Errorf("provider %q is not configured (have: %s)", name, strings.Join(names, ", "))
}

// Text returns the turn's text blocks joined.
func (t Turn) Text() string {
	var parts []string
	for _, b := range t.Blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, strings.TrimSpace(b.Text))
		}
	}
	return strings.Join(parts, "\n\n")
}
