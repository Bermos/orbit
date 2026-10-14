package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/Bermos/orbit/internal/pipeline"
)

// The sandbox tools of agent jobs (pipeline.SandboxTools), built on Exec so
// they work the same in a local directory and in a pod.

const (
	maxReadLines = 400
	maxFileSize  = 1 << 20
	maxRunOutput = 20000
	runTimeout   = 10 * time.Minute
)

func runTool(ctx context.Context, ws Workspace, logs io.Writer, call pipeline.ToolCall) (string, error) {
	var in struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
		Content   string `json:"content"`
		OldText   string `json:"old_text"`
		NewText   string `json:"new_text"`
		Command   string `json:"command"`
	}
	if len(call.Input) > 0 {
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return "", fmt.Errorf("invalid input: %w", err)
		}
	}
	switch call.Tool {
	case "list_files":
		p, err := relPath(in.Path, true)
		if err != nil {
			return "", err
		}
		var out bytes.Buffer
		script := `find "$1" -maxdepth 3 \( -name .git -prune \) -o -print | sed 's|^\./||' | sort | head -n 1000`
		if _, err := runChecked(ctx, ws, []string{"sh", "-c", script, "sh", p}, nil, &out); err != nil {
			return "", err
		}
		return out.String(), nil
	case "read_file":
		p, err := relPath(in.Path, false)
		if err != nil {
			return "", err
		}
		content, err := readFile(ctx, ws, p)
		if err != nil {
			return "", err
		}
		lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
		start := max(in.StartLine, 1)
		end := in.EndLine
		if end <= 0 || end > len(lines) {
			end = len(lines)
		}
		end = min(end, start+maxReadLines-1)
		if start > len(lines) {
			return "", fmt.Errorf("the file has %d lines", len(lines))
		}
		var b strings.Builder
		for i := start; i <= end; i++ {
			fmt.Fprintf(&b, "%d\t%s\n", i, lines[i-1])
		}
		if end < len(lines) {
			fmt.Fprintf(&b, "[%d more lines; read on with start_line=%d]\n", len(lines)-end, end+1)
		}
		return b.String(), nil
	case "write_file":
		p, err := relPath(in.Path, false)
		if err != nil {
			return "", err
		}
		if err := writeFile(ctx, ws, p, in.Content); err != nil {
			return "", err
		}
		return fmt.Sprintf("wrote %d bytes to %s", len(in.Content), p), nil
	case "edit_file":
		p, err := relPath(in.Path, false)
		if err != nil {
			return "", err
		}
		if in.OldText == "" {
			return "", errors.New("old_text is required")
		}
		content, err := readFile(ctx, ws, p)
		if err != nil {
			return "", err
		}
		switch n := strings.Count(content, in.OldText); n {
		case 0:
			return "", errors.New("old_text does not appear in the file; read it again and copy the text exactly")
		case 1:
		default:
			return "", fmt.Errorf("old_text appears %d times; include more surrounding lines so it is unique", n)
		}
		if err := writeFile(ctx, ws, p, strings.Replace(content, in.OldText, in.NewText, 1)); err != nil {
			return "", err
		}
		return "edited " + p, nil
	case "run":
		if strings.TrimSpace(in.Command) == "" {
			return "", errors.New("command is required")
		}
		fmt.Fprintf(logs, "$ %s\n", in.Command)
		rctx, cancel := context.WithTimeout(ctx, runTimeout)
		defer cancel()
		tail := &tailBuffer{max: maxRunOutput}
		w := io.MultiWriter(tail, logs)
		code, err := ws.Exec(rctx, Command{Argv: []string{"sh", "-c", in.Command}, Stdout: w, Stderr: w})
		if rctx.Err() == context.DeadlineExceeded {
			return tail.String(), fmt.Errorf("the command did not finish within %s", runTimeout)
		}
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("exit code %d\n%s", code, tail.String()), nil
	}
	return "", fmt.Errorf("unknown tool %q", call.Tool)
}

// relPath cleans a path relative to the checkout and refuses paths that
// leave it.
func relPath(p string, dir bool) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		if dir {
			return ".", nil
		}
		return "", errors.New("path is required")
	}
	c := path.Clean(strings.TrimPrefix(p, "./"))
	if path.IsAbs(c) || c == ".." || strings.HasPrefix(c, "../") {
		return "", errors.New("paths are relative to the repository root and stay inside it")
	}
	if c == ".git" || strings.HasPrefix(c, ".git/") {
		return "", errors.New("the .git directory is off limits")
	}
	return c, nil
}

// runChecked runs a command and fails on a non-zero exit, with its error output.
func runChecked(ctx context.Context, ws Workspace, argv []string, stdin io.Reader, stdout io.Writer) (int, error) {
	var stderr bytes.Buffer
	code, err := ws.Exec(ctx, Command{Argv: argv, Stdin: stdin, Stdout: stdout, Stderr: &stderr})
	if err != nil {
		return code, err
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = fmt.Sprintf("exit code %d", code)
		}
		return code, errors.New(msg)
	}
	return 0, nil
}

func readFile(ctx context.Context, ws Workspace, p string) (string, error) {
	var out bytes.Buffer
	if _, err := runChecked(ctx, ws, []string{"sh", "-c", `test -f "$1" || { echo "no such file: $1" >&2; exit 1; }; cat -- "$1"`, "sh", p}, nil,
		&limitedWriter{w: &out, n: maxFileSize}); err != nil {
		return "", err
	}
	if bytes.IndexByte(out.Bytes(), 0) >= 0 {
		return "", errors.New("binary file")
	}
	return out.String(), nil
}

func writeFile(ctx context.Context, ws Workspace, p, content string) error {
	_, err := runChecked(ctx, ws, []string{"sh", "-c", `mkdir -p "$(dirname -- "$1")" && cat > "$1"`, "sh", p}, strings.NewReader(content), io.Discard)
	return err
}

// tailBuffer keeps the last max bytes written.
type tailBuffer struct {
	max       int
	buf       []byte
	truncated bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
		t.truncated = true
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	if t.truncated {
		return "[earlier output cut]\n" + string(t.buf)
	}
	return string(t.buf)
}
