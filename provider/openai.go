// Package provider contains model-agnostic inference adapters. OpenRouter uses
// its OpenAI-compatible Chat Completions surface; Meta uses its Responses API
// for stateful multi-turn reasoning. Scientific logic never imports a provider.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
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
	return NewMetaWithPricing(apiKey, model, 0, 0)
}

// NewMetaWithPricing configures optional per-million-token prices for cost
// budgets when the Meta API response does not include a cost field.
func NewMetaWithPricing(apiKey, model string, inputPricePerMillion, outputPricePerMillion float64) *OpenAIProvider {
	if model == "" {
		model = "muse-spark-1.3"
	}
	return NewOpenAICompatible(OpenAIConfig{
		Name: "meta", BaseURL: MetaBaseURL, APIKey: apiKey, Model: model,
		InputPricePerMillion: inputPricePerMillion, OutputPricePerMillion: outputPricePerMillion,
		Capabilities: harness.Capabilities{
			ToolCalls: true, StructuredOutput: true, Reasoning: true,
			Streaming: true, MaxContextTokens: 1048576, MinOutputTokens: 16,
		},
	})
}

func NewOpenRouter(apiKey, model string) *OpenAIProvider {
	return NewOpenRouterWithPricing(apiKey, model, 0, 0)
}

// NewOpenRouterWithPricing configures optional fallback prices for cost
// accounting. OpenRouter normally reports the actual request cost.
func NewOpenRouterWithPricing(apiKey, model string, inputPricePerMillion, outputPricePerMillion float64) *OpenAIProvider {
	return NewOpenRouterWithCapabilities(apiKey, model, harness.Capabilities{}, inputPricePerMillion, outputPricePerMillion)
}

// NewOpenRouterWithCapabilities requires capabilities to come from explicit
// model-specific metadata or user confirmation. The provider API accepting a
// tools field does not mean every routed model supports tool calls.
func NewOpenRouterWithCapabilities(apiKey, model string, capabilities harness.Capabilities, inputPricePerMillion, outputPricePerMillion float64) *OpenAIProvider {
	return NewOpenAICompatible(OpenAIConfig{
		Name: "openrouter", BaseURL: OpenRouterBaseURL, APIKey: apiKey, Model: model,
		InputPricePerMillion: inputPricePerMillion, OutputPricePerMillion: outputPricePerMillion,
		Capabilities:   capabilities,
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
func (p *OpenAIProvider) RedactSensitive(value string) string {
	return redactSecret(value, p.cfg.APIKey)
}

// DryRun serializes the exact provider request body without making a network
// request or adding authorization headers. It is suitable for offline schema
// and provider-compatibility checks; callers should still redact task content
// before displaying the returned JSON.
func (p *OpenAIProvider) DryRun(request *harness.ModelRequest) ([]byte, error) {
	if request == nil {
		return nil, errors.New("model request is required")
	}
	if p.cfg.Name == "meta" {
		return p.buildMetaRequest(request)
	}
	return p.buildRequest(request)
}

func (p *OpenAIProvider) Complete(ctx context.Context, req *harness.ModelRequest) (*harness.ModelResponse, error) {
	if strings.TrimSpace(p.cfg.APIKey) == "" {
		return nil, errors.New("provider API key is empty")
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, errors.New("model id is empty")
	}
	if p.cfg.Name == "openrouter" && len(req.Tools) > 0 && !p.cfg.Capabilities.ToolCalls {
		return nil, errors.New("OpenRouter tool support is model-specific and has not been confirmed; select a model whose catalog lists `tools` in supported_parameters")
	}
	if p.cfg.Name == "openrouter" && req.ReasoningEffort != "" && !p.cfg.Capabilities.Reasoning {
		return nil, errors.New("OpenRouter reasoning parameters are model-specific and have not been confirmed; select a model whose catalog lists reasoning support")
	}
	if p.cfg.Name == "openrouter" && req.ResponseFormat != nil && !p.cfg.Capabilities.StructuredOutput {
		return nil, errors.New("OpenRouter structured output is model-specific and has not been confirmed; select a model whose catalog lists response_format support")
	}
	if p.cfg.Name == "meta" {
		return p.completeMetaResponses(ctx, req)
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
		return nil, newHTTPStatusError(p.cfg.Name, resp, responseBody, p.cfg.APIKey)
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
		return nil, newHTTPStatusError(p.cfg.Name+" models", resp, body, p.cfg.APIKey)
	}
	var parsed struct {
		Data []struct {
			ID                  string   `json:"id"`
			Name                string   `json:"name"`
			ContextLength       int      `json:"context_length"`
			SupportedParameters []string `json:"supported_parameters"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	models := make([]harness.ModelInfo, 0, len(parsed.Data))
	for _, model := range parsed.Data {
		capabilities := p.cfg.Capabilities
		if p.cfg.Name == "openrouter" {
			// OpenRouter's catalog is model-specific. Do not advertise tool or
			// structured-output support just because the API accepts those fields.
			capabilities = harness.Capabilities{
				ToolCalls:        supports(model.SupportedParameters, "tools"),
				StructuredOutput: supports(model.SupportedParameters, "response_format"),
				Reasoning:        supports(model.SupportedParameters, "reasoning") || supports(model.SupportedParameters, "reasoning_effort"),
				Streaming:        p.cfg.Capabilities.Streaming,
				MaxContextTokens: model.ContextLength,
			}
		}
		displayName := model.Name
		if displayName == "" {
			displayName = model.ID
		}
		models = append(models, harness.ModelInfo{
			ID: model.ID, Provider: p.cfg.Name, DisplayName: displayName,
			Capabilities: capabilities,
		})
	}
	return models, nil
}

func supports(parameters []string, wanted string) bool {
	for _, parameter := range parameters {
		if parameter == wanted {
			return true
		}
	}
	return false
}

func truncateString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

type HTTPStatusError struct {
	Provider   string
	StatusCode int
	Delay      time.Duration
	Message    string
}

func (e *HTTPStatusError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%s HTTP %d", e.Provider, e.StatusCode)
	}
	return fmt.Sprintf("%s HTTP %d: %s", e.Provider, e.StatusCode, e.Message)
}

func (e *HTTPStatusError) RetryDelay() time.Duration { return e.Delay }

func newHTTPStatusError(provider string, response *http.Response, body []byte, secret string) *HTTPStatusError {
	message := redactSecret(string(body), secret)
	return &HTTPStatusError{
		Provider: provider, StatusCode: response.StatusCode,
		Delay:   parseRetryAfter(response.Header.Get("Retry-After")),
		Message: truncateString(message, 800),
	}
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
}

func redactSecret(value, secret string) string {
	if strings.TrimSpace(secret) == "" {
		return value
	}
	value = strings.ReplaceAll(value, secret, "[REDACTED]")
	return value
}
