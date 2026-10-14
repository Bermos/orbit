// Package pipeline reads Orbit pipeline files and holds the job types the
// server and runners share.
//
// A repository defines its pipeline in .orbit/pipeline.yml:
//
//	when:
//	  branches: [main, "release/*"]   # optional; default: every branch
//	jobs:
//	  test:
//	    image: golang:1.26
//	    script:
//	      - go vet ./...
//	      - go test ./...
//	  build:
//	    image: golang:1.26
//	    needs: [test]
//	    script: [go build ./...]
//	  review:
//	    agent: reviewer                  # an agent job instead of a script
//	    prompt: Review this change for missing tests.
//
// Jobs run in parallel unless they wait for others with needs.
package pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// File is where a repository's pipeline lives.
const File = ".orbit/pipeline.yml"

// DefaultImage runs script jobs that name no image.
const DefaultImage = "alpine:3.21"

const (
	maxJobs           = 50
	defaultTimeout    = 30 * time.Minute
	maxTimeout        = 6 * time.Hour
	defaultAgentLimit = 30 * time.Minute
)

// Spec is a parsed pipeline file.
type Spec struct {
	When struct {
		Branches []string `yaml:"branches"`
	} `yaml:"when"`
	// Jobs in file order.
	Jobs []Job `yaml:"-"`
}

// Job is one job. Script jobs run commands in a container; agent jobs run an
// agent on a checkout of the commit.
type Job struct {
	Name    string            `json:"name"`
	Image   string            `json:"image,omitempty"`
	Script  []string          `json:"script,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Needs   []string          `json:"needs,omitempty"`
	Timeout Duration          `json:"timeout"`
	// Agent jobs.
	Agent  string `json:"agent,omitempty"`
	Prompt string `json:"prompt,omitempty"`
}

// Kind is "agent" or "script".
func (j Job) Kind() string {
	if j.Agent != "" {
		return "agent"
	}
	return "script"
}

// Duration is a time.Duration written as "20m" in YAML and as seconds in JSON.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return fmt.Appendf(nil, "%d", int64(time.Duration(d)/time.Second)), nil
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var secs int64
	if _, err := fmt.Sscan(string(b), &secs); err != nil {
		return err
	}
	*d = Duration(time.Duration(secs) * time.Second)
	return nil
}

var (
	jobNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type rawJob struct {
	Image   string            `yaml:"image"`
	Script  stringList        `yaml:"script"`
	Env     map[string]string `yaml:"env"`
	Needs   stringList        `yaml:"needs"`
	Timeout string            `yaml:"timeout"`
	Agent   string            `yaml:"agent"`
	Prompt  string            `yaml:"prompt"`
}

// stringList accepts a single string or a list of strings.
type stringList []string

func (l *stringList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*l = []string{n.Value}
		return nil
	}
	var v []string
	if err := n.Decode(&v); err != nil {
		return err
	}
	*l = v
	return nil
}

// Parse reads and validates a pipeline file.
func Parse(data []byte) (Spec, error) {
	var doc struct {
		When struct {
			Branches stringList `yaml:"branches"`
		} `yaml:"when"`
		Jobs yaml.Node `yaml:"jobs"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return Spec{}, fmt.Errorf("%s: %w", File, err)
	}
	var spec Spec
	spec.When.Branches = doc.When.Branches
	if doc.Jobs.Kind != yaml.MappingNode || len(doc.Jobs.Content) == 0 {
		return spec, errors.New("the pipeline has no jobs")
	}
	if len(doc.Jobs.Content)/2 > maxJobs {
		return spec, fmt.Errorf("a pipeline can have at most %d jobs", maxJobs)
	}
	seen := map[string]bool{}
	for i := 0; i < len(doc.Jobs.Content); i += 2 {
		name := doc.Jobs.Content[i].Value
		if !jobNameRE.MatchString(name) {
			return spec, fmt.Errorf("invalid job name %q: use lowercase letters, digits, - and _", name)
		}
		if seen[name] {
			return spec, fmt.Errorf("job %q is defined twice", name)
		}
		seen[name] = true
		var raw rawJob
		if err := decodeStrict(doc.Jobs.Content[i+1], &raw); err != nil {
			return spec, fmt.Errorf("job %s: %w", name, err)
		}
		j, err := raw.job(name)
		if err != nil {
			return spec, fmt.Errorf("job %s: %w", name, err)
		}
		spec.Jobs = append(spec.Jobs, j)
	}
	if err := checkNeeds(spec.Jobs); err != nil {
		return spec, err
	}
	return spec, nil
}

