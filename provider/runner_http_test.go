package provider_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/qeinstein/paradox/app"
	"github.com/qeinstein/paradox/evidence"
	"github.com/qeinstein/paradox/harness"
	"github.com/qeinstein/paradox/provider"
)

type httpLoopDomain struct{}

func (httpLoopDomain) Name() string { return "http-fixture" }
func (httpLoopDomain) Init(context.Context, harness.Task) (*harness.DomainContext, error) {
	return &harness.DomainContext{Instructions: "Record the observed value."}, nil
}
func (httpLoopDomain) Tools() []harness.Tool {
	return []harness.Tool{{
		Name: "measure", Description: "return a deterministic measurement",
		Parameters: json.RawMessage(`{"type":"object","properties":{"sample":{"type":"string"}},"required":["sample"]}`),
		Handler: func(context.Context, *harness.ToolContext, json.RawMessage) (*harness.ToolResult, error) {
			return &harness.ToolResult{Content: "sample is positive", Data: map[string]any{"value": "positive"}, Evidence: []evidence.ObservationInput{{
				Text: "fixture measurement is positive", ClaimKey: "fixture:value", ClaimValue: "positive",
				Source: evidence.SourceExperiment,
			}}}, nil
		},
	}}
}
func (httpLoopDomain) VerificationPlans(context.Context, evidence.Node, evidence.Snapshot, harness.VerificationPolicy) ([]harness.VerificationPlan, error) {
	return nil, nil
}
func (httpLoopDomain) EvaluateVerification(context.Context, harness.VerificationPlan, *harness.ToolResult, evidence.Snapshot) (*harness.VerificationOutcome, error) {
	return &harness.VerificationOutcome{}, nil
}
func (httpLoopDomain) Evaluate(_ context.Context, input harness.EvaluationInput) (*harness.Evaluation, error) {
	for _, node := range input.Snapshot.Nodes {
		if node.Kind == evidence.KindClaim && node.Active && node.ClaimKey == "fixture:value" && node.ClaimValue == "positive" {
			return &harness.Evaluation{Success: input.Completed, Score: 1}, nil
		}
	}
	return &harness.Evaluation{Success: false}, nil
}

func TestOpenRouterCompletesMultiTurnRunnerWorkflowOverHTTP(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requests = append(requests, request)
		messages, _ := request["messages"].([]any)
		call := len(requests)
		if call > 1 {
			lastToolID := ""
			for _, raw := range messages {
				message, _ := raw.(map[string]any)
				if message["role"] == "tool" {
					lastToolID, _ = message["tool_call_id"].(string)
				}
			}
			want := "call-measure"
			if call == 3 {
				want = "call-submit_claim"
			}
			if lastToolID != want {
				t.Errorf("turn %d did not preserve tool-call ordering: last tool id=%q want=%q", call, lastToolID, want)
			}
		}
		var toolName, toolArgs string
		switch call {
		case 1:
			toolName, toolArgs = "measure", `{"sample":"A"}`
		case 2:
			id := evidenceIDFromOpenAI(messages)
			toolName, toolArgs = "submit_claim", fmt.Sprintf(`{"text":"measured positive","claim_key":"fixture:value","claim_value":"positive","evidence_ids":[%q]}`, id)
		default:
			toolName, toolArgs = "finish", `{"conclusion":"The evidence supports positive."}`
		}
		writeChatCompletion(w, "call-"+toolName, toolName, toolArgs, "")
	}))
	defer server.Close()

	model := provider.NewOpenAICompatible(provider.OpenAIConfig{
		Name: "openrouter", BaseURL: server.URL, APIKey: "local-fixture-key", Model: "fixture/model",
		Capabilities: harness.Capabilities{ToolCalls: true},
	})
	runHTTPWorkflow(t, model)
	if len(requests) != 3 {
		t.Fatalf("expected measure, claim, finish requests; got %d", len(requests))
	}
	if requests[1]["model"] != "fixture/model" || requests[1]["max_completion_tokens"] == nil {
		t.Fatalf("OpenRouter request parameters were not preserved: %+v", requests[1])
	}
}

