// Package agent holds what Orbit needs to run an agent independently of the
// rest of the server: agent definitions (files), model providers and the
// tool-use loop. Orbit's own tools and the permission rules that guard them
// live in the server package, which hands them to the loop as a Toolbox.
package agent

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Definition is an agent as written in its file: Markdown with a YAML front
// matter block. The front matter configures the agent; the body is its
// instructions.
//
//	---
//	name: assistant
//	model: anthropic/claude-opus-5-5
//	tools: [search, get_issue, propose_issue]
//	---
//	You help people find their way around Orbit.
type Definition struct {
	// Name is the agent's username and how people @mention it.
	Name        string `yaml:"name" json:"name"`
	DisplayName string `yaml:"display_name" json:"display_name"`
	Description string `yaml:"description" json:"description"`
	// Model is "<provider>/<model>", for example "anthropic/claude-opus-5-5"
	// or "fake".
	Model string `yaml:"model" json:"model"`
	// Effort is passed to providers that support it (low, medium, high, ...).
	Effort string `yaml:"effort" json:"effort,omitempty"`
	// Role is the highest application role the agent may ever act with. The
	// role it actually gets is the minimum of this, the asking user's role
	// and, in a shared channel, what every member can read.
	Role string `yaml:"role" json:"role"`
	// Apps limits the agent to these applications; "*" means all.
	Apps []string `yaml:"apps" json:"apps"`
	// Tools the agent may call. Empty means every tool.
	Tools []string `yaml:"tools" json:"tools"`
	// MaxSteps caps model calls per answer.
	MaxSteps int `yaml:"max_steps" json:"max_steps"`

	Instructions string `yaml:"-" json:"-"`
	// Source is the file the definition came from.
	Source string `yaml:"-" json:"source"`
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,38}$`)

// Parse reads a definition file.
func Parse(data []byte) (Definition, error) {
	var d Definition
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	rest, ok := bytes.CutPrefix(data, []byte("---\n"))
	if !ok {
		return d, errors.New("missing front matter (the file must start with ---)")
	}
	front, body, ok := bytes.Cut(rest, []byte("\n---"))
	if !ok {
		return d, errors.New("front matter is not closed with ---")
	}
	dec := yaml.NewDecoder(bytes.NewReader(front))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil && !errors.Is(err, io.EOF) {
		return d, fmt.Errorf("front matter: %w", err)
	}
	d.Instructions = strings.TrimSpace(strings.TrimPrefix(string(body), "\n"))
	if !nameRE.MatchString(d.Name) {
		return d, fmt.Errorf("invalid name %q", d.Name)
	}
	if d.DisplayName == "" {
		d.DisplayName = d.Name
	}
	if d.Model == "" {
		return d, errors.New("model is required")
	}
	if d.Role == "" {
		d.Role = "reader"
	}
	if !slices.Contains([]string{"reader", "developer", "maintainer", "owner"}, d.Role) {
		return d, fmt.Errorf("invalid role %q", d.Role)
	}
	if len(d.Apps) == 0 {
		d.Apps = []string{"*"}
	}
	if d.MaxSteps <= 0 {
		d.MaxSteps = 8
	}
	return d, nil
}

// AllowsApp reports whether the definition's scope includes the application.
func (d Definition) AllowsApp(app string) bool {
	return slices.Contains(d.Apps, "*") || slices.Contains(d.Apps, app)
}

// AllowsTool reports whether the agent may call the tool.
func (d Definition) AllowsTool(name string) bool {
	return len(d.Tools) == 0 || slices.Contains(d.Tools, name)
}

// ProviderAndModel splits Model into its provider and model parts.
func (d Definition) ProviderAndModel() (provider, model string) {
	p, m, _ := strings.Cut(d.Model, "/")
	return p, m
}

//go:embed catalog/*.md
var builtin embed.FS

// LoadCatalog returns the instance catalog: the built-in agents, overridden
// and extended by every *.md file in dir (if dir is not empty). A broken file
// is reported, not fatal, so one typo does not take every agent down.
func LoadCatalog(dir string) (defs []Definition, problems []error) {
	byName := map[string]Definition{}
	load := func(fsys fs.FS, pattern, label string) {
		names, _ := fs.Glob(fsys, pattern)
		sort.Strings(names)
		for _, n := range names {
			data, err := fs.ReadFile(fsys, n)
			if err != nil {
				problems = append(problems, fmt.Errorf("%s: %w", n, err))
				continue
			}
			d, err := Parse(data)
			if err != nil {
				problems = append(problems, fmt.Errorf("%s/%s: %w", label, filepath.Base(n), err))
				continue
			}
			d.Source = label + "/" + filepath.Base(n)
			byName[d.Name] = d
		}
	}
	load(builtin, "catalog/*.md", "builtin")
	if dir != "" {
		if _, err := os.Stat(dir); err == nil {
			load(os.DirFS(dir), "*.md", dir)
		} else {
			problems = append(problems, fmt.Errorf("agents directory: %w", err))
		}
	}
	for _, d := range byName {
		defs = append(defs, d)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs, problems
}
