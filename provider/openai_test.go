package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		Capabilities: harness.Capabilities{Reasoning: true},
	})
	if _, err := client.Complete(context.Background(), &harness.ModelRequest{Model: "x", ReasoningEffort: "medium", MaxTokens: 123}); err != nil {
		t.Fatal(err)
	}
	if received["max_completion_tokens"] != float64(123) || received["max_tokens"] != nil {
		t.Fatalf("OpenRouter must use its documented max_completion_tokens field: %+v", received)
	}
	reasoning, _ := received["reasoning"].(map[string]any)
	if reasoning["effort"] != "medium" {
		t.Fatalf("reasoning object not forwarded: %+v", received)
	}
	if _, exists := received["reasoning_effort"]; exists {
		t.Fatalf("unexpected top-level reasoning_effort: %+v", received)
	}
	if _, exists := received["usage"]; exists {
		t.Fatalf("non-streaming request should not send undocumented usage configuration: %+v", received)
	}
}

func TestOpenRouterToolCallsRequireExplicitModelCapability(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"model":"x","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()
	request := &harness.ModelRequest{Model: "x", Tools: []harness.ToolDef{{Name: "step", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	withoutCapability := provider.NewOpenAICompatible(provider.OpenAIConfig{
		Name: "openrouter", BaseURL: server.URL, APIKey: "fixture", Model: "x",
	})
	if _, err := withoutCapability.Complete(context.Background(), request); err == nil || !strings.Contains(err.Error(), "model-specific") {
		t.Fatalf("unconfirmed model tool support should fail clearly before network: %v", err)
	}
	if requests != 0 {
		t.Fatalf("unsupported capability check should not make request: %d", requests)
	}
	configured := provider.NewOpenRouterWithCapabilities("fixture", "x", harness.Capabilities{ToolCalls: true}, 0, 0)
	if !configured.Capabilities().ToolCalls {
		t.Fatal("explicit OpenRouter capability was not retained")
	}
	// Use a local endpoint while retaining the same capability behavior.
	withCapability := provider.NewOpenAICompatible(provider.OpenAIConfig{
		Name: "openrouter", BaseURL: server.URL, APIKey: "fixture", Model: "x",
		Capabilities: harness.Capabilities{ToolCalls: true},
	})
	if _, err := withCapability.Complete(context.Background(), request); err != nil {
		t.Fatalf("confirmed model tool support should permit request: %v", err)
	}
	if requests != 1 {
		t.Fatalf("expected one permitted request, got %d", requests)
	}
}

func TestOpenAICompatiblePreservesMultipleToolCallsAndMissingUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"multi","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[
			{"id":"call-a","type":"function","function":{"name":"one","arguments":"not-json"}},
			{"id":"call-b","type":"function","function":{"name":"two","arguments":"{}"}}
		]}}]}`))
	}))
	defer server.Close()
	client := provider.NewOpenAICompatible(provider.OpenAIConfig{Name: "test", BaseURL: server.URL, APIKey: "fixture", Model: "x"})
	response, err := client.Complete(context.Background(), &harness.ModelRequest{Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 2 || response.ToolCalls[0].ID != "call-a" || response.ToolCalls[1].ID != "call-b" {
		t.Fatalf("tool call ordering/IDs changed: %+v", response.ToolCalls)
	}
	if response.ToolCalls[0].Arguments != "not-json" || response.Usage.TotalTokens != 0 || response.Usage.CostAvailable {
		t.Fatalf("provider should preserve malformed arguments and mark omitted usage unavailable: %+v", response)
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
		http.Error(w, "upstream rejected Bearer super-secret-key", http.StatusBadRequest)
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
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("expected redacted response body, got %v", err)
	}
}

func TestHTTPStatusErrorCarriesRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		http.Error(w, `{"error":"rate limit"}`, http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := provider.NewOpenAICompatible(provider.OpenAIConfig{Name: "openrouter", BaseURL: server.URL, APIKey: "key", Model: "x"})
	_, err := client.Complete(context.Background(), &harness.ModelRequest{Model: "x"})
	var statusErr *provider.HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("expected provider HTTP status error, got %v", err)
	}
	if statusErr.StatusCode != http.StatusTooManyRequests || statusErr.RetryDelay() != 2*time.Second {
		t.Fatalf("status or Retry-After missing: %+v", statusErr)
	}
}

func TestProviderInferenceHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Some transports do not cancel the server request context immediately
		// after the client deadline. Respond after the client has already timed
		// out so the fixture server itself always shuts down deterministically.
		time.Sleep(250 * time.Millisecond)
		_, _ = w.Write([]byte(`{"model":"x","choices":[{"message":{"role":"assistant","content":"late"}}]}`))
	}))
	defer server.Close()
	client := provider.NewOpenAICompatible(provider.OpenAIConfig{
		Name: "openrouter", BaseURL: server.URL, APIKey: "fixture-key", Model: "vendor/model",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := client.Complete(ctx, &harness.ModelRequest{Model: "vendor/model"})
	if err == nil || ctx.Err() == nil || time.Since(started) > time.Second {
		t.Fatalf("provider request did not stop promptly on cancellation: err=%v elapsed=%s", err, time.Since(started))
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
