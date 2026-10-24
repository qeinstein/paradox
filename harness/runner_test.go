package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qeinstein/paradox/evidence"
	"github.com/qeinstein/paradox/harness"
)

type stubDomain struct {
	tools      []harness.Tool
	evaluation harness.Evaluation
	plans      []harness.VerificationPlan
	outcome    *harness.VerificationOutcome
}

func (d *stubDomain) Name() string { return "test" }
func (d *stubDomain) Init(context.Context, harness.Task) (*harness.DomainContext, error) {
	return &harness.DomainContext{Instructions: "test instructions"}, nil
}
func (d *stubDomain) Tools() []harness.Tool { return d.tools }
func (d *stubDomain) VerificationPlans(_ context.Context, claim evidence.Node, _ evidence.Snapshot, _ harness.VerificationPolicy) ([]harness.VerificationPlan, error) {
	plans := append([]harness.VerificationPlan(nil), d.plans...)
	for index := range plans {
		if plans[index].ClaimID == "" {
			plans[index].ClaimID = claim.ID
		}
		if plans[index].AssumptionID == "" && len(claim.AssumptionIDs) > 0 {
			plans[index].AssumptionID = claim.AssumptionIDs[0]
		}
	}
	return plans, nil
}
func (d *stubDomain) EvaluateVerification(context.Context, harness.VerificationPlan, *harness.ToolResult, evidence.Snapshot) (*harness.VerificationOutcome, error) {
	if d.outcome == nil {
		return &harness.VerificationOutcome{}, nil
	}
	result := *d.outcome
	return &result, nil
}
func (d *stubDomain) Evaluate(context.Context, harness.EvaluationInput) (*harness.Evaluation, error) {
	evaluation := d.evaluation
	return &evaluation, nil
}

type captureRecorder struct {
	mu     sync.Mutex
	events []harness.Event
}

func (r *captureRecorder) Record(event harness.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}
func (r *captureRecorder) SaveResult(*harness.RunResult) error  { return nil }
func (r *captureRecorder) SaveEvidence(evidence.Snapshot) error { return nil }
func (r *captureRecorder) eventTypes() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]int{}
	for _, event := range r.events {
		out[event.Type]++
	}
	return out
}

func testBudget() harness.Budget {
	return harness.Budget{
		MaxSteps: 5, MaxToolCalls: 10, MaxTokens: 10000,
		MaxCostUSD: 1, TimeoutMS: 3000, MaxRetries: 0, ToolTimeoutMS: 1000,
	}
}

