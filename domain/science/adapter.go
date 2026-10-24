// Package science implements PARADOX's first domain adapter: executable
// computational experiments. The adapter supplies tools, verification plans,
// and evaluation; the general harness never imports scientific concepts.
package science

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/qeinstein/paradox/evidence"
	"github.com/qeinstein/paradox/harness"
)

const (
	DomainName    = "science"
	TaskPrimality = "science-primality-v1"
)

type Adapter struct {
}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) Name() string { return DomainName }

func (a *Adapter) Init(ctx context.Context, task harness.Task) (*harness.DomainContext, error) {
	if task.Domain != "" && task.Domain != DomainName {
		return nil, fmt.Errorf("science adapter cannot run domain %q", task.Domain)
	}
	expression := "n*n+n+41"
	if value, ok := task.Metadata["expression"].(string); ok && strings.TrimSpace(value) != "" {
		expression = strings.TrimSpace(value)
	}
	instructions := scienceInstructions(task.ID, expression)
	return &harness.DomainContext{
		Instructions: instructions,
		Metadata:     map[string]any{"expression": expression},
	}, nil
}

func (a *Adapter) Tools() []harness.Tool {
	return []harness.Tool{primalityScanTool()}
}

func (a *Adapter) ToolsForTask(task harness.Task) []harness.Tool {
	if task.ID == "" || task.ID == TaskPrimality {
		expression := "n*n+n+41"
		if value, ok := task.Metadata["expression"].(string); ok && strings.TrimSpace(value) != "" {
			expression = strings.TrimSpace(value)
		}
		return []harness.Tool{primalityScanToolWithDefault(expression)}
	}
	return []harness.Tool{taskExperimentTool(task.ID)}
}

func (a *Adapter) VerificationPlans(ctx context.Context, claim evidence.Node, snapshot evidence.Snapshot, policy harness.VerificationPolicy) ([]harness.VerificationPlan, error) {
	if !policy.Enabled || claim.Kind != evidence.KindClaim || !claim.Active || claim.Verification == evidence.Verified {
		return nil, nil
	}
	assumptions := map[string]evidence.Node{}
	for _, node := range snapshot.Nodes {
		assumptions[node.ID] = node
	}
	plans := []harness.VerificationPlan{}
	for _, assumptionID := range claim.AssumptionIDs {
		assumption, ok := assumptions[assumptionID]
		if !ok || assumption.Kind != evidence.KindAssumption || !assumption.Active || assumption.Verification == evidence.Verified {
			continue
		}
		if policy.MinAssumptionConfidence > 0 && assumption.Confidence > policy.MinAssumptionConfidence {
			continue
		}
		metadata := assumption.Metadata
		if metadata == nil {
			continue
		}
		kind, _ := metadata["assumption_kind"].(string)
		if kind == "" {
			kind, _ = metadata["kind"].(string)
		}
		if kind == "measurement_stability" && aTaskSupportsMeasurement(claim, snapshot) {
			plans = append(plans, harness.VerificationPlan{
				ID:      "measurement-repeat-" + claim.ID + "-" + assumptionID,
				ClaimID: claim.ID, AssumptionID: assumptionID,
				Rationale: "Check the stationarity assumption with the independent replication batch.",
				Tool:      "run_experiment", Args: map[string]any{
					"experiment": "collect_measurements", "dataset": "sensor-stability-v1", "batch": "replication",
				},
				Check: "measurement_stability", Metadata: map[string]any{"pilot_batch": "pilot", "tolerance": 0.5},
			})
			continue
		}
		if kind != "range_representativeness" {
			continue
		}
		testedStart, ok1 := number(metadata["tested_start"])
		testedEnd, ok2 := number(metadata["tested_end"])
		claimedStart, ok3 := number(metadata["claimed_start"])
		claimedEnd, ok4 := number(metadata["claimed_end"])
		if !ok1 || !ok2 || !ok3 || !ok4 {
			continue
		}
		if claimedStart >= testedStart && claimedEnd <= testedEnd {
			continue
		}
		expression, _ := metadata["expression"].(string)
		if expression == "" {
			expression = "n*n+n+41"
		}
		plans = append(plans, harness.VerificationPlan{
			ID:      "vplan-" + claim.ID + "-" + assumptionID,
			ClaimID: claim.ID, AssumptionID: assumptionID,
			Rationale: fmt.Sprintf("The claim covers n=[%d,%d], but the assumption only records tests over n=[%d,%d]. Run an exhaustive scan of the claimed range to look for a counterexample.", claimedStart, claimedEnd, testedStart, testedEnd),
			Tool:      "run_experiment",
			Args: map[string]any{
				"experiment": "primality_scan", "expression": expression,
				"start": claimedStart, "end": claimedEnd,
			},
			Check: "range_exhaustive_counterexample",
			Metadata: map[string]any{
				"tested_start": testedStart, "tested_end": testedEnd,
				"claimed_start": claimedStart, "claimed_end": claimedEnd,
			},
		})
	}
	return plans, nil
}

