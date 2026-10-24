package harness_test

import (
	"context"
	"encoding/json"
	"errors"
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
}

func (d *stubDomain) Name() string { return "test" }
func (d *stubDomain) Init(context.Context, harness.Task) (*harness.DomainContext, error) {
	return &harness.DomainContext{Instructions: "test instructions"}, nil
}
func (d *stubDomain) Tools() []harness.Tool { return d.tools }
func (d *stubDomain) VerificationPlans(context.Context, evidence.Node, evidence.Snapshot, harness.VerificationPolicy) ([]harness.VerificationPlan, error) {
	return nil, nil
}
func (d *stubDomain) EvaluateVerification(context.Context, harness.VerificationPlan, *harness.ToolResult, evidence.Snapshot) (*harness.VerificationOutcome, error) {
	return &harness.VerificationOutcome{}, nil
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
