package scienceworld

import (
	"context"
	"testing"
	"time"

	"github.com/qeinstein/paradox/evidence"
	"github.com/qeinstein/paradox/harness"
)

func TestNewTaskRecordsOfficialTaskVariation(t *testing.T) {
	task := NewTask("boil", 4, "", 77)
	if task.Domain != DomainName || task.ID != "scienceworld:boil:variation:4" {
		t.Fatalf("unexpected benchmark task identity: %+v", task)
	}
	if task.Metadata["task_name"] != "boil" || task.Metadata["variation"] != 4 || task.Metadata["step_limit"] != 77 {
		t.Fatalf("official variation or resource limit missing: %+v", task.Metadata)
	}
}

func TestOfficialScienceWorldEnvironmentSmokeWhenInstalled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dependencies := CheckDependencies(ctx)
	if err := dependencies.Ready(); err != nil {
		t.Skipf("official environment smoke test blocked: %v", err)
	}
	stepCalled := false
	model := harness.NewMock("scienceworld-smoke", "fixture-v1", func(_ context.Context, _ *harness.ModelRequest) (*harness.ModelResponse, error) {
		if !stepCalled {
			stepCalled = true
			return &harness.ModelResponse{ToolCalls: []harness.ToolCall{{
				ID: "step-1", Name: "scienceworld_step", Arguments: `{"action":"look around"}`,
			}}, FinishReason: "tool_calls"}, nil
		}
		return &harness.ModelResponse{ToolCalls: []harness.ToolCall{{
			ID: "finish-1", Name: "finish", Arguments: `{"conclusion":"The official environment returned a real observation."}`,
		}}, FinishReason: "tool_calls"}, nil
	})
	task := NewTask("boil", 0, "", 2)
	workDir := t.TempDir()
	result, err := harness.Run(ctx, harness.RunConfig{
		RunID: "scienceworld-official-smoke", Task: task, Mode: harness.Baseline,
		Provider: model, Domain: New(), WorkDir: workDir,
		ArtifactsDir: workDir + "/artifacts",
		Budget:       harness.Budget{MaxSteps: 3, MaxToolCalls: 4, MaxTokens: 1000, MaxCostUSD: 1, TimeoutMS: 90000, ToolTimeoutMS: 30000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" || result.Usage.ToolCalls != 2 || result.Snapshot.Stats.Observations != 1 {
		t.Fatalf("official action did not execute through the harness: %+v", result)
	}
	observation := result.Snapshot.Nodes[0]
	if observation.Source != evidence.SourceTool || observation.Metadata["official_score"] == nil || observation.Metadata["completed"] != false {
		t.Fatalf("expected real, incomplete official episode evidence: %+v", observation)
	}
	if result.Success || result.Score != 0 {
		t.Fatalf("incomplete episode must use the official zero score and remain unsuccessful: %+v", result)
	}
}

func TestScienceWorldToolRejectsInvalidActionShape(t *testing.T) {
	adapter := New()
	tools := adapter.ToolsForTask(harness.Task{ID: "scienceworld:boil:variation:0"})
	if len(tools) != 1 || tools[0].Name != "scienceworld_step" {
		t.Fatalf("ScienceWorld action tool unavailable: %+v", tools)
	}
	if _, err := tools[0].Handler(nil, nil, []byte(`{"action":"x\ny"}`)); err == nil {
		// An inactive adapter returns its state error before a process action is attempted.
		t.Fatal("expected inactive episode error")
	}
}
