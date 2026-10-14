package pipeline

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	spec, err := Parse([]byte(`
when:
  branches: [main, "release/*"]
jobs:
  test:
    image: golang:1.26
    script:
      - go vet ./...
      - go test ./...
    env:
      GOFLAGS: -mod=mod
  build:
    needs: test
    script: go build ./...
    timeout: 5m
  review:
    agent: reviewer
    prompt: Look for missing tests.
    needs: [test]
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Jobs) != 3 || spec.Jobs[0].Name != "test" || spec.Jobs[1].Name != "build" || spec.Jobs[2].Name != "review" {
		t.Fatalf("jobs = %+v", spec.Jobs)
	}
	build := spec.Jobs[1]
	if build.Image != DefaultImage || len(build.Script) != 1 || build.Needs[0] != "test" || time.Duration(build.Timeout) != 5*time.Minute {
		t.Fatalf("build = %+v", build)
	}
	if r := spec.Jobs[2]; r.Kind() != "agent" || r.Image != "" || time.Duration(r.Timeout) != 30*time.Minute {
		t.Fatalf("review = %+v", r)
	}
	for branch, want := range map[string]bool{"main": true, "release/1.2": true, "feature/x": false} {
		if spec.Runs(branch) != want {
			t.Errorf("Runs(%q) = %v", branch, !want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for name, src := range map[string]string{
		"no jobs":        "jobs: {}\n",
		"unknown field":  "jobs:\n  a:\n    script: x\n    colour: red\n",
		"bad name":       "jobs:\n  A!:\n    script: x\n",
		"neither":        "jobs:\n  a:\n    image: x\n",
		"both":           "jobs:\n  a:\n    script: x\n    agent: coder\n    prompt: p\n",
		"agent w/o task": "jobs:\n  a:\n    agent: coder\n",
		"unknown need":   "jobs:\n  a:\n    script: x\n    needs: [b]\n",
		"cycle":          "jobs:\n  a:\n    script: x\n    needs: [b]\n  b:\n    script: x\n    needs: [a]\n",
		"reserved env":   "jobs:\n  a:\n    script: x\n    env:\n      ORBIT_TOKEN: x\n",
		"bad timeout":    "jobs:\n  a:\n    script: x\n    timeout: soon\n",
	} {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestScript(t *testing.T) {
	out, err := exec.Command("sh", "-c", Script([]string{"echo 'it''s' \"$((1+1))\"", "false", "echo never"})).CombinedOutput()
	if err == nil {
		t.Fatal("script should fail at false")
	}
	got := string(out)
	if !strings.Contains(got, "$ echo 'it''s'") || !strings.Contains(got, "its 2") || strings.Contains(got, "never\n") || !strings.Contains(got, "$ false") {
		t.Fatalf("output:\n%s", got)
	}
}