func number(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	default:
		return 0, false
	}
}

func numberFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func (a *Adapter) EvaluateVerification(ctx context.Context, plan harness.VerificationPlan, result *harness.ToolResult, snapshot evidence.Snapshot) (*harness.VerificationOutcome, error) {
	if result == nil || result.Data == nil {
		return nil, fmt.Errorf("verification produced no data")
	}
	if plan.Check == "measurement_stability" {
		pilotMean := 20.0
		for _, node := range snapshot.Nodes {
			if node.Kind == evidence.KindObservation && node.ClaimKey == "measurement:sensor-stability-v1:pilot" {
				if value, ok := numberFloat(node.Metadata["mean"]); ok {
					pilotMean = value
				}
			}
		}
		replicationMean, ok := numberFloat(result.Data["mean"])
		if !ok {
			return nil, fmt.Errorf("replication result is missing a numeric mean")
		}
		if result.Data["batch"] != "replication" {
			return nil, fmt.Errorf("verification requires the replication batch")
		}
		difference := replicationMean - pilotMean
		return &harness.VerificationOutcome{
			Contradicted: difference > 0.5 || difference < -0.5,
			AssumptionID: plan.AssumptionID,
			Reason:       fmt.Sprintf("replication mean %.3f differs from pilot mean %.3f by %.3f (tolerance 0.5)", replicationMean, pilotMean, difference),
			Method:       plan.Check,
		}, nil
	}
	if plan.Check != "range_exhaustive_counterexample" {
		return &harness.VerificationOutcome{Reason: "unknown verification check"}, nil
	}
	composites, _ := number(result.Data["composite_count"])
	if composites == 0 {
		return &harness.VerificationOutcome{
			Contradicted: false, AssumptionID: plan.AssumptionID,
			Reason: "the full claimed range was scanned and no counterexample was found",
			Method: plan.Check,
		}, nil
	}
	counterexample := result.Data["counterexample"]
	reason := fmt.Sprintf("the claimed range contains %d composite value(s); first counterexample: %s", composites, formatCounterexample(counterexample))
	return &harness.VerificationOutcome{
		Contradicted: true, AssumptionID: plan.AssumptionID,
		Reason: reason, Method: plan.Check,
	}, nil
}

func (a *Adapter) Evaluate(ctx context.Context, in harness.EvaluationInput) (*harness.Evaluation, error) {
	switch in.Task.ID {
	case TaskPermutationTest, TaskOptimization, TaskMeasurement:
		return evaluateResearchTask(in)
	default:
		return a.evaluatePrimality(ctx, in)
	}
}

