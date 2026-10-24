package app_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qeinstein/paradox/app"
	"github.com/qeinstein/paradox/domain/science"
	"github.com/qeinstein/paradox/harness"
	"github.com/qeinstein/paradox/provider"
	"github.com/qeinstein/paradox/runstore"
)

func TestOpenRouterCapabilitiesRequireAndPreserveExplicitConfiguration(t *testing.T) {
	model, err := app.BuildProvider(app.ProviderConfig{
		Name: "openrouter", Model: "vendor/model", APIKey: "fixture-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if model.Capabilities().ToolCalls || model.Capabilities().Reasoning || model.Capabilities().StructuredOutput {
		t.Fatalf("OpenRouter must not guess model capabilities: %+v", model.Capabilities())
	}
	model, err = app.BuildProvider(app.ProviderConfig{
		Name: "openrouter", Model: "vendor/model", APIKey: "fixture-key",
		OpenRouterToolCallsSupported: true, OpenRouterReasoningSupported: true,
		OpenRouterStructuredOutputSupported: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := model.Capabilities(); !got.ToolCalls || !got.Reasoning || !got.StructuredOutput {
		t.Fatalf("explicit OpenRouter capabilities were not retained: %+v", got)
	}
	if _, ok := model.(*provider.OpenAIProvider); !ok {
		t.Fatalf("expected OpenRouter HTTP adapter, got %T", model)
	}
}

func TestScienceVerticalSliceModes(t *testing.T) {
	task := app.DefaultScienceTask()
	for _, tc := range []struct {
		mode    harness.Mode
		success bool
	}{
		{harness.Baseline, false},
		{harness.EvidenceMode, false},
		{harness.Paradox, true},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			output, err := app.Run(ctx, app.RunOptions{
				Task: task, Mode: tc.mode, Provider: science.NewMockProvider(),
				Budget: harness.Budget{
					MaxSteps: 10, MaxToolCalls: 20, MaxTokens: 20000,
					MaxCostUSD: 1, TimeoutMS: 30000, MaxRetries: 0,
					ToolTimeoutMS: 30000,
				},
				RunsDir: t.TempDir(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if output.Result.Success != tc.success {
				t.Fatalf("mode %s success=%v, want %v; evaluation=%+v error=%q", tc.mode, output.Result.Success, tc.success, output.Result.Evaluation, output.Result.Error)
			}
			if tc.mode == harness.Paradox {
				if output.Result.Snapshot.Stats.Contradictions == 0 {
					t.Fatal("expected a contradiction in paradox mode")
				}
				if output.Result.Snapshot.Stats.Retractions == 0 {
					t.Fatal("expected retractions in paradox mode")
				}
				if output.Result.Snapshot.Stats.ActiveClaims == 0 {
					t.Fatal("expected revised active claims")
				}
				foundReplan := false
				for _, event := range output.Result.Snapshot.Retractions {
					if event.Cause == "verification" || event.Cause == "contradiction" {
						foundReplan = true
					}
				}
				if !foundReplan {
					t.Fatal("expected verification-driven retraction")
				}
			}
			record, err := runstore.New(output.Store.Root())
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := record.Load(output.Result.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Result == nil || loaded.Evidence == nil {
				t.Fatal("expected persisted result and evidence")
			}
			if _, err := os.Stat(filepath.Join(output.RunDir, "events.jsonl")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBudgetEnforcementStopsRun(t *testing.T) {
	ctx := context.Background()
	output, err := app.Run(ctx, app.RunOptions{
		Task: app.DefaultScienceTask(), Mode: harness.Paradox, Provider: science.NewMockProvider(),
		Budget: harness.Budget{
			MaxSteps: 1, MaxToolCalls: 20, MaxTokens: 20000,
			MaxCostUSD: 1, TimeoutMS: 30000, MaxRetries: 0, ToolTimeoutMS: 30000,
		},
		RunsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Result.Error == "" {
		t.Fatal("expected a step-budget error")
	}
	if output.Result.Success {
		t.Fatal("a budget-exhausted run should not succeed")
	}
}
