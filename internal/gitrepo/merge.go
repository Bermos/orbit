package gitrepo

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Merge requests need a diff between two commits, the commits in between, a
// conflict check and the merge itself. All of it works on the bare
// repository: merges are computed with git merge-tree and committed with
// commit-tree, so no working tree is ever checked out.

// ErrNoMergeBase is returned when two commits share no history.
var ErrNoMergeBase = errors.New("the branches have no common history")

// ErrRefMoved is returned when a branch is not where the caller expected it,
// because someone pushed in between.
var ErrRefMoved = errors.New("the branch moved")

// exitCode runs git and returns its exit code; it only fails when git could
// not run at all.
func (s Store) exitCode(ctx context.Context, app, repo string, out *bytes.Buffer, args ...string) (int, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--git-dir", s.Path(app, repo)}, args...)...)
	var stderr bytes.Buffer
	if out != nil {
		cmd.Stdout = out
	}
	cmd.Stderr = &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, fmt.Errorf("git %s: %v: %s", args[0], err, stderr.String())
	}
	return 0, nil
}

// MergeBase returns the best common ancestor of two commits.
func (s Store) MergeBase(ctx context.Context, app, repo, a, b string) (string, error) {
	var out bytes.Buffer
	code, err := s.exitCode(ctx, app, repo, &out, "merge-base", a, b)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", ErrNoMergeBase
	}
	return strings.TrimSpace(out.String()), nil
}

// IsAncestor reports whether commit a is reachable from commit b.
func (s Store) IsAncestor(ctx context.Context, app, repo, a, b string) (bool, error) {
	code, err := s.exitCode(ctx, app, repo, nil, "merge-base", "--is-ancestor", a, b)
	if err != nil {
		return false, err
	}
	return code == 0, nil
}

// Range returns the commits reachable from head but not from base, newest
// first.
func (s Store) Range(ctx context.Context, app, repo, base, head string, limit int) ([]Commit, error) {
	return s.Log(ctx, app, repo, base+".."+head, limit)
}

// MergeTree merges theirs into ours in memory. It returns the resulting tree,
// or the paths that conflict.
func (s Store) MergeTree(ctx context.Context, app, repo, ours, theirs string) (tree string, conflicts []string, err error) {
	var out bytes.Buffer
	code, err := s.exitCode(ctx, app, repo, &out, "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", ours, theirs)
	if err != nil {
		return "", nil, err
	}
	fields := strings.Split(strings.TrimRight(out.String(), "\x00"), "\x00")
	switch code {
	case 0:
		return strings.TrimSpace(fields[0]), nil, nil
	case 1:
		seen := map[string]bool{}
		for _, f := range fields[1:] {
			if f != "" && !seen[f] {
				seen[f] = true
				conflicts = append(conflicts, f)
			}
		}
		return "", conflicts, nil
	}
	return "", nil, fmt.Errorf("git merge-tree exited with %d", code)
}