func (a *Adapter) evaluatePrimality(ctx context.Context, in harness.EvaluationInput) (*harness.Evaluation, error) {
	spec := map[string]any{}
	if raw, ok := in.Task.Metadata["evaluation"].(map[string]any); ok {
		spec = raw
	}
	expectedN, _ := number(spec["expected_counterexample"])
	if expectedN == 0 {
		expectedN = 40
	}
	expectedKey, _ := spec["claim_key"].(string)
	if expectedKey == "" {
		expectedKey = "primality:poly:n^2+n+41"
	}
	fullEnd, _ := number(spec["full_range_end"])
	if fullEnd == 0 {
		fullEnd = 100
	}

	counterexampleObserved := false
	universalActive := false
	boundedActive := false
	counterexampleClaimActive := false
	for _, node := range in.Snapshot.Nodes {
		if node.Kind == evidence.KindObservation && node.ClaimValue == "composite" {
			if n, ok := number(node.Metadata["n"]); ok && n == expectedN {
				counterexampleObserved = true
			}
		}
		if node.Kind != evidence.KindClaim {
			continue
		}
		scopeStart, scopeEnd := claimScope(node)
		if node.ClaimKey == expectedKey && node.ClaimValue == "all_prime" {
			if node.Active && scopeEnd >= fullEnd {
				universalActive = true
			}
			if node.Active && scopeEnd < fullEnd && scopeEnd >= expectedN-1 {
				boundedActive = true
			}
		}
		if node.Active && node.ClaimValue == "composite" {
			if n, ok := number(node.Metadata["n"]); ok && n == expectedN {
				counterexampleClaimActive = true
			}
			if strings.Contains(node.Text, fmt.Sprintf("f(%d)", expectedN)) {
				counterexampleClaimActive = true
			}
		}
		_ = scopeStart
	}
	retractedUniversal := false
	for _, retraction := range in.Snapshot.Retractions {
		if strings.Contains(retraction.Reason, "counterexample") || retraction.Cause == "verification" || retraction.Cause == "contradiction" {
			retractedUniversal = true
		}
	}

	score := 0.0
	if in.Snapshot.Stats.Observations > 0 {
		score += 0.2
	}
	if counterexampleObserved {
		score += 0.25
	}
	if !universalActive && retractedUniversal {
		score += 0.3
	}
	if boundedActive {
		score += 0.15
	}
	if counterexampleClaimActive {
		score += 0.1
	}
	success := in.Completed && counterexampleObserved && !universalActive && boundedActive && retractedUniversal
	if success {
		score = 1.0
	}
	return &harness.Evaluation{
		Success: success, Score: score,
		Details: map[string]any{
			"expected_counterexample":      expectedN,
			"counterexample_observed":      counterexampleObserved,
			"universal_claim_active":       universalActive,
			"bounded_claim_active":         boundedActive,
			"counterexample_claim_active":  counterexampleClaimActive,
			"retracted_after_verification": retractedUniversal,
			"completed":                    in.Completed,
			"claim_key":                    expectedKey,
		},
	}, nil
}

func scienceInstructions(taskID, expression string) string {
	switch taskID {
	case TaskPermutationTest:
		return "Use the task-scoped exact permutation experiment. Treat its data and computed p-value as observations; your interpretation must cite its evidence ID. Do not invent samples or calculations."
	case TaskOptimization:
		return "Use the task-scoped exhaustive integer optimization experiment. Report the returned minimizer and objective value, and cite the experiment evidence in your claim."
	case TaskMeasurement:
		return "Use the pilot measurement experiment before concluding. Make measurement stability an explicit assumption when you infer it. The harness may run the independent replication batch to challenge that assumption; revise if it contradicts the pilot."
	default:
		return fmt.Sprintf(
			"You are investigating the integer polynomial f(n) = %s. Use run_experiment to execute a deterministic primality scan. Report the tested range, number of primes and composites, and any counterexample. Never state that a range was tested unless the tool result says so. Make any extrapolation assumption explicit.",
			expression,
		)
	}
}

func aTaskSupportsMeasurement(claim evidence.Node, snapshot evidence.Snapshot) bool {
	if claim.ClaimKey != "measurement:sensor-stability-v1" {
		return false
	}
	for _, node := range snapshot.Nodes {
		if node.Kind == evidence.KindObservation && node.ClaimKey == "measurement:sensor-stability-v1:pilot" {
			return true
		}
	}
	return false
}

func formatCounterexample(value any) string {
	counterexample, ok := value.(map[string]any)
	if !ok {
		return fmt.Sprint(value)
	}
	n, _ := number(counterexample["n"])
	composite, _ := number(counterexample["value"])
	factors := []string{}
	switch raw := counterexample["factors"].(type) {
	case []any:
		for _, item := range raw {
			if factor, ok := number(item); ok {
				factors = append(factors, fmt.Sprintf("%d", factor))
			}
		}
	case []int:
		for _, factor := range raw {
			factors = append(factors, fmt.Sprintf("%d", factor))
		}
	}
	if len(factors) == 0 {
		return fmt.Sprintf("f(%d)=%d", n, composite)
	}
	return fmt.Sprintf("f(%d)=%d=%s", n, composite, strings.Join(factors, "*"))
}

func claimScope(node evidence.Node) (int, int) {
	scope, _ := node.Metadata["scope"].(map[string]any)
	start, _ := number(scope["start"])
	end, _ := number(scope["end"])
	return start, end
}
