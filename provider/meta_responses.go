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

	"github.com/qeinstein/paradox/harness"
)

// Meta's Responses API can continue a model-side response chain across tool
// turns. ProviderState is opaque to the harness and is scoped to one Runner.
type metaResponseState struct {
	ResponseID   string `json:"response_id"`
	MessageCount int    `json:"message_count"`
}

type metaResponsesRequest struct {
	Model              string              `json:"model"`
	Instructions       string              `json:"instructions,omitempty"`
	Input              []metaResponseInput `json:"input"`
	Tools              []metaResponseTool  `json:"tools,omitempty"`
	MaxOutputTokens    int                 `json:"max_output_tokens,omitempty"`
	Temperature        *float64            `json:"temperature,omitempty"`
	Reasoning          *reasoningConfig    `json:"reasoning,omitempty"`
	PreviousResponseID string              `json:"previous_response_id,omitempty"`
	Store              bool                `json:"store"`
	ToolChoice         string              `json:"tool_choice,omitempty"`
	Text               *metaResponseText   `json:"text,omitempty"`
}

type metaResponseInput struct {
	Type    string                `json:"type,omitempty"`
	Role    string                `json:"role,omitempty"`
	Content []metaResponseContent `json:"content,omitempty"`
	CallID  string                `json:"call_id,omitempty"`
	Output  string                `json:"output,omitempty"`
}

type metaResponseContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type metaResponseTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      bool            `json:"strict,omitempty"`
}

type metaResponseText struct {
	Format *metaResponseFormat `json:"format,omitempty"`
}

type metaResponseFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
	Strict bool            `json:"strict,omitempty"`
}

func (p *OpenAIProvider) completeMetaResponses(ctx context.Context, req *harness.ModelRequest) (*harness.ModelResponse, error) {
	body, err := p.buildMetaRequest(req)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(p.cfg.BaseURL, "/") + "/responses"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	response, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("meta request failed: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, newHTTPStatusError("meta", response, responseBody, p.cfg.APIKey)
	}
	return p.parseMetaResponse(responseBody, len(req.Messages))
}

func (p *OpenAIProvider) buildMetaRequest(req *harness.ModelRequest) ([]byte, error) {
	state := metaResponseState{}
	if req.ProviderState != "" {
		if err := json.Unmarshal([]byte(req.ProviderState), &state); err != nil {
			return nil, fmt.Errorf("invalid Meta response state: %w", err)
		}
		if state.ResponseID == "" || state.MessageCount < 0 || state.MessageCount > len(req.Messages) {
			return nil, errors.New("invalid Meta response state: response ID or message boundary is invalid")
		}
	}
	start := state.MessageCount
	input, err := metaResponseInputs(req.Messages[start:])
	if err != nil {
		return nil, err
	}
	if len(input) == 0 {
		return nil, errors.New("Meta Responses API request has no new input")
	}
	request := metaResponsesRequest{
		Model: req.Model, Instructions: req.System, Input: input,
		MaxOutputTokens: req.MaxTokens, Temperature: req.Temperature,
		Store: true, ToolChoice: "auto", PreviousResponseID: state.ResponseID,
	}
	if req.ReasoningEffort != "" {
		request.Reasoning = &reasoningConfig{Effort: req.ReasoningEffort}
	}
	for _, tool := range req.Tools {
		parameters := tool.Parameters
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		request.Tools = append(request.Tools, metaResponseTool{
			Type: "function", Name: tool.Name, Description: tool.Description,
			Parameters: parameters,
		})
	}
	if req.ResponseFormat != nil {
		format := &metaResponseFormat{Type: req.ResponseFormat.Type}
		if req.ResponseFormat.Type == "json_schema" {
			format.Name = req.ResponseFormat.Name
			format.Schema = req.ResponseFormat.Schema
			format.Strict = req.ResponseFormat.Strict
		}
		request.Text = &metaResponseText{Format: format}
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode Meta Responses request: %w", err)
	}
	return body, nil
}

