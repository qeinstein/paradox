package eval_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qeinstein/paradox/domain/science"
	"github.com/qeinstein/paradox/eval"
	"github.com/qeinstein/paradox/harness"
)

func TestMatchedScienceComparison(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	report, err := eval.Run(ctx, eval.Options{
		Provider: science.NewMockProvider(),
		Budget: harness.Budget{
			MaxSteps: 12, MaxToolCalls: 30, MaxTokens: 30000,
			MaxCostUSD: 1, TimeoutMS: 30000, MaxRetries: 0, ToolTimeoutMS: 30000,
		},
		Trials:    1,
		RunsDir:   filepath.Join(root, "runs"),
		OutputDir: filepath.Join(root, "evals"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Overall.Runs != 3 {
		t.Fatalf("expected three matched runs, got %d", report.Overall.Runs)
	}
	if report.ByMode["baseline"].Successes != 0 {
		t.Fatalf("baseline should not pass the science task: %+v", report.ByMode["baseline"])
	}
	if report.ByMode["evidence"].Successes != 0 {
		t.Fatalf("evidence mode should not pass the science task: %+v", report.ByMode["evidence"])
	}
	paradox := report.ByMode["paradox"]
	if paradox.Successes != 1 || paradox.MeanScore != 1 {
		t.Fatalf("paradox should pass: %+v", paradox)
	}
	if paradox.Contradictions == 0 || paradox.Retractions == 0 {
		t.Fatalf("expected contradiction and retraction metrics: %+v", paradox)
	}
	if paradox.VerificationAccuracy != 1 {
		t.Fatalf("expected verification accuracy 1, got %+v", paradox)
	}
	if report.Overall.UnsupportedClaims == 0 {
		t.Fatal("expected baseline/evidence unsupported claims to be counted")
	}
	if len(report.Paired) != 4 {
		t.Fatalf("expected aggregate and task-level paired comparisons for evidence and paradox, got %+v", report.Paired)
	}
	for _, paired := range report.Paired {
		if paired.Pairs != 1 || paired.UncertaintyAvailable {
			t.Fatalf("single-trial comparisons should be descriptive without uncertainty: %+v", paired)
		}
	}
	if report.Files["json"] == "" || report.Files["md"] == "" {
		t.Fatal("expected report paths")
	}
	for _, path := range []string{report.Files["json"], report.Files["md"]} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing report file %s: %v", path, err)
		}
	}
	markdown, err := os.ReadFile(report.Files["md"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markdown), "Incorrect retraction rate") {
		t.Fatal("markdown report is missing metric definitions")
	}
	var persisted eval.Report
	data, err := os.ReadFile(report.Files["json"])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.EvalID != report.EvalID {
		t.Fatalf("persisted report mismatch: %+v", persisted)
	}
}

func TestReproducibilityRequiresRepeatedTrials(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	report, err := eval.Run(ctx, eval.Options{
		Provider: science.NewMockProvider(),
		Modes:    []harness.Mode{harness.Paradox},
		Trials:   2,
		Budget: harness.Budget{
			MaxSteps: 12, MaxToolCalls: 30, MaxTokens: 30000,
			MaxCostUSD: 1, TimeoutMS: 30000, MaxRetries: 0, ToolTimeoutMS: 30000,
		},
		RunsDir:   filepath.Join(root, "runs"),
		OutputDir: filepath.Join(root, "evals"),
	})
	if err != nil {
		t.Fatal(err)
	}
	aggregate := report.ByMode["paradox"]
	if aggregate.ReproducibilityRuns != 2 {
		t.Fatalf("expected reproducibility over two runs: %+v", aggregate)
	}
	if aggregate.Reproducibility != 1 {
		t.Fatalf("deterministic mock should reproduce, got %f", aggregate.Reproducibility)
	}
}
