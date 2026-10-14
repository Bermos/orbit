package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
)

// Local runs jobs as processes in a temporary directory on the runner's own
// machine. It ignores images and isolates nothing, so it is for development
// and trusted setups only; use the Kubernetes executor for anything shared.
type Local struct {
	// Dir holds the job directories; the system's temporary directory if
	// empty.
	Dir string
}

func (Local) Name() string { return "local" }

func (l Local) Prepare(ctx context.Context, spec Spec) (Workspace, error) {
	dir, err := os.MkdirTemp(l.Dir, "orbit-job-")
	if err != nil {
		return nil, err
	}
	ws := &localWorkspace{dir: dir, env: os.Environ()}
	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ws.env = append(ws.env, k+"="+spec.Env[k])
	}
	fmt.Fprintf(spec.Log, "Checking out %s at %s\n", spec.Assignment.App+"/"+spec.Assignment.Repo, shortSHA(spec.Assignment.Commit))
	clone := exec.CommandContext(ctx, "git", "-c", "http.extraHeader="+spec.GitAuth, "clone", "--quiet", "--no-checkout", spec.CloneURL, dir)
	clone.Stdout, clone.Stderr = spec.Log, spec.Log
	clone.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if err := clone.Run(); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("git clone: %w", err)
	}
	checkout := exec.CommandContext(ctx, "git", "-C", dir, "checkout", "--quiet", "--detach", spec.Assignment.Commit)
	checkout.Stdout, checkout.Stderr = spec.Log, spec.Log
	if err := checkout.Run(); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("git checkout: %w", err)
	}
	return ws, nil
}

type localWorkspace struct {
	dir string
	env []string
}

func (w *localWorkspace) Exec(ctx context.Context, c Command) (int, error) {
	if len(c.Argv) == 0 {
		return 0, errors.New("empty command")
	}
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Dir, cmd.Env = w.dir, w.env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = c.Stdin, c.Stdout, c.Stderr
	killGroup(cmd)
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && ctx.Err() == nil {
		return exit.ExitCode(), nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		return -1, err
	}
	return 0, nil
}

func (w *localWorkspace) Close(context.Context) error {
	return os.RemoveAll(w.dir)
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
