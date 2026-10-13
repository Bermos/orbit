package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDefinition(t *testing.T) {
	d, err := Parse([]byte("---\nname: reviewer\nmodel: anthropic/claude-opus-5-5\napps: [ledger]\ntools: [search]\n---\nReview carefully.\n"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "reviewer" || d.DisplayName != "reviewer" || d.Role != "reader" || d.MaxSteps != 8 || d.Instructions != "Review carefully." {
		t.Fatalf("parsed %+v", d)
	}
	if p, m := d.ProviderAndModel(); p != "anthropic" || m != "claude-opus-5-5" {
		t.Fatalf("provider %q model %q", p, m)
	}
	if !d.AllowsApp("ledger") || d.AllowsApp("billing") || !d.AllowsTool("search") || d.AllowsTool("read_file") {
		t.Fatalf("scope %+v", d)
	}

	for name, src := range map[string]string{
		"no front matter": "name: x\n",
		"unknown field":   "---\nname: bot\nmodel: fake\ncolour: red\n---\n",
		"bad name":        "---\nname: Bot!\nmodel: fake\n---\n",
		"no model":        "---\nname: bot\n---\n",
		"bad role":        "---\nname: bot\nmodel: fake\nrole: god\n---\n",
	} {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestCatalogOverridesBuiltins(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "assistant.md"), []byte("---\nname: assistant\nmodel: fake\n---\nLocal.\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "broken.md"), []byte("nope"), 0o644)
	defs, problems := LoadCatalog(dir)
	if len(defs) != 1 || defs[0].Model != "fake" || defs[0].Instructions != "Local." {
		t.Fatalf("defs = %+v", defs)
	}
	if len(problems) != 1 || !strings.Contains(problems[0].Error(), "broken.md") {
		t.Fatalf("problems = %v", problems)
	}

	defs, problems = LoadCatalog("")
	if len(problems) != 0 || len(defs) != 1 || defs[0].Name != "assistant" || !strings.HasPrefix(defs[0].Model, "anthropic/") {
		t.Fatalf("builtin defs = %+v, %v", defs, problems)
	}
}

func TestProvidersFallBackToFake(t *testing.T) {
	d := Definition{Model: "anthropic/claude-opus-5-5"}
	if _, model, fallback, err := (Providers{"fake": Fake{}}).Resolve(d); err != nil || !fallback || model != "fake" {
		t.Fatalf("model %q fallback %v err %v", model, fallback, err)
	}
	if _, _, _, err := (Providers{}).Resolve(d); err == nil {
		t.Fatal("expected an error without providers")
	}
}

type echoTools struct{ calls []string }

func (e *echoTools) Specs() []ToolSpec {
	return []ToolSpec{{Name: "search"}, {Name: "propose_issue"}}
}

func (e *echoTools) Call(ctx context.Context, name string, input json.RawMessage) (string, error) {
	e.calls = append(e.calls, name+" "+string(input))
	if name == "propose_issue" {
		return "", errors.New("not allowed here")
	}
	return `{"results":[{"title":"Bookings are double counted"}]}`, nil
}

func TestRunLoopWithFake(t *testing.T) {
	tools := &echoTools{}
	req := Request{Turns: []Turn{{Role: "user", Blocks: []Block{{Type: "text", Text: "context"}, {Type: "text", Text: "@assistant double counting?"}}}}}
	var steps []Step
	res, err := Run(context.Background(), Fake{}, req, tools, 4, func(s Step) { steps = append(steps, s) })
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.calls) != 1 || tools.calls[0] != `search {"query":"double counting?"}` {
		t.Fatalf("calls = %v", tools.calls)
	}
	if len(steps) != 1 || !strings.Contains(res.Text, "Bookings are double counted") {
		t.Fatalf("steps %v text %q", steps, res.Text)
	}

	// A failed tool call is reported to the model, which can recover.
	req.Turns[0].Blocks[1].Text = "@assistant create issue: Fix it"
	res, err = Run(context.Background(), Fake{}, req, tools, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Steps[0].Error != "not allowed here" || !strings.Contains(res.Text, "couldn't") {
		t.Fatalf("result %+v", res)
	}
}

type refuser struct{}

func (refuser) Complete(ctx context.Context, req Request) (Response, error) {
	return Response{StopReason: "refusal"}, nil
}

func TestRunReportsRefusal(t *testing.T) {
	_, err := Run(context.Background(), refuser{}, Request{Turns: []Turn{{Role: "user"}}}, &echoTools{}, 2, nil)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v", err)
	}
}