func TestMetaResponsesCompletesMultiTurnRunnerWorkflowOverHTTP(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requests = append(requests, request)
		call := len(requests)
		if call > 1 {
			want := fmt.Sprintf("resp-%d", call-1)
			if request["previous_response_id"] != want {
				t.Errorf("turn %d previous_response_id=%v want=%s", call, request["previous_response_id"], want)
			}
			inputs, _ := request["input"].([]any)
			if len(inputs) != 1 {
				t.Errorf("turn %d expected only the new tool output: %+v", call, inputs)
			} else if item, ok := inputs[0].(map[string]any); !ok || item["type"] != "function_call_output" || item["call_id"] != map[int]string{2: "call-measure", 3: "call-submit_claim"}[call] {
				t.Errorf("turn %d malformed function output: %+v", call, inputs)
			}
		}
		var name, args string
		switch call {
		case 1:
			name, args = "measure", `{"sample":"A"}`
		case 2:
			id := evidenceIDFromMeta(requests[1])
			name, args = "submit_claim", fmt.Sprintf(`{"text":"measured positive","claim_key":"fixture:value","claim_value":"positive","evidence_ids":[%q]}`, id)
		default:
			name, args = "finish", `{"conclusion":"The evidence supports positive."}`
		}
		writeMetaResponse(w, fmt.Sprintf("resp-%d", call), "call-"+name, name, args)
	}))
	defer server.Close()

	model := provider.NewOpenAICompatible(provider.OpenAIConfig{
		Name: "meta", BaseURL: server.URL, APIKey: "local-fixture-key", Model: "muse-fixture",
		Capabilities: harness.Capabilities{ToolCalls: true},
	})
	runHTTPWorkflow(t, model)
	if len(requests) != 3 {
		t.Fatalf("expected three chained response requests, got %d", len(requests))
	}
}

func TestProviderCredentialEchoIsRedactedBeforePersistence(t *testing.T) {
	const secret = "local-provider-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "redaction-response", "model": "fixture/model",
			"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{
				"role": "assistant", "content": "Echoed credential: " + secret,
			}}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 4, "total_tokens": 14},
		})
	}))
	defer server.Close()
	model := provider.NewOpenAICompatible(provider.OpenAIConfig{
		Name: "openrouter", BaseURL: server.URL, APIKey: secret, Model: "fixture/model",
		Capabilities: harness.Capabilities{ToolCalls: true},
	})
	output, err := app.Run(context.Background(), app.RunOptions{
		Task: harness.Task{ID: "provider-secret-redaction", Domain: "http-fixture", Prompt: "return text"},
		Mode: harness.EvidenceMode, Provider: model, Domain: httpLoopDomain{}, RunsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.Result.FinalText, secret) || !strings.Contains(output.Result.FinalText, "[REDACTED]") {
		t.Fatalf("credential echo was not redacted from final text: %q", output.Result.FinalText)
	}
	events, err := os.ReadFile(output.RunDir + "/events.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(events), secret) {
		t.Fatal("credential echo was persisted to the event trace")
	}
}

func runHTTPWorkflow(t *testing.T, model harness.ModelProvider) {
	t.Helper()
	result, err := app.Run(context.Background(), app.RunOptions{
		Task: harness.Task{ID: "provider-http-workflow", Domain: "http-fixture", Prompt: "Measure and record the value."},
		Mode: harness.EvidenceMode, Provider: model, Domain: httpLoopDomain{}, RunsDir: t.TempDir(),
		Budget: harness.Budget{MaxSteps: 5, MaxToolCalls: 5, MaxTokens: 1000, MaxCostUSD: 1, TimeoutMS: 5000, ToolTimeoutMS: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Result.Success || result.Result.Usage.InferenceCalls != 3 || result.Result.Usage.ToolCalls != 3 {
		t.Fatalf("multi-turn workflow did not complete: %+v", result.Result)
	}
}

func evidenceIDFromOpenAI(messages []any) string {
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message["role"] != "tool" {
			continue
		}
		var payload struct {
			EvidenceIDs []string `json:"evidence_ids"`
		}
		_ = json.Unmarshal([]byte(fmt.Sprint(message["content"])), &payload)
		if len(payload.EvidenceIDs) > 0 {
			return payload.EvidenceIDs[0]
		}
	}
	return ""
}

func evidenceIDFromMeta(request map[string]any) string {
	inputs, _ := request["input"].([]any)
	for _, raw := range inputs {
		item, _ := raw.(map[string]any)
		if item["type"] != "function_call_output" {
			continue
		}
		var payload struct {
			EvidenceIDs []string `json:"evidence_ids"`
		}
		_ = json.Unmarshal([]byte(fmt.Sprint(item["output"])), &payload)
		if len(payload.EvidenceIDs) > 0 {
			return payload.EvidenceIDs[0]
		}
	}
	return ""
}

func writeChatCompletion(w http.ResponseWriter, id, name, arguments, text string) {
	w.Header().Set("Content-Type", "application/json")
	response := map[string]any{
		"id": "fixture-response", "model": "fixture/model",
		"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{
			"role": "assistant", "content": nil,
			"tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": arguments}}},
		}}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 3, "total_tokens": 13, "cost": 0.0001},
	}
	if text != "" {
		response["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"] = text
	}
	_ = json.NewEncoder(w).Encode(response)
}

func writeMetaResponse(w http.ResponseWriter, responseID, callID, name, arguments string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": responseID, "model": "muse-fixture", "status": "completed",
		"output": []any{map[string]any{"type": "function_call", "call_id": callID, "name": name, "arguments": arguments}},
		"usage":  map[string]any{"input_tokens": 10, "output_tokens": 3, "total_tokens": 13, "cost": 0.0001},
	})
}
