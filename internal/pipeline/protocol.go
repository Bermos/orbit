package pipeline

import "encoding/json"

// The runner protocol. Runners pull work from the server over HTTPS:
//
//	POST /api/runner/v1/jobs/next             runner token; long-polls, returns an Assignment or 204
//	POST /api/runner/v1/jobs/{id}/logs        job token; text/plain log output (empty = heartbeat), returns LogAck
//	POST /api/runner/v1/jobs/{id}/agent       job token; AgentPoll in, AgentStep out (agent jobs)
//	PUT  /api/runner/v1/jobs/{id}/patch       job token; the binary git diff an agent job produced
//	POST /api/runner/v1/jobs/{id}/finish      job token; Finish
//
// The job token is only valid while the job runs. It also lets the runner
// clone that one repository over git: user "orbit-job", password the token.

// JobUser is the git username for job tokens.
const JobUser = "orbit-job"

// Assignment is a job handed to a runner.
type Assignment struct {
	JobID      string `json:"job_id"`
	Token      string `json:"token"`
	PipelineID string `json:"pipeline_id"`
	Number     int    `json:"pipeline_number"`
	App        string `json:"app"`
	Repo       string `json:"repo"`
	// ClonePath is the repository's git URL path on the server, for example
	// /git/ledger/api.git. Runners join it with the URL they use for Orbit.
	ClonePath string `json:"clone_path"`
	Commit    string `json:"commit"`
	Ref       string `json:"ref"`
	Kind      string `json:"kind"` // "script" or "agent"
	Job       Job    `json:"job"`
}

// LogAck answers a log upload. Cancel asks the runner to stop the job.
type LogAck struct {
	Cancel bool `json:"cancel"`
}

// Finish is a job's outcome as the runner saw it.
type Finish struct {
	Status   string `json:"status"` // success, failure or error
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Agent jobs: the model and Orbit's own tools stay on the server, which runs
// the agent loop. Tools that touch the checkout (SandboxTools) are sent to
// the runner, which runs them in the job's workspace and sends the result
// with its next poll.

// ToolCall asks the runner to run a sandbox tool.
type ToolCall struct {
	ID    string          `json:"id"`
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input"`
}

// ToolResult is the outcome of a ToolCall.
type ToolResult struct {
	ID     string `json:"id"`
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

// AgentPoll carries the previous call's result, if any.
type AgentPoll struct {
	Result *ToolResult `json:"result,omitempty"`
}

// AgentStep is the next thing to do: run a tool, keep waiting (neither set),
// or stop because the agent has finished.
type AgentStep struct {
	Call *ToolCall `json:"call,omitempty"`
	Done bool      `json:"done,omitempty"`
	// Failed is set when the agent stopped with an error.
	Failed string `json:"failed,omitempty"`
}

// SandboxTool describes a tool runners implement on the job's checkout.
type SandboxTool struct {
	Name        string
	Description string
	Schema      map[string]any
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }

func object(required []string, props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required}
}

// SandboxTools are the tools of an agent job that run in its workspace.
// Paths are relative to the root of the checkout.
var SandboxTools = []SandboxTool{
	{Name: "list_files", Description: "List files and directories in the checkout, up to three levels deep (without .git).",
		Schema: object([]string{}, map[string]any{"path": str("Directory, relative to the repository root. Defaults to the root.")})},
	{Name: "read_file", Description: "Read a file from the checkout with line numbers, up to 400 lines at a time.",
		Schema: object([]string{"path"}, map[string]any{
			"path":       str("File path, relative to the repository root."),
			"start_line": num("First line to read (default 1)."),
			"end_line":   num("Last line to read."),
		})},
	{Name: "write_file", Description: "Create or overwrite a file in the checkout with the given content.",
		Schema: object([]string{"path", "content"}, map[string]any{
			"path":    str("File path, relative to the repository root."),
			"content": str("The complete new content of the file."),
		})},
	{Name: "edit_file", Description: "Replace one exact occurrence of a piece of text in a file. old_text must appear exactly once; include enough surrounding lines to make it unique.",
		Schema: object([]string{"path", "old_text", "new_text"}, map[string]any{
			"path":     str("File path, relative to the repository root."),
			"old_text": str("The exact text to replace."),
			"new_text": str("The replacement text."),
		})},
	{Name: "run", Description: "Run a shell command in the repository root, for example to build or run tests. Returns the exit code and the end of the output. Commands time out after 10 minutes.",
		Schema: object([]string{"command"}, map[string]any{"command": str("The command, run with sh -c.")})},
}

// IsSandboxTool reports whether a tool runs on the runner.
func IsSandboxTool(name string) bool {
	for _, t := range SandboxTools {
		if t.Name == name {
			return true
		}
	}
	return false
}
