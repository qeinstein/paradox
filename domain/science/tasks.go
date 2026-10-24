package science

import (
	"fmt"

	"github.com/qeinstein/paradox/evidence"
	"github.com/qeinstein/paradox/harness"
)

const (
	TaskPermutationTest = "science-permutation-test-v1"
	TaskOptimization    = "science-optimization-v1"
	TaskMeasurement     = "science-measurement-revision-v1"
)

// Tasks returns the reproducible development tasks. Reference metadata is
// consumed only by Evaluate; the runner sends Prompt and domain instructions
// to the model, never Task.Metadata.
func Tasks() []harness.Task {
	return []harness.Task{
		{
			ID: TaskPermutationTest, Domain: DomainName,
			Prompt: "For a two-sided exact permutation test, compare control samples [1,2,3,4,5] with treatment samples [7,8,9,10,11] at alpha=0.05. " +
				"Run the exact_permutation_test experiment for dataset group-difference-v1. Report the sample means, exact p-value, and whether these data support a difference. " +
				"Record a claim backed by the experiment evidence.",
			Metadata: map[string]any{"evaluation": map[string]any{"claim_key": "stats:group-difference-v1", "claim_value": "significant_difference"}},
		},
		{
			ID: TaskOptimization, Domain: DomainName,
			Prompt: "Find the integer x in [-10,10] minimizing f(x)=3x^2-24x+11. Run the integer_optimization experiment for benchmark-function-1. " +
				"Report the minimizing x and the minimum value, then record a claim backed by the experiment evidence.",
			Metadata: map[string]any{"evaluation": map[string]any{"claim_key": "optimization:benchmark-function-1", "x": 4, "value": -37}},
		},
		{
			ID: TaskMeasurement, Domain: DomainName,
			Prompt: "Investigate whether the sensor stays calibrated around 20 degrees across repeated runs. First use the pilot batch from sensor-stability-v1. " +
				"Record a provisional stability claim and make the assumption that pilot measurements represent later runs explicit. " +
				"If new evidence contradicts that assumption, revise the claim and report the measured drift.",
			Metadata: map[string]any{"evaluation": map[string]any{"claim_key": "measurement:sensor-stability-v1", "expected_value": "drift_detected"}},
		},
	}
}

func evaluateResearchTask(in harness.EvaluationInput) (*harness.Evaluation, error) {
	switch in.Task.ID {
	case TaskPermutationTest:
		return evaluatePermutationTask(in), nil
	case TaskOptimization:
		return evaluateOptimizationTask(in), nil
	case TaskMeasurement:
		return evaluateMeasurementTask(in), nil
	default:
		return nil, fmt.Errorf("unsupported research evaluation task %q", in.Task.ID)
	}
}

func directObservationIndex(snapshot evidence.Snapshot) map[string]evidence.Node {
	index := map[string]evidence.Node{}
	for _, node := range snapshot.Nodes {
		if node.Kind == evidence.KindObservation {
			index[node.ID] = node
		}
	}
	return index
}

func activeSupportedClaim(snapshot evidence.Snapshot, claimKey, claimValue string) (evidence.Node, bool) {
	observations := directObservationIndex(snapshot)
	for _, claim := range snapshot.Nodes {
		if claim.Kind != evidence.KindClaim || !claim.Active || claim.ClaimKey != claimKey || claim.ClaimValue != claimValue {
			continue
		}
		for _, id := range claim.EvidenceIDs {
			if observation, ok := observations[id]; ok && observation.Active {
				return claim, true
			}
		}
	}
	return evidence.Node{}, false
}

func evaluatePermutationTask(in harness.EvaluationInput) *harness.Evaluation {
	_, correct := activeSupportedClaim(in.Snapshot, "stats:group-difference-v1", "significant_difference")
	observationMatches := false
	for _, observation := range directObservationIndex(in.Snapshot) {
		if observation.ClaimKey == "stats:group-difference-v1" && observation.ClaimValue == "significant_difference" {
			p, pOK := numberFloat(observation.Metadata["p_value"])
			permutations, nOK := number(observation.Metadata["permutations"])
			// The fixture has two extreme assignments among 252 possible group labels.
			observationMatches = pOK && nOK && permutations == 252 && mathAbs(p-2.0/252.0) < 1e-12 && p <= 0.05
		}
	}
	success := in.Completed && correct && observationMatches
	score := 0.0
	if observationMatches {
		score += 0.5
	}
	if correct {
		score += 0.5
	}
	return &harness.Evaluation{Success: success, Score: score, Details: map[string]any{
		"reference_method":              "exact two-sided permutation test; 2/252 assignments at least as extreme",
		"observation_matches_reference": observationMatches,
		"correct_active_claim":          correct,
	}}
}

func evaluateOptimizationTask(in harness.EvaluationInput) *harness.Evaluation {
	// The scorer uses the analytic vertex and evaluates the objective there;
	// the experiment tool independently searches every integer in the interval.
	x := 4
	value := 3*x*x - 24*x + 11
	expected := fmt.Sprintf("x=%d;value=%d", x, value)
	claim, claimOK := activeSupportedClaim(in.Snapshot, "optimization:benchmark-function-1", expected)
	measurementOK := false
	for _, observation := range directObservationIndex(in.Snapshot) {
		if observation.ClaimKey != "optimization:benchmark-function-1" {
			continue
		}
		modelX, xOK := number(observation.Metadata["minimizer"])
		modelValue, yOK := numberFloat(observation.Metadata["minimum"])
		evaluated, evaluatedOK := number(observation.Metadata["evaluated"])
		measurementOK = xOK && yOK && evaluatedOK && modelX == x && modelValue == float64(value) && evaluated == 21
	}
	success := in.Completed && claimOK && measurementOK
	score := 0.0
	if measurementOK {
		score += 0.5
	}
	if claimOK {
		score += 0.5
	}
	_ = claim
	return &harness.Evaluation{Success: success, Score: score, Details: map[string]any{
		"reference_minimizer": x, "reference_minimum": value,
		"observation_matches_reference": measurementOK, "correct_active_claim": claimOK,
	}}
}

func evaluateMeasurementTask(in harness.EvaluationInput) *harness.Evaluation {
	observations := directObservationIndex(in.Snapshot)
	replicationPresent := false
	for _, observation := range observations {
		if observation.ClaimKey == "measurement:sensor-stability-v1:replication" {
			meanValue, ok := number(observation.Metadata["mean"])
			replicationPresent = ok && meanValue == 23
		}
	}
	_, driftOK := activeSupportedClaim(in.Snapshot, "measurement:sensor-stability-v1", "drift_detected")
	stabilityClaimActive := false
	for _, claim := range in.Snapshot.Nodes {
		if claim.Kind == evidence.KindClaim && claim.Active && claim.ClaimKey == "measurement:sensor-stability-v1" && claim.ClaimValue == "stable" {
			stabilityClaimActive = true
		}
	}
	success := in.Completed && replicationPresent && driftOK && !stabilityClaimActive
	score := 0.0
	if replicationPresent {
		score += 0.5
	}
	if driftOK && !stabilityClaimActive {
		score += 0.5
	}
	return &harness.Evaluation{Success: success, Score: score, Details: map[string]any{
		"replication_matches_reference":      replicationPresent,
		"correct_active_drift_claim":         driftOK,
		"unsupported_stability_claim_active": stabilityClaimActive,
	}}
}

func mathAbs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