// CommitTree creates a commit for a tree with the given parents.
func (s Store) CommitTree(ctx context.Context, app, repo, tree, message string, author, committer Person, parents ...string) (string, error) {
	env := []string{
		"GIT_AUTHOR_NAME=" + author.Name, "GIT_AUTHOR_EMAIL=" + author.Email,
		"GIT_COMMITTER_NAME=" + committer.Name, "GIT_COMMITTER_EMAIL=" + committer.Email,
	}
	args := []string{"commit-tree", tree}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	var out bytes.Buffer
	if err := s.gitEnv(ctx, app, repo, env, strings.NewReader(message), &out, args...); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

// MoveBranch points an existing branch at commit, only if it still points
// at old.
func (s Store) MoveBranch(ctx context.Context, app, repo, branch, commit, old string) error {
	code, err := s.exitCode(ctx, app, repo, nil, "update-ref", "refs/heads/"+branch, commit, old)
	if err != nil {
		return err
	}
	if code != 0 {
		return ErrRefMoved
	}
	return nil
}

// DeleteBranch removes a branch, only if it still points at old.
func (s Store) DeleteBranch(ctx context.Context, app, repo, branch, old string) error {
	code, err := s.exitCode(ctx, app, repo, nil, "update-ref", "-d", "refs/heads/"+branch, old)
	if err != nil {
		return err
	}
	if code != 0 {
		return ErrRefMoved
	}
	return nil
}

// KeepRef points a hidden ref (outside refs/heads and refs/tags) at commit,
// so the commit survives garbage collection after its branch is deleted.
func (s Store) KeepRef(ctx context.Context, app, repo, ref, commit string) error {
	if !strings.HasPrefix(ref, "refs/orbit/") {
		return fmt.Errorf("refusing to set %s", ref)
	}
	return s.git(ctx, app, repo, nil, "update-ref", ref, commit)
}

// Diff limits keep huge changes from overwhelming the review page.
const (
	maxDiffFileLines  = 3000
	maxDiffTotalLines = 20000
)

// DiffLine is one line of a hunk. Old and New are line numbers on each side,
// zero where the line does not exist on that side.
type DiffLine struct {
	Kind string `json:"kind"` // "context", "add" or "del"
	Old  int    `json:"old,omitempty"`
	New  int    `json:"new,omitempty"`
	Text string `json:"text"`
}

// Hunk is a contiguous block of changes.
type Hunk struct {
	Header   string     `json:"header"`
	OldStart int        `json:"old_start"`
	NewStart int        `json:"new_start"`
	Lines    []DiffLine `json:"lines"`
}

// DiffFile is one file of a diff.
type DiffFile struct {
	OldPath   string `json:"old_path"`
	NewPath   string `json:"new_path"`
	Status    string `json:"status"` // "added", "deleted", "modified" or "renamed"
	Binary    bool   `json:"binary"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	// TooLarge files are listed without their hunks.
	TooLarge bool   `json:"too_large,omitempty"`
	Hunks    []Hunk `json:"hunks"`
}

// Diff compares two commits file by file, detecting renames.
func (s Store) Diff(ctx context.Context, app, repo, base, head string) ([]DiffFile, error) {
	var out bytes.Buffer
	if err := s.git(ctx, app, repo, &out, "-c", "core.quotePath=true", "diff", "--no-color", "--no-ext-diff", "-M", "-U3",
		"--src-prefix=a/", "--dst-prefix=b/", base, head, "--"); err != nil {
		return nil, err
	}
	return ParseDiff(out.Bytes()), nil
}

// ParseDiff reads unified diff output of git diff.
func ParseDiff(b []byte) []DiffFile {
	files := []DiffFile{}
	var f *DiffFile
	var h *Hunk
	var oldN, newN, total int
	flush := func() {
		if f == nil {
			return
		}
		if f.Status == "" {
			f.Status = "modified"
			if f.OldPath != f.NewPath {
				f.Status = "renamed"
			}
		}
		if f.TooLarge {
			f.Hunks = []Hunk{}
		}
		files = append(files, *f)
		f, h = nil, nil
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			f = &DiffFile{Hunks: []Hunk{}}
			f.OldPath, f.NewPath = splitDiffGitLine(strings.TrimPrefix(line, "diff --git "))
			continue
		}
		if f == nil {
			continue
		}
		if h == nil {
			switch {
			case strings.HasPrefix(line, "new file mode"):
				f.Status = "added"
			case strings.HasPrefix(line, "deleted file mode"):
				f.Status = "deleted"
			case strings.HasPrefix(line, "rename from "):
				f.OldPath = unquotePath(strings.TrimPrefix(line, "rename from "))
			case strings.HasPrefix(line, "rename to "):
				f.NewPath = unquotePath(strings.TrimPrefix(line, "rename to "))
			case strings.HasPrefix(line, "Binary files "):
				f.Binary = true
			case strings.HasPrefix(line, "--- "):
				if p := strings.TrimPrefix(line, "--- "); p != "/dev/null" {
					f.OldPath = strings.TrimPrefix(unquotePath(p), "a/")
				}
				continue
			case strings.HasPrefix(line, "+++ "):
				if p := strings.TrimPrefix(line, "+++ "); p != "/dev/null" {
					f.NewPath = strings.TrimPrefix(unquotePath(p), "b/")
				}
				continue
			}
		}
		if strings.HasPrefix(line, "@@ ") {
			spec, _, _ := strings.Cut(strings.TrimPrefix(line, "@@ "), " @@")
			o, n, _ := strings.Cut(spec, " ")
			hk := Hunk{Header: line, Lines: []DiffLine{},
				OldStart: rangeStart(strings.TrimPrefix(o, "-")), NewStart: rangeStart(strings.TrimPrefix(n, "+"))}
			oldN, newN = hk.OldStart, hk.NewStart
			f.Hunks = append(f.Hunks, hk)
			h = &f.Hunks[len(f.Hunks)-1]
			continue
		}
		if h == nil {
			continue
		}
		var dl DiffLine
		switch {
		case strings.HasPrefix(line, "+"):
			dl = DiffLine{Kind: "add", New: newN, Text: line[1:]}
			newN++
			f.Additions++
		case strings.HasPrefix(line, "-"):
			dl = DiffLine{Kind: "del", Old: oldN, Text: line[1:]}
			oldN++
			f.Deletions++
		case strings.HasPrefix(line, " ") || line == "":
			dl = DiffLine{Kind: "context", Old: oldN, New: newN, Text: strings.TrimPrefix(line, " ")}
			oldN++
			newN++
		default: // "\ No newline at end of file"
			continue
		}
		total++
		if lineCount(f) > maxDiffFileLines || total > maxDiffTotalLines {
			f.TooLarge = true
			continue
		}
		h.Lines = append(h.Lines, dl)
	}
	flush()
	return files
}

func lineCount(f *DiffFile) int { return f.Additions + f.Deletions }

func rangeStart(s string) int {
	a, _, _ := strings.Cut(s, ",")
	n, _ := strconv.Atoi(a)
	return n
}

// splitDiffGitLine splits "a/old b/new". Paths with spaces are ambiguous
// there; the ---/+++ and rename lines that follow correct them.
func splitDiffGitLine(rest string) (string, string) {
	if strings.HasPrefix(rest, `"`) {
		if end := closingQuote(rest); end > 0 {
			a := unquotePath(rest[:end+1])
			b := unquotePath(strings.TrimSpace(rest[end+1:]))
			return strings.TrimPrefix(a, "a/"), strings.TrimPrefix(b, "b/")
		}
	}
	// Without a rename both halves are equal: "a/x b/x".
	if len(rest)%2 == 1 {
		half := (len(rest) - 1) / 2
		if a, b := rest[:half], rest[half+1:]; strings.TrimPrefix(a, "a/") == strings.TrimPrefix(b, "b/") {
			return strings.TrimPrefix(a, "a/"), strings.TrimPrefix(b, "b/")
		}
	}
	a, b, _ := strings.Cut(rest, " b/")
	return strings.TrimPrefix(a, "a/"), b
}

