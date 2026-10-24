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

func TestMetaResponsesChainsToolCallsAndUsage(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Method != http.MethodPost {
			t.Errorf("unexpected endpoint %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing bearer auth")
		}
		var received map[string]any
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests = append(requests, received)
		w.Header().Set("Content-Type", "application/json")
		if len(requests) == 1 {
			_, _ = w.Write([]byte(`{
				"id":"resp-1","model":"muse-spark-1.3","status":"completed",
				"output":[{"type":"function_call","call_id":"call-1","name":"run_experiment","arguments":"{\"start\":0,\"end\":10}"}],
				"usage":{"input_tokens":100,"output_tokens":30,"total_tokens":130,
					"input_tokens_details":{"cached_tokens":10},
					"output_tokens_details":{"reasoning_tokens":20},"cost":0.0012}
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"resp-2","model":"muse-spark-1.3","status":"completed",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done with the experiment."}]}],
			"usage":{"input_tokens":40,"output_tokens":12,"total_tokens":52}
		}`))
	}))
	defer server.Close()

	client := provider.NewOpenAICompatible(provider.OpenAIConfig{
		Name: "meta", BaseURL: server.URL, APIKey: "test-key", Model: "muse-spark-1.3",
		Capabilities: harness.Capabilities{ToolCalls: true, StructuredOutput: true, Reasoning: true},
	})
	first, err := client.Complete(context.Background(), &harness.ModelRequest{
		Model: "muse-spark-1.3", System: "Follow the research protocol.",
		Messages: []harness.Message{{Role: harness.RoleUser, Content: "Run the experiment."}},
		Tools: []harness.ToolDef{{
			Name: "run_experiment", Description: "runs a fixed experiment",
			Parameters: json.RawMessage(`{"type":"object","properties":{"start":{"type":"integer"}}}`),
		}},
		MaxTokens: 512, ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].ID != "call-1" || first.ToolCalls[0].Name != "run_experiment" {
		t.Fatalf("unexpected function call: %+v", first.ToolCalls)
	}
	if first.FinishReason != "tool_calls" || first.Usage.TotalTokens != 130 || first.Usage.ReasoningTokens != 20 || first.Usage.CachedTokens != 10 {
		t.Fatalf("unexpected response accounting: %+v", first)
	}
	if !first.Usage.CostAvailable || first.Usage.CostUSD != 0.0012 {
		t.Fatalf("expected provider-reported cost: %+v", first.Usage)
	}

	var state struct {
		ResponseID   string `json:"response_id"`
		MessageCount int    `json:"message_count"`
	}
	if err := json.Unmarshal([]byte(first.ProviderState), &state); err != nil {
		t.Fatal(err)
	}
	if state.ResponseID != "resp-1" || state.MessageCount != 1 {
		t.Fatalf("unexpected provider state: %+v", state)
	}

	second, err := client.Complete(context.Background(), &harness.ModelRequest{
		Model: "muse-spark-1.3", System: "Follow the research protocol.",
		Messages: []harness.Message{
			{Role: harness.RoleUser, Content: "Run the experiment."},
			{Role: harness.RoleAssistant, ToolCalls: first.ToolCalls},
			{Role: harness.RoleTool, ToolCallID: "call-1", Name: "run_experiment", Content: `{"ok":true}`},
		},
		Tools:     []harness.ToolDef{{Name: "run_experiment", Parameters: json.RawMessage(`{"type":"object"}`)}},
		MaxTokens: 512, ProviderState: first.ProviderState,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Content != "Done with the experiment." || len(second.ToolCalls) != 0 {
		t.Fatalf("unexpected final response: %+v", second)
	}
	if len(requests) != 2 {
		t.Fatalf("expected two API calls, got %d", len(requests))
	}
	firstRequest := requests[0]
	if firstRequest["instructions"] != "Follow the research protocol." || firstRequest["max_output_tokens"] != float64(512) || firstRequest["store"] != true {
		t.Fatalf("Meta request options missing: %+v", firstRequest)
	}
	reasoning, _ := firstRequest["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" {
		t.Fatalf("reasoning effort missing: %+v", firstRequest)
	}
	toolDefs, _ := firstRequest["tools"].([]any)
	toolDef, _ := toolDefs[0].(map[string]any)
	if toolDef["type"] != "function" || toolDef["name"] != "run_experiment" {
		t.Fatalf("Responses API tool definition is not flat: %+v", firstRequest["tools"])
	}
	secondRequest := requests[1]
	if secondRequest["previous_response_id"] != "resp-1" {
		t.Fatalf("prior response was not chained: %+v", secondRequest)
	}
	inputs, _ := secondRequest["input"].([]any)
	if len(inputs) != 1 {
		t.Fatalf("expected only the new function output as input: %+v", inputs)
	}
	toolOutput, _ := inputs[0].(map[string]any)
	if toolOutput["type"] != "function_call_output" || toolOutput["call_id"] != "call-1" || toolOutput["output"] != `{"ok":true}` {
		t.Fatalf("unexpected function output item: %+v", toolOutput)
	}
}

func TestMetaFailedResponseRedactsCredentialEcho(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"resp-failed","status":"failed","error":{"message":"rejected secret-meta-key"}}`))
	}))
	defer server.Close()
	client := provider.NewOpenAICompatible(provider.OpenAIConfig{
		Name: "meta", BaseURL: server.URL, APIKey: "secret-meta-key", Model: "muse-fixture",
	})
	_, err := client.Complete(context.Background(), &harness.ModelRequest{
		Model: "muse-fixture", Messages: []harness.Message{{Role: harness.RoleUser, Content: "test"}},
	})
	if err == nil || strings.Contains(err.Error(), "secret-meta-key") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("Meta failure did not safely redact credential: %v", err)
	}
}
