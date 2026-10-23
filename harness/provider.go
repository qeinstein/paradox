package harness

import (
	"context"
	"encoding/json"
	"sync"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ResponseFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
	Strict bool            `json:"strict,omitempty"`
}

type ModelRequest struct {
	Model           string          `json:"model"`
	System          string          `json:"system,omitempty"`
	Messages        []Message       `json:"messages"`
	Tools           []ToolDef       `json:"tools,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	MaxTokens       int             `json:"max_tokens,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	ResponseFormat  *ResponseFormat `json:"response_format,omitempty"`
	Seed            *int64          `json:"seed,omitempty"`
}

type ModelResponse struct {
	Content      string     `json:"content,omitempty"`
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`
	FinishReason string     `json:"finish_reason,omitempty"`
	Model        string     `json:"model,omitempty"`
	Usage        Usage      `json:"usage"`
	Raw          []byte     `json:"-"`
}

type Capabilities struct {
	ToolCalls        bool `json:"tool_calls"`
	StructuredOutput bool `json:"structured_output"`
	Reasoning        bool `json:"reasoning"`
	Streaming        bool `json:"streaming"`
	MaxContextTokens int  `json:"max_context_tokens,omitempty"`
}

type ModelInfo struct {
	ID           string       `json:"id"`
	Provider     string       `json:"provider"`
	DisplayName  string       `json:"display_name"`
	Capabilities Capabilities `json:"capabilities"`
}

type ModelProvider interface {
	Name() string
	Model() string
	Capabilities() Capabilities
	Complete(ctx context.Context, req *ModelRequest) (*ModelResponse, error)
}

// MockProvider is a deterministic in-process provider for tests and the
// internal evaluation suite. Its handler receives the full request so tests can
// drive multi-turn behavior without network access.
type MockProvider struct {
	NameValue  string
	ModelValue string
	Caps       Capabilities
	Handler    func(ctx context.Context, req *ModelRequest) (*ModelResponse, error)

	mu    sync.Mutex
	calls int
}

func NewMock(name, model string, handler func(context.Context, *ModelRequest) (*ModelResponse, error)) *MockProvider {
	return &MockProvider{
		NameValue: name, ModelValue: model,
		Caps:    Capabilities{ToolCalls: true, StructuredOutput: true, Reasoning: true},
		Handler: handler,
	}
}

func (m *MockProvider) Name() string               { return m.NameValue }
func (m *MockProvider) Model() string              { return m.ModelValue }
func (m *MockProvider) Capabilities() Capabilities { return m.Caps }
func (m *MockProvider) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *MockProvider) Complete(ctx context.Context, req *ModelRequest) (*ModelResponse, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.Handler == nil {
		return &ModelResponse{Content: "mock provider has no handler", FinishReason: "stop"}, nil
	}
	return m.Handler(ctx, req)
}
