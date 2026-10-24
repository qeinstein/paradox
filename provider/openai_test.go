package provider_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qeinstein/paradox/harness"
	"github.com/qeinstein/paradox/provider"
)

func TestOpenAICompatibleParsesToolCallsAndUsage(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing bearer auth")
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-test","model":"test-model","object":"chat.completion",
			"choices":[{"index":0,"finish_reason":"tool_calls","message":{
				"role":"assistant","content":null,
				"tool_calls":[{"id":"call-1","type":"function","function":{"name":"run_experiment","arguments":"{\"start\":0,\"end\":39}"}}]
			}}],
			"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,
				"completion_tokens_details":{"reasoning_tokens":5},
				"prompt_tokens_details":{"cached_tokens":10},"cost":0.0012}
		}`))
	}))
	defer server.Close()

	client := provider.NewOpenAICompatible(provider.OpenAIConfig{
		Name: "test", BaseURL: server.URL, APIKey: "test-key", Model: "test-model",
	})
	response, err := client.Complete(context.Background(), &harness.ModelRequest{
		Model: "test-model", System: "system prompt",
		Messages:  []harness.Message{{Role: harness.RoleUser, Content: "hello"}},
		Tools:     []harness.ToolDef{{Name: "run_experiment", Description: "run", Parameters: json.RawMessage(`{"type":"object"}`)}},
		MaxTokens: 512, ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].Name != "run_experiment" {
		t.Fatalf("unexpected tool calls: %+v", response.ToolCalls)
	}
	if !response.Usage.CostAvailable || response.Usage.CostUSD != 0.0012 {
		t.Fatalf("unexpected usage: %+v", response.Usage)
	}
	if response.Usage.ReasoningTokens != 5 || response.Usage.CachedTokens != 10 {
		t.Fatalf("unexpected detailed usage: %+v", response.Usage)
	}
	if received["model"] != "test-model" {
		t.Fatalf("model not forwarded: %+v", received)
	}
	if received["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort not forwarded: %+v", received)
	}
	if received["max_completion_tokens"] != float64(512) {
		t.Fatalf("max tokens not forwarded: %+v", received)
	}
	tools, _ := received["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools not forwarded: %+v", received)
	}
}

func TestOpenRouterUsesReasoningObject(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&received)
		_, _ = w.Write([]byte(`{"model":"x","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()
	client := provider.NewOpenAICompatible(provider.OpenAIConfig{
		Name: "openrouter", BaseURL: server.URL, APIKey: "k", Model: "x", ReasoningParam: "reasoning",
	})
	if _, err := client.Complete(context.Background(), &harness.ModelRequest{Model: "x", ReasoningEffort: "medium"}); err != nil {
		t.Fatal(err)
	}
	reasoning, _ := received["reasoning"].(map[string]any)
	if reasoning["effort"] != "medium" {
		t.Fatalf("reasoning object not forwarded: %+v", received)
	}
	if _, exists := received["reasoning_effort"]; exists {
		t.Fatalf("unexpected top-level reasoning_effort: %+v", received)
	}
	usage, _ := received["usage"].(map[string]any)
	if usage["include"] != true {
		t.Fatalf("OpenRouter usage reporting was not requested: %+v", received)
	}
}

func TestConfiguredPricesProvideCostAccounting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"x","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}],"usage":{"prompt_tokens":1000000,"completion_tokens":500000,"total_tokens":1500000}}`))
	}))
	defer server.Close()
	client := provider.NewOpenAICompatible(provider.OpenAIConfig{
		Name: "test", BaseURL: server.URL, APIKey: "k", Model: "x",
		InputPricePerMillion: 2, OutputPricePerMillion: 8,
	})
	response, err := client.Complete(context.Background(), &harness.ModelRequest{Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Usage.CostAvailable {
		t.Fatal("expected configured prices to make cost available")
	}
	if response.Usage.CostUSD != 6 {
		t.Fatalf("unexpected configured cost: %f", response.Usage.CostUSD)
	}
}

func TestProviderErrorDoesNotLeakKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream rejected request", http.StatusBadRequest)
	}))
	defer server.Close()
	client := provider.NewOpenAICompatible(provider.OpenAIConfig{
		Name: "test", BaseURL: server.URL, APIKey: "super-secret-key", Model: "x",
	})
	_, err := client.Complete(context.Background(), &harness.ModelRequest{Model: "x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "super-secret-key") {
		t.Fatal("error leaked API key")
	}
}

func TestListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a"},{"id":"model-b"}]}`))
	}))
	defer server.Close()
	client := provider.NewOpenAICompatible(provider.OpenAIConfig{Name: "test", BaseURL: server.URL, Model: "x"})
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "model-a" {
		t.Fatalf("unexpected models: %+v", models)
	}
}

func TestOpenRouterModelCatalogUsesReportedCapabilities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"vendor/tool-model","name":"Tool Model","context_length":128000,"supported_parameters":["tools","reasoning","response_format"]},
			{"id":"vendor/text-model","name":"Text Model","context_length":32000,"supported_parameters":["temperature"]}
		]}`))
	}))
	defer server.Close()
	client := provider.NewOpenAICompatible(provider.OpenAIConfig{
		Name: "openrouter", BaseURL: server.URL, Capabilities: harness.Capabilities{Streaming: true},
	})
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("expected two models, got %+v", models)
	}
	first := models[0]
	if first.DisplayName != "Tool Model" || first.Capabilities.MaxContextTokens != 128000 {
		t.Fatalf("model metadata missing: %+v", first)
	}
	if !first.Capabilities.ToolCalls || !first.Capabilities.StructuredOutput || !first.Capabilities.Reasoning {
		t.Fatalf("reported capabilities missing: %+v", first.Capabilities)
	}
	second := models[1]
	if second.Capabilities.ToolCalls || second.Capabilities.StructuredOutput || second.Capabilities.Reasoning {
		t.Fatalf("unsupported capabilities were advertised: %+v", second.Capabilities)
	}
	if !second.Capabilities.Streaming {
		t.Fatal("provider streaming capability should be preserved")
	}
}
