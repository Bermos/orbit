// Package gitrepo stores bare repositories on disk and reads from them with
// the git CLI. Repositories live at <root>/<app-key>/<repo>.git, so their
// location follows the stable resource tree, never the team tree.
package gitrepo

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound is returned when a ref or path does not exist.
var ErrNotFound = errors.New("not found")

// Store manages the repositories under Root.
type Store struct {
	Root string
}

// Path returns the on-disk location of a repository.
func (s Store) Path(app, repo string) string {
	return filepath.Join(s.Root, app, repo+".git")
}

// Create initialises an empty bare repository.
func (s Store) Create(ctx context.Context, app, repo, defaultBranch string) error {
	p := s.Path(app, repo)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "git", "init", "--bare", "--initial-branch="+defaultBranch, p).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git init: %v: %s", err, out)
	}
	// Allow pushes over smart HTTP.
	return s.git(ctx, app, repo, nil, "config", "http.receivepack", "true")
}

func (s Store) git(ctx context.Context, app, repo string, out *bytes.Buffer, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--git-dir", s.Path(app, repo)}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if out != nil {
		cmd.Stdout = out
	}
	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		if strings.Contains(msg, "Not a valid object name") || strings.Contains(msg, "does not exist") ||
			strings.Contains(msg, "unknown revision") || strings.Contains(msg, "bad revision") ||
			strings.Contains(msg, "Needed a single revision") || strings.Contains(msg, "not a tree object") {
			return ErrNotFound
		}
		return fmt.Errorf("git %s: %v: %s", args[0], err, msg)
	}
	return nil
}

// Branch is a branch head.
type Branch struct {
	Name   string `json:"name"`
	Commit string `json:"commit"`
}

// Branches lists the repository's branches.
func (s Store) Branches(ctx context.Context, app, repo string) ([]Branch, error) {
	var out bytes.Buffer
	if err := s.git(ctx, app, repo, &out, "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads"); err != nil {
		return nil, err
	}
	branches := []Branch{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if name, sha, ok := strings.Cut(line, " "); ok {
			branches = append(branches, Branch{Name: name, Commit: sha})
		}
	}
	return branches, nil
}

// ResolveCommit turns a ref into a full commit SHA.
func (s Store) ResolveCommit(ctx context.Context, app, repo, ref string) (string, error) {
	if strings.HasPrefix(ref, "-") {
		return "", ErrNotFound
	}
	var out bytes.Buffer
	if err := s.git(ctx, app, repo, &out, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
		return "", ErrNotFound
	}
	return strings.TrimSpace(out.String()), nil
}

// Entry is one item in a tree listing.
type Entry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"` // "blob" or "tree"
	Size int64  `json:"size"`
}

func cleanPath(p string) (string, error) {
	p = strings.Trim(p, "/")
	if p == "" {
		return "", nil
	}
	c := filepath.ToSlash(filepath.Clean(p))
	if c == ".." || strings.HasPrefix(c, "../") || strings.HasPrefix(c, "-") {
		return "", ErrNotFound
	}
	return c, nil
}

// Tree lists a directory at a commit.
func (s Store) Tree(ctx context.Context, app, repo, commit, dir string) ([]Entry, error) {
	dir, err := cleanPath(dir)
	if err != nil {
		return nil, err
	}
	spec := commit + "^{tree}"
	if dir != "" {
		spec = commit + ":" + dir
	}
	var out bytes.Buffer
	if err := s.git(ctx, app, repo, &out, "ls-tree", "-l", "-z", spec); err != nil {
		return nil, err
	}
	entries := []Entry{}
	for _, rec := range strings.Split(out.String(), "\x00") {
		if rec == "" {
			continue
		}
		meta, name, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) < 4 || (f[1] != "blob" && f[1] != "tree") {
			continue
		}
		size, _ := strconv.ParseInt(f[3], 10, 64)
		p := name
		if dir != "" {
			p = dir + "/" + name
		}
		entries = append(entries, Entry{Name: name, Path: p, Type: f[1], Size: size})
	}
	return entries, nil
}

// MaxBlobSize caps what the code view will load.
const MaxBlobSize = 1 << 20

// Blob returns a file's content at a commit. Binary or oversized files are
// reported without content.
func (s Store) Blob(ctx context.Context, app, repo, commit, path string) (content string, binary bool, err error) {
	path, err = cleanPath(path)
	if err != nil || path == "" {
		return "", false, ErrNotFound
	}
	spec := commit + ":" + path
	var sizeOut bytes.Buffer
	if err := s.git(ctx, app, repo, &sizeOut, "cat-file", "-s", spec); err != nil {
		return "", false, err
	}
	size, _ := strconv.Atoi(strings.TrimSpace(sizeOut.String()))
	if size > MaxBlobSize {
		return "", true, nil
	}
	var out bytes.Buffer
	if err := s.git(ctx, app, repo, &out, "cat-file", "blob", spec); err != nil {
		return "", false, err
	}
	b := out.Bytes()
	if bytes.IndexByte(b, 0) >= 0 {
		return "", true, nil
	}
	return string(b), false, nil
}

// Lines returns lines start..end (1-based, inclusive) of a file.
func (s Store) Lines(ctx context.Context, app, repo, commit, path string, start, end int) ([]string, error) {
	content, binary, err := s.Blob(ctx, app, repo, commit, path)
	if err != nil {
		return nil, err
	}
	if binary {
		return nil, errors.New("binary file")
	}
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Buffer(make([]byte, 0, 64*1024), MaxBlobSize)
	for n := 1; sc.Scan(); n++ {
		if n >= start && n <= end {
			lines = append(lines, sc.Text())
		}
		if n > end {
			break
		}
	}
	if len(lines) == 0 {
		return nil, ErrNotFound
	}
	return lines, nil
}

// Commit is a log entry.
type Commit struct {
	SHA     string    `json:"sha"`
	Author  string    `json:"author"`
	Email   string    `json:"email"`
	Date    time.Time `json:"date"`
	Subject string    `json:"subject"`
}

// Log returns the most recent commits reachable from commit.
func (s Store) Log(ctx context.Context, app, repo, commit string, limit int) ([]Commit, error) {
	var out bytes.Buffer
	if err := s.git(ctx, app, repo, &out, "log", "-z", "-n", strconv.Itoa(limit), "--format=%H%x1f%an%x1f%ae%x1f%aI%x1f%s", commit); err != nil {
		return nil, err
	}
	commits := []Commit{}
	for _, rec := range strings.Split(out.String(), "\x00") {
		f := strings.Split(strings.TrimSpace(rec), "\x1f")
		if len(f) != 5 {
			continue
		}
		t, _ := time.Parse(time.RFC3339, f[3])
		commits = append(commits, Commit{SHA: f[0], Author: f[1], Email: f[2], Date: t, Subject: f[4]})
	}
	return commits, nil
}

// HTTPBackend serves the git smart HTTP protocol for one request through
// git-http-backend. pathInfo is "/<app>/<repo>.git/<rest>".
func (s Store) HTTPBackend(w http.ResponseWriter, r *http.Request, pathInfo, remoteUser string) {
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		http.Error(w, "git not available", http.StatusInternalServerError)
		return
	}
	h := &cgi.Handler{
		Path: filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend"),
		Env: []string{
			"GIT_PROJECT_ROOT=" + s.Root,
			"GIT_HTTP_EXPORT_ALL=1",
			"REMOTE_USER=" + remoteUser,
		},
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = pathInfo
	// cgi.Handler derives PATH_INFO from the URL path minus Root.
	h.Root = ""
	h.ServeHTTP(w, r2)
}
