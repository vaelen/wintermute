// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package ollama

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/vaelen/wintermute/internal/llm"
)

const defaultURL = "http://localhost:11434"

func init() { llm.Register("ollama", New) }

type client struct {
	url            *url.URL
	model          string
	embeddingModel string
	keepAlive      time.Duration
	temperature    float32
	api            *api.Client
}

// New builds a client from an opts map. Recognised keys:
//
//	url             string   base URL, defaults to OLLAMA_HOST env or http://localhost:11434
//	model           string   chat model; may be overridden per-call via ChatOpts.Model
//	embedding_model string   model used by Embed; Embed errors if unset
//	keep_alive      string   time.ParseDuration value (e.g. "5m")
//	temperature     float    default temperature when ChatOpts.Temperature is zero
func New(opts map[string]any) (llm.LLM, error) {
	c := &client{}

	rawURL := defaultURL
	if v := os.Getenv("OLLAMA_HOST"); v != "" {
		rawURL = v
	}
	if raw, ok := opts["url"]; ok {
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("ollama: opts[\"url\"] must be string, got %T", raw)
		}
		if s != "" {
			rawURL = s
		}
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		if err == nil {
			err = fmt.Errorf("missing scheme or host")
		}
		return nil, fmt.Errorf("ollama: invalid url %q: %w", rawURL, err)
	}
	c.url = parsed

	if raw, ok := opts["model"]; ok {
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("ollama: opts[\"model\"] must be string, got %T", raw)
		}
		c.model = s
	}

	if raw, ok := opts["embedding_model"]; ok {
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("ollama: opts[\"embedding_model\"] must be string, got %T", raw)
		}
		c.embeddingModel = s
	}

	if raw, ok := opts["keep_alive"]; ok {
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("ollama: opts[\"keep_alive\"] must be string, got %T", raw)
		}
		if s != "" {
			d, err := time.ParseDuration(s)
			if err != nil {
				return nil, fmt.Errorf("ollama: invalid keep_alive %q: %w", s, err)
			}
			c.keepAlive = d
		}
	}

	if raw, ok := opts["temperature"]; ok {
		t, err := toFloat32(raw)
		if err != nil {
			return nil, fmt.Errorf("ollama: opts[\"temperature\"]: %w", err)
		}
		c.temperature = t
	}

	c.api = api.NewClient(c.url, http.DefaultClient)
	return c, nil
}

func (c *client) Chat(ctx context.Context, msgs []llm.Message, tools []llm.ToolDef, opts llm.ChatOpts) (llm.Response, error) {
	model := opts.Model
	if model == "" {
		model = c.model
	}
	if model == "" {
		return llm.Response{}, fmt.Errorf("ollama: no model configured (set opts[\"model\"] or ChatOpts.Model)")
	}

	req := &api.ChatRequest{
		Model:    model,
		Messages: toAPIMessages(msgs),
		Tools:    toAPITools(tools),
		Stream:   ptr(false),
	}

	options := map[string]any{}
	temp := opts.Temperature
	if temp == 0 {
		temp = c.temperature
	}
	if temp != 0 {
		options["temperature"] = temp
	}
	if opts.MaxTokens != 0 {
		options["num_predict"] = opts.MaxTokens
	}
	if len(options) > 0 {
		req.Options = options
	}

	if c.keepAlive != 0 {
		req.KeepAlive = &api.Duration{Duration: c.keepAlive}
	}

	var (
		content   string
		toolCalls []llm.ToolCall
		usageIn   int
		usageOut  int
	)
	err := c.api.Chat(ctx, req, func(resp api.ChatResponse) error {
		content += resp.Message.Content
		for _, tc := range resp.Message.ToolCalls {
			toolCalls = append(toolCalls, llm.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments.ToMap(),
			})
		}
		if resp.Done {
			usageIn = resp.PromptEvalCount
			usageOut = resp.EvalCount
		}
		return nil
	})
	if err != nil {
		return llm.Response{}, fmt.Errorf("ollama: chat: %w", err)
	}

	return llm.Response{
		Content:   content,
		ToolCalls: toolCalls,
		UsageIn:   usageIn,
		UsageOut:  usageOut,
	}, nil
}

