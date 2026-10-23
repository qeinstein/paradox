// Package provider contains model-agnostic inference adapters. The OpenAI-
// compatible adapter is used for Meta Model API and OpenRouter; both document
// an OpenAI-compatible chat-completions surface. Scientific logic lives in
// domain packages and never imports a provider.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/qeinstein/paradox/harness"
)

const (
	MetaBaseURL       = "https://api.meta.ai/v1"
	OpenRouterBaseURL = "https://openrouter.ai/api/v1"
)

type OpenAIConfig struct {
	Name                  string
	BaseURL               string
	APIKey                string
	Model                 string
	HTTPClient            *http.Client
	Headers               map[string]string
	InputPricePerMillion  float64
	OutputPricePerMillion float64
	Capabilities          harness.Capabilities
	ReasoningParam        string // "reasoning_effort" (default) or "reasoning"
}

type OpenAIProvider struct {
	cfg    OpenAIConfig
	client *http.Client
}

func NewOpenAICompatible(cfg OpenAIConfig) *OpenAIProvider {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 180 * time.Second}
	}
	if cfg.ReasoningParam == "" {
		cfg.ReasoningParam = "reasoning_effort"
	}
	return &OpenAIProvider{cfg: cfg, client: cfg.HTTPClient}
}

func NewMeta(apiKey, model string) *OpenAIProvider {
	if model == "" {
		model = "muse-spark-1.3"
	}
	return NewOpenAICompatible(OpenAIConfig{
		Name: "meta", BaseURL: MetaBaseURL, APIKey: apiKey, Model: model,
		Capabilities: harness.Capabilities{
			ToolCalls: true, StructuredOutput: true, Reasoning: true,
			Streaming: true, MaxContextTokens: 1048576,
		},
	})
}

func NewOpenRouter(apiKey, model string) *OpenAIProvider {
	return NewOpenAICompatible(OpenAIConfig{
		Name: "openrouter", BaseURL: OpenRouterBaseURL, APIKey: apiKey, Model: model,
		Capabilities: harness.Capabilities{
			ToolCalls: true, StructuredOutput: true, Reasoning: true, Streaming: true,
		},
		ReasoningParam: "reasoning",
		Headers: map[string]string{
			"HTTP-Referer": "https://github.com/qeinstein/paradox",
			"X-Title":      "PARADOX",
		},
	})
}

func (p *OpenAIProvider) Name() string                       { return p.cfg.Name }
func (p *OpenAIProvider) Model() string                      { return p.cfg.Model }
func (p *OpenAIProvider) Capabilities() harness.Capabilities { return p.cfg.Capabilities }

func (p *OpenAIProvider) Complete(ctx context.Context, req *harness.ModelRequest) (*harness.ModelResponse, error) {
	if strings.TrimSpace(p.cfg.APIKey) == "" {
		return nil, errors.New("provider API key is empty")
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, errors.New("model id is empty")
	}
	body, err := p.buildRequest(req)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(p.cfg.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	for key, value := range p.cfg.Headers {
		httpReq.Header.Set(key, value)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s request failed: %w", p.cfg.Name, err)
	}
	defer resp.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s HTTP %d: %s", p.cfg.Name, resp.StatusCode, truncateString(string(responseBody), 800))
	}
	return p.parseResponse(responseBody)
}

type chatRequest struct {
	Model               string           `json:"model"`
	Messages            []chatMessage    `json:"messages"`
	Tools               []chatTool       `json:"tools,omitempty"`
	ToolChoice          string           `json:"tool_choice,omitempty"`
	MaxCompletionTokens int              `json:"max_completion_tokens,omitempty"`
	Temperature         *float64         `json:"temperature,omitempty"`
	ReasoningEffort     string           `json:"reasoning_effort,omitempty"`
	Reasoning           *reasoningConfig `json:"reasoning,omitempty"`
	ResponseFormat      *responseFormat  `json:"response_format,omitempty"`
}

type reasoningConfig struct {
	Effort string `json:"effort"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatFunctionCall `json:"function"`
}

type chatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict,omitempty"`
}

type responseFormat struct {
	Type       string          `json:"type"`
	JSONSchema *jsonSchemaSpec `json:"json_schema,omitempty"`
}

type jsonSchemaSpec struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict,omitempty"`
	Schema json.RawMessage `json:"schema"`
}