func runTestHarness(t *testing.T, ctx context.Context, provider harness.ModelProvider, domain harness.DomainAdapter, budget harness.Budget, recorder harness.Recorder) *harness.RunResult {
	t.Helper()
	result, err := harness.Run(ctx, harness.RunConfig{
		Task: harness.Task{ID: "test-task", Domain: "test", Prompt: "test prompt"},
		Mode: harness.Paradox, Provider: provider, Domain: domain,
		Budget: budget, WorkDir: t.TempDir(), Recorder: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestProviderRetriesThenSucceeds(t *testing.T) {
	calls := 0
	provider := harness.NewMock("test", "test-model", func(context.Context, *harness.ModelRequest) (*harness.ModelResponse, error) {
		calls++
		if calls < 3 {
			return nil, errors.New("temporary upstream failure")
		}
		return &harness.ModelResponse{
			Content: "finished", FinishReason: "stop",
			Usage: harness.Usage{TotalTokens: 10, CostUSD: 0.001, CostAvailable: true},
		}, nil
	})
	budget := testBudget()
	budget.MaxRetries = 2
	recorder := &captureRecorder{}
	result := runTestHarness(t, context.Background(), provider, &stubDomain{
		evaluation: harness.Evaluation{Success: true, Score: 1},
	}, budget, recorder)
	if calls != 3 {
		t.Fatalf("expected three provider attempts, got %d", calls)
	}
	if !result.Success || result.Error != "" {
		t.Fatalf("expected successful run after retries: %+v", result)
	}
	if result.Usage.InferenceCalls != 1 {
		t.Fatalf("expected one successful inference call, got %d", result.Usage.InferenceCalls)
	}
	if recorder.eventTypes()["provider_error"] != 2 {
		t.Fatalf("expected two provider_error events: %+v", recorder.eventTypes())
	}
}

type retryAfterFixtureError struct{ delay time.Duration }

func (e retryAfterFixtureError) Error() string             { return "fixture rate limit" }
func (e retryAfterFixtureError) RetryDelay() time.Duration { return e.delay }

func TestProviderHonorsServerRetryDelay(t *testing.T) {
	calls := 0
	provider := harness.NewMock("test", "test-model", func(context.Context, *harness.ModelRequest) (*harness.ModelResponse, error) {
		calls++
		if calls == 1 {
			return nil, retryAfterFixtureError{delay: 250 * time.Millisecond}
		}
		return &harness.ModelResponse{Content: "done"}, nil
	})
	budget := testBudget()
	budget.MaxRetries = 1
	started := time.Now()
	result := runTestHarness(t, context.Background(), provider, &stubDomain{}, budget, nil)
	if result.Error != "" || calls != 2 || time.Since(started) < 240*time.Millisecond {
		t.Fatalf("retry delay was not honored: calls=%d elapsed=%s result=%+v", calls, time.Since(started), result)
	}
}

func TestKnownContextWindowCapsOutputAndRejectsOversizedPrompt(t *testing.T) {
	provider := harness.NewMock("test", "test-model", func(context.Context, *harness.ModelRequest) (*harness.ModelResponse, error) {
		t.Fatal("provider must not be called when the prompt exceeds its advertised context")
		return nil, nil
	})
	provider.Caps.MaxContextTokens = 64
	result := runTestHarness(t, context.Background(), provider, &stubDomain{}, testBudget(), nil)
	if !strings.Contains(result.Error, "exceeds provider context window") {
		t.Fatalf("expected context-window error, got %q", result.Error)
	}
}

func TestMissingRequiredToolParameterDoesNotRunHandler(t *testing.T) {
	handlerCalls := 0
	providerCalls := 0
	provider := harness.NewMock("test", "test-model", func(context.Context, *harness.ModelRequest) (*harness.ModelResponse, error) {
		providerCalls++
		if providerCalls == 1 {
			return &harness.ModelResponse{ToolCalls: []harness.ToolCall{{
				ID: "call-1", Name: "measure", Arguments: `{"not_value":"invented"}`,
			}}}, nil
		}
		return &harness.ModelResponse{Content: "done"}, nil
	})
	domain := &stubDomain{tools: []harness.Tool{{
		Name: "measure", Parameters: json.RawMessage(`{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"]}`),
		Handler: func(context.Context, *harness.ToolContext, json.RawMessage) (*harness.ToolResult, error) {
			handlerCalls++
			return &harness.ToolResult{Content: "executed"}, nil
		},
	}}}
	recorder := &captureRecorder{}
	result := runTestHarness(t, context.Background(), provider, domain, testBudget(), recorder)
	if handlerCalls != 0 {
		t.Fatalf("invalid arguments reached the tool handler %d times", handlerCalls)
	}
	if result.Error != "" || recorder.eventTypes()["tool_error"] == 0 {
		t.Fatalf("expected an explicit tool error and recoverable run, result=%+v events=%v", result, recorder.eventTypes())
	}
}

func TestMultipleToolCallsExecuteInReturnedOrder(t *testing.T) {
	var executed []string
	providerCalls := 0
	provider := harness.NewMock("test", "test-model", func(_ context.Context, request *harness.ModelRequest) (*harness.ModelResponse, error) {
		providerCalls++
		if providerCalls == 1 {
			return &harness.ModelResponse{ToolCalls: []harness.ToolCall{
				{ID: "call-first", Name: "measure", Arguments: `{"id":"first"}`},
				{ID: "call-second", Name: "measure", Arguments: `{"id":"second"}`},
			}}, nil
		}
		var toolIDs []string
		for _, message := range request.Messages {
			if message.Role == harness.RoleTool {
				toolIDs = append(toolIDs, message.ToolCallID)
			}
		}
		if len(toolIDs) != 2 || toolIDs[0] != "call-first" || toolIDs[1] != "call-second" {
			return nil, fmt.Errorf("tool output ordering changed: %v", toolIDs)
		}
		return &harness.ModelResponse{Content: "done"}, nil
	})
	domain := &stubDomain{tools: []harness.Tool{{
		Name: "measure", Parameters: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		Handler: func(_ context.Context, _ *harness.ToolContext, raw json.RawMessage) (*harness.ToolResult, error) {
			var args struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			executed = append(executed, args.ID)
			return &harness.ToolResult{Content: args.ID}, nil
		},
	}}}
	result := runTestHarness(t, context.Background(), provider, domain, testBudget(), nil)
	if result.Error != "" || len(executed) != 2 || executed[0] != "first" || executed[1] != "second" || result.Usage.ToolCalls != 2 {
		t.Fatalf("multiple tool-call turn was not executed in order: result=%+v executed=%v", result, executed)
	}
}

func TestProviderTruncatedResponseIsNotAcceptedAsComplete(t *testing.T) {
	provider := harness.NewMock("test", "test-model", func(context.Context, *harness.ModelRequest) (*harness.ModelResponse, error) {
		return &harness.ModelResponse{Content: "partial", FinishReason: "length"}, nil
	})
	result := runTestHarness(t, context.Background(), provider, &stubDomain{}, testBudget(), nil)
	if !strings.Contains(result.Error, "truncated") || result.Success {
		t.Fatalf("provider-truncated answer should fail the run: %+v", result)
	}
}

func TestProviderStateIsPassedBetweenTurnsAndResetForEachRun(t *testing.T) {
	runNumber := 0
	provider := harness.NewMock("test", "test-model", func(_ context.Context, req *harness.ModelRequest) (*harness.ModelResponse, error) {
		if len(req.Messages) == 1 {
			if req.ProviderState != "" {
				return nil, errors.New("provider state leaked into a new run")
			}
			runNumber++
			return &harness.ModelResponse{
				ToolCalls:     []harness.ToolCall{{ID: "call-echo", Name: "echo", Arguments: `{}`}},
				ProviderState: fmt.Sprintf("response-%d", runNumber),
			}, nil
		}
		if req.ProviderState != fmt.Sprintf("response-%d", runNumber) {
			return nil, fmt.Errorf("provider state did not continue current run: %q", req.ProviderState)
		}
		return &harness.ModelResponse{Content: "finished"}, nil
	})
	echo := harness.Tool{
		Name: "echo", Description: "returns ok",
		Handler: func(context.Context, *harness.ToolContext, json.RawMessage) (*harness.ToolResult, error) {
			return &harness.ToolResult{Content: "ok"}, nil
		},
	}
	domain := &stubDomain{tools: []harness.Tool{echo}, evaluation: harness.Evaluation{Success: true, Score: 1}}
	for i := 0; i < 2; i++ {
		result := runTestHarness(t, context.Background(), provider, domain, testBudget(), &captureRecorder{})
		if !result.Success {
			t.Fatalf("run %d failed: %+v", i+1, result)
		}
	}
}

func TestToolTimeoutIsReportedToTheModel(t *testing.T) {
	calls := 0
	provider := harness.NewMock("test", "test-model", func(context.Context, *harness.ModelRequest) (*harness.ModelResponse, error) {
		calls++
		if calls == 1 {
			return &harness.ModelResponse{ToolCalls: []harness.ToolCall{{
				ID: "call-slow", Name: "slow", Arguments: "{}",
			}}}, nil
		}
		return &harness.ModelResponse{Content: "finished", FinishReason: "stop"}, nil
	})
	slow := harness.Tool{
		Name: "slow", Description: "waits until cancelled",
		Handler: func(ctx context.Context, _ *harness.ToolContext, _ json.RawMessage) (*harness.ToolResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	budget := testBudget()
	budget.ToolTimeoutMS = 50
	recorder := &captureRecorder{}
	result := runTestHarness(t, context.Background(), provider, &stubDomain{
		tools: []harness.Tool{slow}, evaluation: harness.Evaluation{Success: true, Score: 1},
	}, budget, recorder)
	if !result.Success {
		t.Fatalf("expected run to recover after a tool timeout: %+v", result)
	}
	if result.Usage.ToolCalls != 1 {
		t.Fatalf("expected one tool call, got %d", result.Usage.ToolCalls)
	}
	if recorder.eventTypes()["tool_error"] != 1 {
		t.Fatalf("expected a tool_error event: %+v", recorder.eventTypes())
	}
}

func TestToolCallBudgetStopsRunAndCannotBeSuccessful(t *testing.T) {
	provider := harness.NewMock("test", "test-model", func(context.Context, *harness.ModelRequest) (*harness.ModelResponse, error) {
		return &harness.ModelResponse{ToolCalls: []harness.ToolCall{{
			ID: "call-echo", Name: "echo", Arguments: "{}",
		}}}, nil
	})
	echo := harness.Tool{
		Name: "echo", Description: "returns ok",
		Handler: func(context.Context, *harness.ToolContext, json.RawMessage) (*harness.ToolResult, error) {
			return &harness.ToolResult{Content: "ok"}, nil
		},
	}
	budget := testBudget()
	budget.MaxToolCalls = 1
	result := runTestHarness(t, context.Background(), provider, &stubDomain{
		tools: []harness.Tool{echo}, evaluation: harness.Evaluation{Success: true, Score: 1},
	}, budget, &captureRecorder{})
	if !strings.Contains(result.Error, "tool-call budget exhausted") {
		t.Fatalf("expected tool-call budget error, got %q", result.Error)
	}
	if result.Success {
		t.Fatal("a budget-exhausted run must not be marked successful")
	}
}

func TestVerificationIsSkippedWhenNoToolBudgetRemains(t *testing.T) {
	providerCalls := 0
	var observationID string
	provider := harness.NewMock("test", "test-model", func(_ context.Context, request *harness.ModelRequest) (*harness.ModelResponse, error) {
		providerCalls++
		if providerCalls == 1 {
			return &harness.ModelResponse{ToolCalls: []harness.ToolCall{{ID: "measure-1", Name: "measure", Arguments: `{}`}}}, nil
		}
		if providerCalls == 2 {
			for _, message := range request.Messages {
				if message.Role == harness.RoleTool {
					var payload struct {
						EvidenceIDs []string `json:"evidence_ids"`
					}
					_ = json.Unmarshal([]byte(message.Content), &payload)
					if len(payload.EvidenceIDs) > 0 {
						observationID = payload.EvidenceIDs[0]
					}
				}
			}
			args, _ := json.Marshal(map[string]any{
				"text": "measurement supports the provisional claim", "claim_key": "budget:claim",
				"claim_value": "true", "evidence_ids": []string{observationID},
				"assumptions": []map[string]any{{"text": "instrument is calibrated", "kind": "calibration"}},
			})
			return &harness.ModelResponse{ToolCalls: []harness.ToolCall{{ID: "claim-1", Name: "submit_claim", Arguments: string(args)}}}, nil
		}
		return &harness.ModelResponse{ToolCalls: []harness.ToolCall{{
			ID: "finish-1", Name: "finish", Arguments: `{"conclusion":"budget limited verification"}`,
		}}}, nil
	})
	domain := &stubDomain{
		plans:   []harness.VerificationPlan{{Tool: "verify", Args: map[string]any{}, Check: "fixture"}},
		outcome: &harness.VerificationOutcome{Contradicted: true, Reason: "would fail if executed"},
		tools: []harness.Tool{
			{Name: "measure", Parameters: json.RawMessage(`{"type":"object"}`), Handler: func(context.Context, *harness.ToolContext, json.RawMessage) (*harness.ToolResult, error) {
				return &harness.ToolResult{Content: "measurement", Evidence: []evidence.ObservationInput{{Text: "measurement is 12", Source: evidence.SourceExperiment}}}, nil
			}},
			{Name: "verify", Parameters: json.RawMessage(`{"type":"object"}`), Handler: func(context.Context, *harness.ToolContext, json.RawMessage) (*harness.ToolResult, error) {
				return &harness.ToolResult{Content: "verification result", Evidence: []evidence.ObservationInput{{Text: "verification evidence", Source: evidence.SourceExperiment}}}, nil
			}},
		},
		evaluation: harness.Evaluation{Success: true, Score: 1},
	}
	budget := testBudget()
	budget.MaxToolCalls = 2
	result := runTestHarness(t, context.Background(), provider, domain, budget, &captureRecorder{})
	if !strings.Contains(result.Error, "tool-call budget exhausted") {
		t.Fatalf("expected bounded failure once no tool budget remained: %+v", result)
	}
	if result.Snapshot.Stats.Verifications != 0 || result.Snapshot.Stats.Retractions != 0 {
		t.Fatalf("budget-exhausted verification must not be recorded as completed or retract the claim: %+v", result.Snapshot)
	}
}

func TestLongRetractionSequenceRequestsReplanningEachTime(t *testing.T) {
	provider := harness.NewMock("test", "test-model", func(_ context.Context, request *harness.ModelRequest) (*harness.ModelResponse, error) {
		invalidations := 0
		for _, message := range request.Messages {
			if message.Role == harness.RoleUser && strings.Contains(message.Content, "INVALIDATED") {
				invalidations++
			}
		}
		if invalidations == 3 {
			return &harness.ModelResponse{ToolCalls: []harness.ToolCall{{
				ID: "finish-1", Name: "finish", Arguments: `{"conclusion":"replanned after three invalidations"}`,
			}}}, nil
		}
		args, _ := json.Marshal(map[string]any{
			"text":        fmt.Sprintf("revised conclusion attempt %d", invalidations+1),
			"claim_key":   fmt.Sprintf("recovery:attempt:%d", invalidations+1),
			"claim_value": "provisional", "assumptions": []map[string]any{{
				"text": fmt.Sprintf("premise for attempt %d", invalidations+1), "kind": "test_premise",
			}},
		})
		return &harness.ModelResponse{ToolCalls: []harness.ToolCall{{ID: fmt.Sprintf("claim-%d", invalidations+1), Name: "submit_claim", Arguments: string(args)}}}, nil
	})
	domain := &stubDomain{
		plans:   []harness.VerificationPlan{{Tool: "verify", Args: map[string]any{}, Check: "fixture"}},
		outcome: &harness.VerificationOutcome{Contradicted: true, Reason: "fixture premise was falsified"},
		tools: []harness.Tool{{Name: "verify", Parameters: json.RawMessage(`{"type":"object"}`), Handler: func(context.Context, *harness.ToolContext, json.RawMessage) (*harness.ToolResult, error) {
			return &harness.ToolResult{Content: "counterexample", Evidence: []evidence.ObservationInput{{
				Text: "verification returned a counterexample", Source: evidence.SourceExperiment,
			}}}, nil
		}}},
		evaluation: harness.Evaluation{Success: true, Score: 1},
	}
	recorder := &captureRecorder{}
	result := runTestHarness(t, context.Background(), provider, domain, testBudget(), recorder)
	if !result.Success || result.Snapshot.Stats.Contradictions != 3 || result.Snapshot.Stats.Retractions != 6 {
		verificationErrors := []map[string]any{}
		for _, event := range recorder.events {
			if event.Type == "verification_error" {
				verificationErrors = append(verificationErrors, event.Data)
			}
		}
		t.Fatalf("long invalidation chain did not recover: result=%+v events=%+v verification_errors=%+v", result, recorder.eventTypes(), verificationErrors)
	}
	if recorder.eventTypes()["replan_required"] != 3 || result.Usage.ToolCalls != 7 {
		t.Fatalf("expected three bounded replans and seven total tool calls, events=%+v usage=%+v", recorder.eventTypes(), result.Usage)
	}
}

func TestExpiredAssumptionTriggersReplanBeforeNextModelAction(t *testing.T) {
	calls := 0
	provider := harness.NewMock("test", "test-model", func(_ context.Context, request *harness.ModelRequest) (*harness.ModelResponse, error) {
		calls++
		if calls == 1 {
			args, _ := json.Marshal(map[string]any{
				"text": "claim based on an expired calibration", "claim_key": "calibration:claim",
				"claim_value": "true", "assumptions": []map[string]any{{
					"text": "the calibration is current", "kind": "calibration",
					"expires_at": time.Now().Add(-time.Second).UnixMilli(),
				}},
			})
			return &harness.ModelResponse{ToolCalls: []harness.ToolCall{{ID: "claim-1", Name: "submit_claim", Arguments: string(args)}}}, nil
		}
		sawReplan := false
		for _, message := range request.Messages {
			if strings.Contains(message.Content, "assumption") && strings.Contains(message.Content, "expired") {
				sawReplan = true
			}
		}
		if !sawReplan {
			return nil, errors.New("second model turn did not receive the expired-assumption replan")
		}
		return &harness.ModelResponse{ToolCalls: []harness.ToolCall{{
			ID: "finish-1", Name: "finish", Arguments: `{"conclusion":"replanned"}`,
		}}}, nil
	})
	recorder := &captureRecorder{}
	result := runTestHarness(t, context.Background(), provider, &stubDomain{
		evaluation: harness.Evaluation{Success: true, Score: 1},
	}, testBudget(), recorder)
	if !result.Success || calls != 2 || result.Snapshot.Stats.Retractions == 0 {
		t.Fatalf("expired support did not recover through a bounded replan: calls=%d result=%+v", calls, result)
	}
	if recorder.eventTypes()["assumption_expired"] != 1 || recorder.eventTypes()["replan_required"] == 0 {
		t.Fatalf("missing expiration/replan trace events: %+v", recorder.eventTypes())
	}
}

func TestVerifierCannotRetractUsingEvidenceFromAnotherOrMissingToolCall(t *testing.T) {
	var observationID string
	providerCalls := 0
	provider := harness.NewMock("test", "test-model", func(_ context.Context, request *harness.ModelRequest) (*harness.ModelResponse, error) {
		providerCalls++
		switch providerCalls {
		case 1:
			return &harness.ModelResponse{ToolCalls: []harness.ToolCall{{ID: "measure-1", Name: "measure", Arguments: `{}`}}}, nil
		case 2:
			for _, message := range request.Messages {
				if message.Role == harness.RoleTool {
					var payload struct {
						EvidenceIDs []string `json:"evidence_ids"`
					}
					_ = json.Unmarshal([]byte(message.Content), &payload)
					if len(payload.EvidenceIDs) > 0 {
						observationID = payload.EvidenceIDs[0]
					}
				}
			}
			args, _ := json.Marshal(map[string]any{
				"text": "measurement supports the provisional conclusion", "claim_key": "test:claim",
				"claim_value": "true", "evidence_ids": []string{observationID},
				"assumptions": []map[string]any{{"text": "instrument is calibrated", "kind": "calibration"}},
			})
			return &harness.ModelResponse{ToolCalls: []harness.ToolCall{{ID: "claim-1", Name: "submit_claim", Arguments: string(args)}}}, nil
		default:
			return &harness.ModelResponse{ToolCalls: []harness.ToolCall{{ID: "finish-1", Name: "finish", Arguments: `{"conclusion":"retain pending verification"}`}}}, nil
		}
	})
	domain := &stubDomain{
		plans:   []harness.VerificationPlan{{ID: "verify-calibration", Tool: "verify", Check: "fixture"}},
		outcome: &harness.VerificationOutcome{Contradicted: true, EvidenceID: "obs-does-not-exist", Reason: "deliberately invalid reference"},
		tools: []harness.Tool{
			{
				Name: "measure", Parameters: json.RawMessage(`{"type":"object"}`),
				Handler: func(context.Context, *harness.ToolContext, json.RawMessage) (*harness.ToolResult, error) {
					return &harness.ToolResult{Content: "measurement", Evidence: []evidence.ObservationInput{{Text: "measurement is 12", Source: evidence.SourceExperiment}}}, nil
				},
			},
			{
				Name: "verify", Parameters: json.RawMessage(`{"type":"object"}`),
				Handler: func(context.Context, *harness.ToolContext, json.RawMessage) (*harness.ToolResult, error) {
					return &harness.ToolResult{Content: "no new observation"}, nil
				},
			},
		},
		evaluation: harness.Evaluation{Success: true, Score: 1},
	}
	recorder := &captureRecorder{}
	result := runTestHarness(t, context.Background(), provider, domain, testBudget(), recorder)
	if !result.Success || result.Snapshot.Stats.Contradictions != 0 || result.Snapshot.Stats.Retractions != 0 {
		t.Fatalf("verifier with no tool-produced evidence retracted a claim: %+v", result)
	}
	if recorder.eventTypes()["verification_error"] == 0 {
		t.Fatalf("missing explicit verifier-reference error: %+v", recorder.eventTypes())
	}
}

func TestInferenceBudgetRejectsOverBudgetResponseBeforeToolExecution(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage harness.Usage
		set   func(*harness.Budget)
		want  string
	}{
		{
			name: "tokens", usage: harness.Usage{TotalTokens: 101, CostUSD: 0.001, CostAvailable: true},
			set: func(b *harness.Budget) { b.MaxTokens = 100 }, want: "token budget exceeded",
		},
		{
			name: "cost", usage: harness.Usage{TotalTokens: 10, CostUSD: 0.02, CostAvailable: true},
			set: func(b *harness.Budget) { b.MaxCostUSD = 0.01 }, want: "cost budget exceeded",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executed := 0
			provider := harness.NewMock("test", "test-model", func(context.Context, *harness.ModelRequest) (*harness.ModelResponse, error) {
				return &harness.ModelResponse{
					ToolCalls: []harness.ToolCall{{ID: "call-echo", Name: "echo", Arguments: `{}`}},
					Usage:     tc.usage,
				}, nil
			})
			echo := harness.Tool{
				Name: "echo", Description: "counts executions",
				Handler: func(context.Context, *harness.ToolContext, json.RawMessage) (*harness.ToolResult, error) {
					executed++
					return &harness.ToolResult{Content: "ok"}, nil
				},
			}
			budget := testBudget()
			tc.set(&budget)
			recorder := &captureRecorder{}
			result := runTestHarness(t, context.Background(), provider, &stubDomain{
				tools: []harness.Tool{echo}, evaluation: harness.Evaluation{Success: true, Score: 1},
			}, budget, recorder)
			if result.Success || !strings.Contains(result.Error, tc.want) {
				t.Fatalf("expected failed run with %q, got %+v", tc.want, result)
			}
			if executed != 0 {
				t.Fatalf("over-budget tool call executed %d times", executed)
			}
			if recorder.eventTypes()["budget_exhausted"] != 1 {
				t.Fatalf("expected budget event: %+v", recorder.eventTypes())
			}
		})
	}
}

func TestRunBudgetTimeoutIsEnforced(t *testing.T) {
	provider := harness.NewMock("test", "test-model", func(ctx context.Context, _ *harness.ModelRequest) (*harness.ModelResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	budget := testBudget()
	budget.TimeoutMS = 100
	started := time.Now()
	result := runTestHarness(t, context.Background(), provider, &stubDomain{
		evaluation: harness.Evaluation{Success: true, Score: 1},
	}, budget, &captureRecorder{})
	elapsed := time.Since(started)
	if elapsed > 2*time.Second {
		t.Fatalf("run budget timeout was not enforced; elapsed=%s", elapsed)
	}
	if result.Success {
		t.Fatal("a timed-out run must not be marked successful")
	}
	if result.Error == "" {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(strings.ToLower(result.Error), "deadline") && !strings.Contains(strings.ToLower(result.Error), "cancel") {
		t.Fatalf("unexpected timeout error: %q", result.Error)
	}
}
