// Package software is a deliberately small transfer adapter. It executes a
// fixed, deterministic test suite against two permitted patch candidates and
// records real test outcomes as evidence; it does not execute model code.
package software

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/qeinstein/paradox/evidence"
	"github.com/qeinstein/paradox/harness"
)

const (
	DomainName = "software"
	TaskID     = "software-boundary-regression-v1"
)

type Adapter struct{}

func New() *Adapter             { return &Adapter{} }
func (a *Adapter) Name() string { return DomainName }

func Task() harness.Task {
	return harness.Task{
		ID: TaskID, Domain: DomainName,
		Prompt: "A range guard should accept integers from 0 through 10 inclusive and reject values outside that range. " +
			"Test candidate-a first, inspect the regression result, then revise the change and test candidate-b if needed. " +
			"Record the verified implementation claim using the test evidence.",
	}
}

func (a *Adapter) Init(_ context.Context, task harness.Task) (*harness.DomainContext, error) {
	if task.Domain != "" && task.Domain != DomainName {
		return nil, fmt.Errorf("software adapter cannot run domain %q", task.Domain)
	}
	return &harness.DomainContext{Instructions: "Only the two enumerated patch candidates are available. `run_test_suite` executes deterministic boundary and regression tests; cite its evidence in any implementation claim."}, nil
}

func (a *Adapter) Tools() []harness.Tool {
	return []harness.Tool{{
		Name: "run_test_suite", Description: "Execute the deterministic test suite against one permitted patch candidate. Candidate A contains a boundary defect; candidate B is the revised patch.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"candidate":{"type":"string","enum":["candidate-a","candidate-b"]}},"required":["candidate"]}`),
		Handler:    runTests,
	}}
}

func runTests(_ context.Context, _ *harness.ToolContext, raw json.RawMessage) (*harness.ToolResult, error) {
	var args struct {
		Candidate string `json:"candidate"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("invalid test-suite arguments: %w", err)
	}
	if args.Candidate != "candidate-a" && args.Candidate != "candidate-b" {
		return nil, errors.New("candidate is not in the permitted patch set")
	}
	tests := []struct {
		name  string
		input int
		want  bool
	}{
		{name: "lower-bound", input: 0, want: true},
		{name: "interior", input: 7, want: true},
		{name: "upper-bound-regression", input: 10, want: true},
		{name: "below-range", input: -1, want: false},
		{name: "above-range", input: 11, want: false},
	}
	results := make([]map[string]any, 0, len(tests))
	passed := true
	for _, test := range tests {
		got := candidateAllows(args.Candidate, test.input)
		ok := got == test.want
		if !ok {
			passed = false
		}
		results = append(results, map[string]any{
			"name": test.name, "input": test.input,
			"expected": test.want, "actual": got, "passed": ok,
		})
	}
	claimValue := "regression_failed"
	if passed {
		claimValue = "all_tests_passed"
	}
	data := map[string]any{"candidate": args.Candidate, "passed": passed, "tests": results}
	text := fmt.Sprintf("test suite for %s: %d/%d passed", args.Candidate, countPassing(results), len(tests))
	return &harness.ToolResult{
		Content: text, Data: data,
		Evidence: []evidence.ObservationInput{{
			Text: text, ClaimKey: "patch:" + args.Candidate, ClaimValue: claimValue,
			Subject: args.Candidate, Predicate: "passes_regression_suite", Object: fmt.Sprint(passed),
			Source:     evidence.SourceTool,
			Provenance: map[string]any{"tool": "run_test_suite", "test_suite": TaskID},
			Metadata:   data,
		}},
	}, nil
}

func candidateAllows(candidate string, input int) bool {
	switch candidate {
	case "candidate-a":
		return input >= 0 && input < 10
	case "candidate-b":
		return input >= 0 && input <= 10
	default:
		return false
	}
}

func countPassing(results []map[string]any) int {
	passed := 0
	for _, result := range results {
		if result["passed"] == true {
			passed++
		}
	}
	return passed
}

func (a *Adapter) VerificationPlans(context.Context, evidence.Node, evidence.Snapshot, harness.VerificationPolicy) ([]harness.VerificationPlan, error) {
	return nil, nil
}

func (a *Adapter) EvaluateVerification(context.Context, harness.VerificationPlan, *harness.ToolResult, evidence.Snapshot) (*harness.VerificationOutcome, error) {
	return nil, errors.New("software adapter has no automatic claim verification plan")
}

func (a *Adapter) Evaluate(_ context.Context, input harness.EvaluationInput) (*harness.Evaluation, error) {
	observations := map[string]evidence.Node{}
	for _, node := range input.Snapshot.Nodes {
		if node.Kind == evidence.KindObservation {
			observations[node.ClaimKey] = node
		}
	}
	failedA, failedAExists := observations["patch:candidate-a"]
	passedB, passedBExists := observations["patch:candidate-b"]
	correctTestSequence := failedAExists && failedA.ClaimValue == "regression_failed" && passedBExists && passedB.ClaimValue == "all_tests_passed"
	claimSupported := false
	for _, claim := range input.Snapshot.Nodes {
		if claim.Kind != evidence.KindClaim || !claim.Active || claim.ClaimKey != "patch:boundary-check" || claim.ClaimValue != "verified" {
			continue
		}
		for _, id := range claim.EvidenceIDs {
			if id == passedB.ID {
				claimSupported = true
			}
		}
	}
	success := input.Completed && correctTestSequence && claimSupported
	score := 0.0
	if correctTestSequence {
		score = 0.5
	}
	if claimSupported {
		score += 0.5
	}
	return &harness.Evaluation{Success: success, Score: score, Details: map[string]any{
		"candidate_a_regression_detected":  failedAExists && failedA.ClaimValue == "regression_failed",
		"candidate_b_suite_passed":         passedBExists && passedB.ClaimValue == "all_tests_passed",
		"verified_claim_cites_candidate_b": claimSupported,
	}}, nil
}