func (p *OpenAIProvider) buildRequest(req *harness.ModelRequest) ([]byte, error) {
	messages := []chatMessage{}
	if strings.TrimSpace(req.System) != "" {
		messages = append(messages, chatMessage{Role: "system", Content: req.System})
	}
	for _, message := range req.Messages {
		chat := chatMessage{
			Role: string(message.Role), Content: message.Content,
			ToolCallID: message.ToolCallID,
		}
		for _, call := range message.ToolCalls {
			chat.ToolCalls = append(chat.ToolCalls, chatToolCall{
				ID: call.ID, Type: "function",
				Function: chatFunctionCall{Name: call.Name, Arguments: call.Arguments},
			})
		}
		messages = append(messages, chat)
	}
	tools := make([]chatTool, 0, len(req.Tools))
	for _, tool := range req.Tools {
		parameters := tool.Parameters
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, chatTool{
			Type: "function",
			Function: chatFunction{
				Name: tool.Name, Description: tool.Description, Parameters: parameters,
			},
		})
	}
	request := chatRequest{
		Model: req.Model, Messages: messages,
		MaxCompletionTokens: req.MaxTokens, Temperature: req.Temperature,
	}
	if len(tools) > 0 {
		request.Tools = tools
		request.ToolChoice = "auto"
	}
	if req.ReasoningEffort != "" {
		if p.cfg.ReasoningParam == "reasoning" {
			request.Reasoning = &reasoningConfig{Effort: req.ReasoningEffort}
		} else {
			request.ReasoningEffort = req.ReasoningEffort
		}
	}
	if req.ResponseFormat != nil {
		format := &responseFormat{Type: req.ResponseFormat.Type}
		if req.ResponseFormat.Type == "json_schema" {
			format.JSONSchema = &jsonSchemaSpec{
				Name: req.ResponseFormat.Name, Strict: req.ResponseFormat.Strict,
				Schema: req.ResponseFormat.Schema,
			}
		}
		request.ResponseFormat = format
	}
	return json.Marshal(request)
}

type chatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content   *string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		TotalTokens         int `json:"total_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
		Cost *float64 `json:"cost"`
	} `json:"usage"`
}

func (p *OpenAIProvider) parseResponse(body []byte) (*harness.ModelResponse, error) {
	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("could not decode %s response: %w", p.cfg.Name, err)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("%s returned no choices", p.cfg.Name)
	}
	choice := parsed.Choices[0]
	response := &harness.ModelResponse{
		Model: parsed.Model, FinishReason: choice.FinishReason, Raw: body,
	}
	if choice.Message.Content != nil {
		response.Content = *choice.Message.Content
	}
	for _, call := range choice.Message.ToolCalls {
		response.ToolCalls = append(response.ToolCalls, harness.ToolCall{
			ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments,
		})
	}
	usage := harness.Usage{
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		ReasoningTokens:  parsed.Usage.CompletionTokensDetails.ReasoningTokens,
		CachedTokens:     parsed.Usage.PromptTokensDetails.CachedTokens,
		TotalTokens:      parsed.Usage.TotalTokens,
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	if parsed.Usage.Cost != nil {
		usage.CostUSD = *parsed.Usage.Cost
		usage.CostAvailable = true
	} else if p.cfg.InputPricePerMillion > 0 || p.cfg.OutputPricePerMillion > 0 {
		usage.CostUSD = (float64(usage.PromptTokens)*p.cfg.InputPricePerMillion +
			float64(usage.CompletionTokens)*p.cfg.OutputPricePerMillion) / 1_000_000
		usage.CostAvailable = true
	}
	response.Usage = usage
	return response, nil
}

type ModelLister interface {
	ListModels(ctx context.Context) ([]harness.ModelInfo, error)
}

func (p *OpenAIProvider) ListModels(ctx context.Context) ([]harness.ModelInfo, error) {
	endpoint := strings.TrimRight(p.cfg.BaseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s models HTTP %d: %s", p.cfg.Name, resp.StatusCode, truncateString(string(body), 500))
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	models := make([]harness.ModelInfo, 0, len(parsed.Data))
	for _, model := range parsed.Data {
		models = append(models, harness.ModelInfo{
			ID: model.ID, Provider: p.cfg.Name, DisplayName: model.ID,
			Capabilities: p.cfg.Capabilities,
		})
	}
	return models, nil
}

func truncateString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