func metaResponseInputs(messages []harness.Message) ([]metaResponseInput, error) {
	inputs := make([]metaResponseInput, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case harness.RoleUser, harness.RoleSystem:
			role := string(message.Role)
			if message.Role == harness.RoleSystem {
				role = "developer"
			}
			inputs = append(inputs, metaResponseInput{
				Role: role, Content: []metaResponseContent{{Type: "input_text", Text: message.Content}},
			})
		case harness.RoleAssistant:
			// The previous_response_id already includes the assistant response and
			// its function_call items. Only its function_call_output is new input.
			if len(message.ToolCalls) == 0 && strings.TrimSpace(message.Content) != "" {
				inputs = append(inputs, metaResponseInput{
					Role: "assistant", Content: []metaResponseContent{{Type: "output_text", Text: message.Content}},
				})
			}
		case harness.RoleTool:
			if message.ToolCallID == "" {
				return nil, errors.New("Meta Responses API tool output is missing tool_call_id")
			}
			inputs = append(inputs, metaResponseInput{
				Type: "function_call_output", CallID: message.ToolCallID, Output: message.Content,
			})
		default:
			return nil, fmt.Errorf("unsupported message role %q for Meta Responses API", message.Role)
		}
	}
	return inputs, nil
}

type metaResponse struct {
	ID     string `json:"id"`
	Model  string `json:"model"`
	Status string `json:"status"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
	Output []struct {
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage struct {
		InputTokens        int `json:"input_tokens"`
		OutputTokens       int `json:"output_tokens"`
		TotalTokens        int `json:"total_tokens"`
		InputTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		OutputTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
		Cost *float64 `json:"cost"`
	} `json:"usage"`
}

func (p *OpenAIProvider) parseMetaResponse(body []byte, messageCount int) (*harness.ModelResponse, error) {
	var parsed metaResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("could not decode Meta Responses API response: %w", err)
	}
	if parsed.Status == "failed" {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return nil, fmt.Errorf("Meta response failed: %s", truncateString(redactSecret(parsed.Error.Message, p.cfg.APIKey), 800))
		}
		return nil, errors.New("Meta response failed")
	}
	result := &harness.ModelResponse{Model: parsed.Model, Raw: body}
	for _, item := range parsed.Output {
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				if content.Type == "output_text" {
					if result.Content != "" {
						result.Content += "\n"
					}
					result.Content += content.Text
				}
			}
		case "function_call":
			if item.CallID == "" || item.Name == "" {
				return nil, errors.New("Meta response returned a function call without call_id or name")
			}
			result.ToolCalls = append(result.ToolCalls, harness.ToolCall{
				ID: item.CallID, Name: item.Name, Arguments: item.Arguments,
			})
		}
	}
	if len(result.ToolCalls) > 0 {
		result.FinishReason = "tool_calls"
	} else if parsed.Status == "incomplete" {
		result.FinishReason = "length"
	} else {
		result.FinishReason = "stop"
	}
	if len(result.ToolCalls) > 0 {
		if parsed.ID == "" {
			return nil, errors.New("Meta response omitted its ID; cannot continue a tool-call turn")
		}
		state, err := json.Marshal(metaResponseState{ResponseID: parsed.ID, MessageCount: messageCount})
		if err != nil {
			return nil, err
		}
		result.ProviderState = string(state)
	} else if parsed.ID != "" {
		state, err := json.Marshal(metaResponseState{ResponseID: parsed.ID, MessageCount: messageCount})
		if err != nil {
			return nil, err
		}
		result.ProviderState = string(state)
	}
	usage := harness.Usage{
		PromptTokens: parsed.Usage.InputTokens, CompletionTokens: parsed.Usage.OutputTokens,
		ReasoningTokens: parsed.Usage.OutputTokensDetails.ReasoningTokens,
		CachedTokens:    parsed.Usage.InputTokensDetails.CachedTokens,
		TotalTokens:     parsed.Usage.TotalTokens,
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
	result.Usage = usage
	return result, nil
}
