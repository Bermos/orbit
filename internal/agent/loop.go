package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Toolbox executes tools for one run. Call returns the result shown to the
// model; an error is shown to the model as a failed tool call, so it can
// recover (for example after a permission refusal).
type Toolbox interface {
	Specs() []ToolSpec
	Call(ctx context.Context, name string, input json.RawMessage) (string, error)
}

// Step records one tool call. Steps are stored on the run and shown under the
// reply, so people can see exactly what the agent looked at.
type Step struct {
	Tool       string          `json:"tool"`
	Input      json.RawMessage `json:"input"`
	Error      string          `json:"error,omitempty"`
	DurationMS int64           `json:"duration_ms"`
}

// Result is the outcome of a run.
type Result struct {
	Text  string
	Steps []Step
	Usage Usage
}

// ErrRefused is returned when the model declined to answer.
var ErrRefused = errors.New("the model declined to answer this request")

// Run drives the tool-use loop: call the model, run the tools it asks for,
// feed the results back, until it answers or maxSteps model calls are spent.
// The last call asks for an answer without further tools. onStep is called
// after every tool call.
func Run(ctx context.Context, p Provider, req Request, tools Toolbox, maxSteps int, onStep func(Step)) (Result, error) {
	var res Result
	req.Tools = tools.Specs()
	for i := 0; i < maxSteps; i++ {
		req.NoMoreTools = i == maxSteps-1
		resp, err := p.Complete(ctx, req)
		if err != nil {
			return res, err
		}
		res.Usage.InputTokens += resp.Usage.InputTokens
		res.Usage.OutputTokens += resp.Usage.OutputTokens
		if resp.StopReason == "refusal" {
			return res, ErrRefused
		}
		req.Turns = append(req.Turns, resp.Turn)

		var results []Block
		for _, b := range resp.Turn.Blocks {
			if b.Type != "tool_use" {
				continue
			}
			start := time.Now()
			out, err := tools.Call(ctx, b.Name, b.Input)
			step := Step{Tool: b.Name, Input: b.Input, DurationMS: time.Since(start).Milliseconds()}
			result := Block{Type: "tool_result", ToolUseID: b.ToolUseID, Text: out}
			if err != nil {
				step.Error = err.Error()
				result.Text, result.IsError = err.Error(), true
			}
			res.Steps = append(res.Steps, step)
			if onStep != nil {
				onStep(step)
			}
			results = append(results, result)
		}
		if len(results) == 0 {
			res.Text = resp.Turn.Text()
			if res.Text == "" && resp.StopReason == "max_tokens" {
				return res, errors.New("the answer did not fit the output limit")
			}
			return res, nil
		}
		req.Turns = append(req.Turns, Turn{Role: "user", Blocks: results})
	}
	return res, fmt.Errorf("no answer after %d model calls", maxSteps)
}
