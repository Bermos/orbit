package agent

import (
	"context"
	"errors"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Anthropic calls Claude through the Messages API.
type Anthropic struct {
	client anthropic.Client
}

// NewAnthropic creates the provider. With an empty key the SDK's own lookup
// applies (ANTHROPIC_API_KEY, ANTHROPIC_BASE_URL, ...).
func NewAnthropic(apiKey string, opts ...option.RequestOption) *Anthropic {
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	return &Anthropic{client: anthropic.NewClient(opts...)}
}

func (a *Anthropic) Complete(ctx context.Context, req Request) (Response, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 16000
	}
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: int64(maxTokens),
		// If a safety classifier declines, the API retries on a suitable
		// fallback model within the same call instead of returning nothing.
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Fallbacks: anthropic.BetaFallbacksParamOfDefault(),
	}
	if req.System != "" {
		params.System = []anthropic.BetaTextBlockParam{{Text: req.System}}
	}
	if req.Effort != "" {
		params.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffort(req.Effort)}
	}
	for _, t := range req.Tools {
		props, _ := t.Schema["properties"].(map[string]any)
		required, _ := t.Schema["required"].([]string)
		params.Tools = append(params.Tools, anthropic.BetaToolUnionParam{OfTool: &anthropic.BetaToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: anthropic.BetaToolInputSchemaParam{Properties: props, Required: required},
		}})
	}
	if req.NoMoreTools && len(params.Tools) > 0 {
		params.ToolChoice = anthropic.BetaToolChoiceUnionParam{OfNone: &anthropic.BetaToolChoiceNoneParam{}}
	}
	for _, t := range req.Turns {
		params.Messages = append(params.Messages, toAnthropic(t))
	}

	msg, err := a.client.Beta.Messages.New(ctx, params)
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				return Response{}, errors.New("the model provider rejected Orbit's credentials")
			case http.StatusTooManyRequests:
				return Response{}, errors.New("the model provider is rate limiting Orbit; try again shortly")
			}
		}
		return Response{}, err
	}

	out := Response{
		StopReason: string(msg.StopReason),
		Usage:      Usage{InputTokens: msg.Usage.InputTokens, OutputTokens: msg.Usage.OutputTokens},
		// Thinking blocks must go back exactly as they came, so the next call
		// reuses the provider's own form of this turn.
		Turn: Turn{Role: "assistant", Raw: msg.ToParam()},
	}
	for _, b := range msg.Content {
		switch b.Type {
		case "text":
			out.Turn.Blocks = append(out.Turn.Blocks, Block{Type: "text", Text: b.Text})
		case "tool_use":
			out.Turn.Blocks = append(out.Turn.Blocks, Block{Type: "tool_use", ToolUseID: b.ID, Name: b.Name, Input: b.Input})
		}
	}
	return out, nil
}

func toAnthropic(t Turn) anthropic.BetaMessageParam {
	if p, ok := t.Raw.(anthropic.BetaMessageParam); ok {
		return p
	}
	var blocks []anthropic.BetaContentBlockParamUnion
	for _, b := range t.Blocks {
		switch b.Type {
		case "text":
			blocks = append(blocks, anthropic.NewBetaTextBlock(b.Text))
		case "tool_use":
			blocks = append(blocks, anthropic.NewBetaToolUseBlock(b.ToolUseID, b.Input, b.Name))
		case "tool_result":
			blocks = append(blocks, anthropic.NewBetaToolResultBlock(b.ToolUseID, b.Text, b.IsError))
		}
	}
	if t.Role == "assistant" {
		return anthropic.BetaMessageParam{Role: anthropic.BetaMessageParamRoleAssistant, Content: blocks}
	}
	return anthropic.NewBetaUserMessage(blocks...)
}