func closingQuote(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

// unquotePath undoes git's C-style quoting of unusual paths.
func unquotePath(p string) string {
	if len(p) < 2 || p[0] != '"' {
		return p
	}
	if u, err := strconv.Unquote(p); err == nil {
		return u
	}
	return p
}

// LineMapper follows lines of a file from one commit to another through
// the hunks of the diff between them: unchanged lines move by the lines
// added or removed above them, changed and removed lines do not map.
func LineMapper(hunks []Hunk) func(line int) (int, bool) {
	return func(n int) (int, bool) {
		offset := 0
		for _, h := range hunks {
			oldLen, newLen := 0, 0
			for _, l := range h.Lines {
				if l.Kind != "add" {
					oldLen++
				}
				if l.Kind != "del" {
					newLen++
				}
			}
			oldStart := h.OldStart
			if oldLen == 0 {
				oldStart++ // "-12,0" adds after line 12
			}
			if n < oldStart {
				break
			}
			if n < oldStart+oldLen {
				for _, l := range h.Lines {
					if l.Old == n {
						return l.New, l.Kind == "context"
					}
				}
				return 0, false
			}
			newStart := h.NewStart
			if newLen == 0 {
				newStart++
			}
			offset = (newStart + newLen) - (oldStart + oldLen)
		}
		return n + offset, true
	}
}

// DiffPath compares one file between two commits.
func (s Store) DiffPath(ctx context.Context, app, repo, from, to, path string) (DiffFile, bool, error) {
	path, err := cleanPath(path)
	if err != nil || path == "" {
		return DiffFile{}, false, ErrNotFound
	}
	var out bytes.Buffer
	if err := s.git(ctx, app, repo, &out, "diff", "--no-color", "--no-ext-diff", "-U0", "--src-prefix=a/", "--dst-prefix=b/", from, to, "--", path); err != nil {
		return DiffFile{}, false, err
	}
	files := ParseDiff(out.Bytes())
	if len(files) == 0 {
		return DiffFile{}, false, nil
	}
	return files[0], true, nil
}
