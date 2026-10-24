package science

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/qeinstein/paradox/evidence"
	"github.com/qeinstein/paradox/harness"
)

func TestDevelopmentScienceTasksRunThroughHarness(t *testing.T) {
	for _, task := range Tasks() {
		t.Run(task.ID, func(t *testing.T) {
			provider := harness.NewMock("fixture", "fixture-v1", func(ctx context.Context, req *harness.ModelRequest) (*harness.ModelResponse, error) {
				return researchStep(task.ID, req)
			})
			result, err := harness.Run(context.Background(), harness.RunConfig{
				RunID: "science-task-test", Task: task, Mode: harness.Paradox,
				Provider: provider, Domain: New(), WorkDir: t.TempDir(),
				ArtifactsDir: t.TempDir(), Budget: harness.DefaultBudget(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Success {
				t.Fatalf("task failed: error=%q evaluation=%+v nodes=%+v", result.Error, result.Evaluation, result.Snapshot.Nodes)
			}
			if result.Usage.ToolCalls == 0 || result.Snapshot.Stats.Observations == 0 {
				t.Fatalf("expected real tool execution and recorded evidence: %+v", result)
			}
		})
	}
}

func TestScienceEvaluatorRejectsFormattedFalseStatisticalClaim(t *testing.T) {
	task := Tasks()[0]
	provider := harness.NewMock("fixture", "fixture-v1", func(ctx context.Context, req *harness.ModelRequest) (*harness.ModelResponse, error) {
		return researchStepWithWrongClaim(task.ID, req)
	})
	result, err := harness.Run(context.Background(), harness.RunConfig{
		RunID: "science-false-claim", Task: task, Mode: harness.EvidenceMode,
		Provider: provider, Domain: New(), WorkDir: t.TempDir(), ArtifactsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Evaluation == nil || result.Evaluation.Success || result.Score >= 1 {
		t.Fatalf("false but well-formed claim passed evaluation: %+v", result.Evaluation)
	}
}

func TestResolvedMeasurementAssumptionIsNotChallengedAgain(t *testing.T) {
	adapter := New()
	kernel := evidence.New("resolved-assumption")
	observation, err := kernel.AddObservation(evidence.ObservationInput{
		Text: "pilot mean is 20", ClaimKey: "measurement:sensor-stability-v1:pilot",
		ClaimValue: "mean=20.0", Source: evidence.SourceExperiment,
		Metadata: map[string]any{"dataset": "sensor-stability-v1", "batch": "pilot", "mean": 20.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := kernel.SubmitClaim(evidence.ClaimInput{
		Text: "sensor is provisionally stable", ClaimKey: "measurement:sensor-stability-v1",
		ClaimValue: "stable", EvidenceIDs: []string{observation.ID},
		Assumptions: []evidence.AssumptionInput{{
			Text: "pilot measurements represent later runs", Kind: "measurement_stability", Confidence: 0.75,
			Metadata: map[string]any{"assumption_kind": "measurement_stability"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := harness.DefaultVerificationPolicy()
	plans, err := adapter.VerificationPlans(context.Background(), claim.Claim, kernel.Snapshot(), policy)
	if err != nil || len(plans) != 1 {
		t.Fatalf("expected one initial falsification plan: plans=%+v err=%v", plans, err)
	}
	if err := kernel.SetVerification(claim.Assumptions[0].ID, evidence.Verified, "measurement_stability"); err != nil {
		t.Fatal(err)
	}
	plans, err = adapter.VerificationPlans(context.Background(), claim.Claim, kernel.Snapshot(), policy)
	if err != nil || len(plans) != 0 {
		t.Fatalf("resolved assumption must not be challenged repeatedly: plans=%+v err=%v", plans, err)
	}
}

func researchStep(taskID string, req *harness.ModelRequest) (*harness.ModelResponse, error) {
	return researchStepValue(taskID, req, false)
}

func researchStepWithWrongClaim(taskID string, req *harness.ModelRequest) (*harness.ModelResponse, error) {
	return researchStepValue(taskID, req, true)
}

func researchStepValue(taskID string, req *harness.ModelRequest, wrong bool) (*harness.ModelResponse, error) {
	observations := map[string]string{}
	claims := map[string]bool{}
	replicationPresent := false
	invalidated := false
	for _, message := range req.Messages {
		if message.Role == harness.RoleUser && strings.Contains(message.Content, "INVALIDATED") {
			invalidated = true
			fields := strings.Fields(message.Content)
			for index, field := range fields {
				if field == "evidence" && index+1 < len(fields) {
					observations["replication"] = strings.Trim(fields[index+1], ".,;:)")
					replicationPresent = observations["replication"] != ""
				}
			}
		}
		if message.Role == harness.RoleTool {
			var payload struct {
				Tool        string         `json:"tool"`
				Result      map[string]any `json:"result"`
				EvidenceIDs []string       `json:"evidence_ids"`
			}
			if json.Unmarshal([]byte(message.Content), &payload) == nil && payload.Tool == "run_experiment" && len(payload.EvidenceIDs) > 0 {
				batch, _ := payload.Result["batch"].(string)
				if batch == "" {
					batch = taskID
				}
				observations[batch] = payload.EvidenceIDs[0]
				if batch == "replication" {
					replicationPresent = true
				}
			}
		}
		if message.Role == harness.RoleAssistant {
			for _, call := range message.ToolCalls {
				if call.Name != "submit_claim" {
					continue
				}
				var input map[string]any
				if json.Unmarshal([]byte(call.Arguments), &input) == nil {
					if key, _ := input["claim_key"].(string); key != "" {
						claims[key+":"+fmt.Sprint(input["claim_value"])] = true
					}
				}
			}
		}
	}

	if taskID == TaskMeasurement && observations["taskID"] != "" {
		// The pilot's batch is present; keep its evidence under the name used below.
		observations["pilot"] = observations["taskID"]
	}
	if taskID == TaskMeasurement && observations["pilot"] == "" {
		return toolResponse("run_experiment", `{"experiment":"collect_measurements","dataset":"sensor-stability-v1","batch":"pilot"}`), nil
	}
	if taskID != TaskMeasurement && observations[taskID] == "" {
		args := map[string]string{
			TaskPermutationTest: `{"experiment":"exact_permutation_test","dataset":"group-difference-v1"}`,
			TaskOptimization:    `{"experiment":"integer_optimization","dataset":"benchmark-function-1"}`,
		}[taskID]
		return toolResponse("run_experiment", args), nil
	}

	claimKey, claimValue, evidenceID := "", "", observations[taskID]
	switch taskID {
	case TaskPermutationTest:
		claimKey, claimValue = "stats:group-difference-v1", "significant_difference"
		if wrong {
			claimValue = "no_significant_difference"
		}
	case TaskOptimization:
		claimKey, claimValue = "optimization:benchmark-function-1", "x=4;value=-37"
	case TaskMeasurement:
		claimKey = "measurement:sensor-stability-v1"
		if !claims[claimKey+":stable"] {
			claimValue, evidenceID = "stable", observations["pilot"]
			return claimToolResponse(claimKey, claimValue, evidenceID, true), nil
		}
		if !replicationPresent && !invalidated {
			return toolResponse("run_experiment", `{"experiment":"collect_measurements","dataset":"sensor-stability-v1","batch":"replication"}`), nil
		}
		claimValue, evidenceID = "drift_detected", observations["replication"]
	}
	if !claims[claimKey+":"+claimValue] {
		return claimToolResponse(claimKey, claimValue, evidenceID, false), nil
	}
	if taskID == TaskMeasurement && !replicationPresent && !invalidated {
		return toolResponse("run_experiment", `{"experiment":"collect_measurements","dataset":"sensor-stability-v1","batch":"replication"}`), nil
	}
	return toolResponse("finish", `{"conclusion":"The experimental evidence supports the recorded conclusion."}`), nil
}

func toolResponse(name, arguments string) *harness.ModelResponse {
	return &harness.ModelResponse{ToolCalls: []harness.ToolCall{{ID: "call-" + name, Name: name, Arguments: arguments}}, FinishReason: "tool_calls"}
}

func claimToolResponse(key, value, evidenceID string, stabilityAssumption bool) *harness.ModelResponse {
	input := map[string]any{
		"text": "Experiment-backed claim: " + value, "claim_key": key,
		"claim_value": value, "confidence": 0.9,
		"evidence_ids": []string{evidenceID},
	}
	if stabilityAssumption {
		input["assumptions"] = []map[string]any{{
			"text": "The pilot batch represents later measurements.",
			"kind": "measurement_stability", "confidence": 0.7,
			"metadata": map[string]any{"assumption_kind": "measurement_stability"},
		}}
	}
	encoded, _ := json.Marshal(input)
	return toolResponse("submit_claim", string(encoded))
}