func (c *client) Embed(ctx context.Context, text string) ([]float32, error) {
	if c.embeddingModel == "" {
		return nil, fmt.Errorf("ollama: no embedding_model configured")
	}
	req := &api.EmbedRequest{
		Model: c.embeddingModel,
		Input: text,
	}
	if c.keepAlive != 0 {
		req.KeepAlive = &api.Duration{Duration: c.keepAlive}
	}
	resp, err := c.api.Embed(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("ollama: embed: %w", err)
	}
	if len(resp.Embeddings) == 0 {
		return nil, fmt.Errorf("ollama: embed: empty result")
	}
	return resp.Embeddings[0], nil
}

func toAPIMessages(msgs []llm.Message) []api.Message {
	out := make([]api.Message, len(msgs))
	for i, m := range msgs {
		out[i] = api.Message{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolName:   m.Name,
			ToolCallID: m.ToolCallID,
		}
		if len(m.ToolCalls) > 0 {
			calls := make([]api.ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				args := api.NewToolCallFunctionArguments()
				for k, v := range tc.Arguments {
					args.Set(k, v)
				}
				calls[j] = api.ToolCall{
					ID: tc.ID,
					Function: api.ToolCallFunction{
						Name:      tc.Name,
						Arguments: args,
					},
				}
			}
			out[i].ToolCalls = calls
		}
	}
	return out
}

func toAPITools(tools []llm.ToolDef) api.Tools {
	if len(tools) == 0 {
		return nil
	}
	out := make(api.Tools, len(tools))
	for i, t := range tools {
		out[i] = api.Tool{
			Type: "function",
			Function: api.ToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  schemaToParameters(t.Schema),
			},
		}
	}
	return out
}

// schemaToParameters performs a minimal translation from a free-form JSON
// schema map into Ollama's typed ToolFunctionParameters. The engine doesn't
// exercise tool calling until M7, so this only fills the fields needed for the
// request to round-trip: the parameters block is encoded back to JSON by the
// api client, and Ollama itself does the schema interpretation.
func schemaToParameters(schema map[string]any) api.ToolFunctionParameters {
	params := api.ToolFunctionParameters{
		Type:       "object",
		Properties: api.NewToolPropertiesMap(),
	}
	if t, ok := schema["type"].(string); ok {
		params.Type = t
	}
	if req, ok := schema["required"].([]string); ok {
		params.Required = req
	} else if reqAny, ok := schema["required"].([]any); ok {
		for _, v := range reqAny {
			if s, ok := v.(string); ok {
				params.Required = append(params.Required, s)
			}
		}
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		for name, raw := range props {
			p, _ := raw.(map[string]any)
			params.Properties.Set(name, toolPropertyFromMap(p))
		}
	}
	return params
}

func toolPropertyFromMap(m map[string]any) api.ToolProperty {
	var p api.ToolProperty
	if m == nil {
		return p
	}
	switch t := m["type"].(type) {
	case string:
		p.Type = api.PropertyType{t}
	case []string:
		p.Type = api.PropertyType(t)
	case []any:
		for _, v := range t {
			if s, ok := v.(string); ok {
				p.Type = append(p.Type, s)
			}
		}
	}
	if d, ok := m["description"].(string); ok {
		p.Description = d
	}
	if e, ok := m["enum"].([]any); ok {
		p.Enum = e
	}
	return p
}

func toFloat32(v any) (float32, error) {
	switch x := v.(type) {
	case float32:
		return x, nil
	case float64:
		return float32(x), nil
	case int:
		return float32(x), nil
	case int32:
		return float32(x), nil
	case int64:
		return float32(x), nil
	default:
		return 0, fmt.Errorf("expected number, got %T", v)
	}
}

func ptr[T any](v T) *T { return &v }