// decodeStrict decodes a node rejecting unknown fields, which Node.Decode
// does not do on its own.
func decodeStrict(n *yaml.Node, v any) error {
	out, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(out))
	dec.KnownFields(true)
	return dec.Decode(v)
}

func (r rawJob) job(name string) (Job, error) {
	j := Job{Name: name, Image: r.Image, Script: r.Script, Env: r.Env, Needs: r.Needs, Agent: r.Agent, Prompt: strings.TrimSpace(r.Prompt)}
	switch {
	case j.Agent != "" && len(j.Script) > 0:
		return j, errors.New("a job has either a script or an agent, not both")
	case j.Agent == "" && len(j.Script) == 0:
		return j, errors.New("a job needs a script or an agent")
	case j.Agent != "" && j.Prompt == "":
		return j, errors.New("an agent job needs a prompt")
	case j.Agent == "" && j.Prompt != "":
		return j, errors.New("prompt is only for agent jobs")
	}
	if j.Image == "" && j.Agent == "" {
		j.Image = DefaultImage
	}
	for k := range j.Env {
		if !envNameRE.MatchString(k) || strings.HasPrefix(k, "ORBIT_") {
			return j, fmt.Errorf("invalid environment variable name %q (names starting with ORBIT_ are reserved)", k)
		}
	}
	timeout := defaultTimeout
	if j.Agent != "" {
		timeout = defaultAgentLimit
	}
	if r.Timeout != "" {
		d, err := time.ParseDuration(r.Timeout)
		if err != nil || d <= 0 {
			return j, fmt.Errorf("invalid timeout %q (use for example 20m)", r.Timeout)
		}
		timeout = min(d, maxTimeout)
	}
	j.Timeout = Duration(timeout)
	return j, nil
}

// checkNeeds rejects unknown needs and cycles.
func checkNeeds(jobs []Job) error {
	byName := map[string]Job{}
	for _, j := range jobs {
		byName[j.Name] = j
	}
	for _, j := range jobs {
		for _, n := range j.Needs {
			if _, ok := byName[n]; !ok {
				return fmt.Errorf("job %s needs %q, which does not exist", j.Name, n)
			}
		}
	}
	const (
		unvisited = iota
		visiting
		done
	)
	state := map[string]int{}
	var visit func(name string, trail []string) error
	visit = func(name string, trail []string) error {
		switch state[name] {
		case visiting:
			return fmt.Errorf("jobs wait for each other in a cycle: %s", strings.Join(append(trail, name), " → "))
		case done:
			return nil
		}
		state[name] = visiting
		for _, n := range byName[name].Needs {
			if err := visit(n, append(trail, name)); err != nil {
				return err
			}
		}
		state[name] = done
		return nil
	}
	for _, j := range jobs {
		if err := visit(j.Name, nil); err != nil {
			return err
		}
	}
	return nil
}

// Runs reports whether a push to the branch runs the pipeline.
func (s Spec) Runs(branch string) bool {
	if len(s.When.Branches) == 0 {
		return true
	}
	for _, pattern := range s.When.Branches {
		if ok, _ := path.Match(pattern, branch); ok {
			return true
		}
	}
	return false
}

// Script turns a job's script lines into one shell script that echoes each
// command before it runs it and stops at the first failure.
func Script(lines []string) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	for _, l := range lines {
		fmt.Fprintf(&b, "printf '%%s\\n' %s\n%s\n", ShellQuote("$ "+l), l)
	}
	return b.String()
}

// ShellQuote quotes s for a POSIX shell.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
