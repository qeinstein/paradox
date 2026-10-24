package science

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/qeinstein/paradox/harness"
)

// NewMockProvider is a deterministic test double for the scientific vertical
// slice. It deliberately overgeneralizes after a partial experiment. The
// harness, not the provider, decides whether that claim is invalidated by the
// executable verification result.
func NewMockProvider() *harness.MockProvider {
	return harness.NewMock("mock-science", "mock-science-v1", func(ctx context.Context, req *harness.ModelRequest) (*harness.ModelResponse, error) {
		return mockStep(req)
	})
}

type mockToolPayload struct {
	Name        string
	Result      map[string]any
	EvidenceIDs []string
}

func mockStep(req *harness.ModelRequest) (*harness.ModelResponse, error) {
	if isPrimalityPrompt(req) {
		return mockPrimalityStep(req)
	}
	return mockResearchStep(req)
}

func isPrimalityPrompt(req *harness.ModelRequest) bool {
	for _, message := range req.Messages {
		if message.Role == harness.RoleUser && strings.Contains(message.Content, "f(n)=n^2+n+41") {
			return true
		}
	}
	return false
}

func mockPrimalityStep(req *harness.ModelRequest) (*harness.ModelResponse, error) {
	payloads := mockPayloads(req)
	invalidated := mockInvalidated(req)
	hasUniversal := mockHasClaim(req, func(args map[string]any) bool {
		return stringValue(args["claim_value"]) == "all_prime" && scopeEnd(args) >= 100
	})
	hasBounded := mockHasClaim(req, func(args map[string]any) bool {
		return stringValue(args["claim_value"]) == "all_prime" && scopeEnd(args) > 0 && scopeEnd(args) < 100
	})
	hasCounterexampleClaim := mockHasClaim(req, func(args map[string]any) bool {
		return stringValue(args["claim_value"]) == "composite"
	})

	partial := findScan(payloads, 0, 39)
	if partial == nil {
		return mockToolResponse("run_experiment", map[string]any{
			"experiment": "primality_scan", "expression": "n*n+n+41", "start": 0, "end": 39,
		}), nil
	}

	if !hasUniversal {
		rangeEvidence := firstEvidence(partial)
		if rangeEvidence == "" {
			return nil, fmt.Errorf("mock: experiment result has no evidence id")
		}
		return mockToolResponse("submit_claim", map[string]any{
			"text":         "For every integer n in [0,100], f(n)=n^2+n+41 is prime.",
			"claim_key":    "primality:poly:n^2+n+41",
			"claim_value":  "all_prime",
			"subject":      "n^2+n+41",
			"predicate":    "all_prime_over_range",
			"object":       "0-100",
			"confidence":   0.8,
			"evidence_ids": []string{rangeEvidence},
			"assumptions": []map[string]any{{
				"text":       "The tested range [0,39] is representative of the claimed range [0,100].",
				"kind":       "range_representativeness",
				"confidence": 0.75,
				"metadata": map[string]any{
					"kind":            "range_representativeness",
					"assumption_kind": "range_representativeness",
					"expression":      "n*n+n+41",
					"tested_start":    0,
					"tested_end":      39,
					"claimed_start":   0,
					"claimed_end":     100,
				},
			}},
			"metadata": map[string]any{
				"scope":      map[string]any{"start": 0, "end": 100},
				"expression": "n*n+n+41",
			},
		}), nil
	}

	if invalidated && !hasBounded {
		boundedEvidence := firstEvidence(partial)
		counterexampleEvidence := invalidationEvidence(req)
		if counterexampleEvidence == "" {
			if full := findCompositeScan(payloads); full != nil {
				counterexampleEvidence = lastEvidence(full)
			}
		}
		if counterexampleEvidence == "" {
			return nil, fmt.Errorf("mock: invalidation reported but no counterexample evidence is available")
		}
		calls := []map[string]any{
			{
				"text":         "For every integer n in [0,39], f(n)=n^2+n+41 is prime.",
				"claim_key":    "primality:poly:n^2+n+41",
				"claim_value":  "all_prime",
				"subject":      "n^2+n+41",
				"predicate":    "all_prime_over_range",
				"object":       "0-39",
				"confidence":   1.0,
				"evidence_ids": []string{boundedEvidence},
				"metadata": map[string]any{
					"scope":      map[string]any{"start": 0, "end": 39},
					"expression": "n*n+n+41",
				},
			},
			{
				"text":         "f(40)=1681=41*41 is composite; the universal primality claim is false.",
				"claim_key":    "primality:n=40:n*n+n+41",
				"claim_value":  "composite",
				"subject":      "n=40",
				"predicate":    "is_prime",
				"object":       "false",
				"confidence":   1.0,
				"evidence_ids": []string{counterexampleEvidence},
				"metadata": map[string]any{
					"n": 40, "value": 1681, "factors": []any{41, 41},
					"scope": map[string]any{"start": 40, "end": 40},
				},
			},
		}
		return mockMultiToolResponse("submit_claim", calls), nil
	}

	if hasBounded && hasCounterexampleClaim {
		return mockToolResponse("finish", map[string]any{
			"conclusion": "The universal claim was false: f(40)=1681=41*41 is composite. The verified bounded claim is that f(n) is prime for every n in [0,39]. The original range assumption was invalidated by an exhaustive scan of [0,100].",
		}), nil
	}

	return mockToolResponse("finish", map[string]any{
		"conclusion": "For every integer n in [0,100], f(n)=n^2+n+41 is prime, based on the experiment over [0,39].",
	}), nil
}

// mockResearchStep is an offline protocol fixture for the general computational
// tasks. It discovers the permitted operation and dataset from the request's
// tool schema, then grounds its claims in returned evidence. It does not read
// task evaluator metadata or supply reference answers to the scorer.
func mockResearchStep(req *harness.ModelRequest) (*harness.ModelResponse, error) {
	experiment, dataset, ok := permittedExperiment(req.Tools)
	if !ok {
		return nil, fmt.Errorf("mock: request has no permitted experiment tool")
	}
	payloads := mockPayloads(req)
	var pilot, replication, primary *mockToolPayload
	for index := range payloads {
		payload := &payloads[index]
		if payload.Name != "run_experiment" || stringValue(payload.Result["experiment"]) != experiment {
			continue
		}
		if experiment == "collect_measurements" {
			switch stringValue(payload.Result["batch"]) {
			case "pilot":
				pilot = payload
			case "replication":
				replication = payload
			}
		} else if primary == nil {
			primary = payload
		}
	}

	if experiment == "collect_measurements" {
		if pilot == nil {
			return mockToolResponse("run_experiment", map[string]any{
				"experiment": experiment, "dataset": dataset, "batch": "pilot",
			}), nil
		}
		if !mockHasClaim(req, func(args map[string]any) bool {
			return stringValue(args["claim_key"]) == "measurement:"+dataset && stringValue(args["claim_value"]) == "stable"
		}) {
			evidenceID := firstEvidence(pilot)
			if evidenceID == "" {
				return nil, fmt.Errorf("mock: pilot result has no evidence id")
			}
			return mockToolResponse("submit_claim", map[string]any{
				"text":      "The pilot measurements are within tolerance; provisionally the sensor is stable.",
				"claim_key": "measurement:" + dataset, "claim_value": "stable",
				"subject": dataset, "predicate": "stable_across_runs", "object": "stable",
				"confidence": 0.75, "evidence_ids": []string{evidenceID},
				"assumptions": []map[string]any{{
					"text": "The pilot batch is representative of subsequent measurements.",
					"kind": "measurement_stability", "confidence": 0.75,
					"metadata": map[string]any{"assumption_kind": "measurement_stability"},
				}},
			}), nil
		}
		if replication == nil && !mockInvalidated(req) {
			return mockToolResponse("run_experiment", map[string]any{
				"experiment": experiment, "dataset": dataset, "batch": "replication",
			}), nil
		}
		evidenceID := ""
		if replication != nil {
			evidenceID = firstEvidence(replication)
		}
		if evidenceID == "" {
			evidenceID = invalidationEvidence(req)
		}
		if evidenceID == "" {
			return nil, fmt.Errorf("mock: no evidence available for measurement replan")
		}
		if !mockHasClaim(req, func(args map[string]any) bool {
			return stringValue(args["claim_key"]) == "measurement:"+dataset && stringValue(args["claim_value"]) == "drift_detected"
		}) {
			return mockToolResponse("submit_claim", map[string]any{
				"text":      "The independent replication batch contradicts the stability assumption and shows drift.",
				"claim_key": "measurement:" + dataset, "claim_value": "drift_detected",
				"subject": dataset, "predicate": "stable_across_runs", "object": "drift_detected",
				"confidence": 0.95, "evidence_ids": []string{evidenceID},
			}), nil
		}
		return mockToolResponse("finish", map[string]any{
			"conclusion": "The independent measurement batch showed drift; the pilot stability assumption did not hold.",
		}), nil
	}

	if primary == nil {
		return mockToolResponse("run_experiment", map[string]any{
			"experiment": experiment, "dataset": dataset,
		}), nil
	}
	evidenceID := firstEvidence(primary)
	if evidenceID == "" {
		return nil, fmt.Errorf("mock: experiment result has no evidence id")
	}
	claimKey, claimValue, text := claimFromMeasurement(experiment, dataset, primary.Result)
	if claimKey == "" || claimValue == "" {
		return nil, fmt.Errorf("mock: cannot derive a claim from experiment %q output", experiment)
	}
	if !mockHasClaim(req, func(args map[string]any) bool {
		return stringValue(args["claim_key"]) == claimKey && stringValue(args["claim_value"]) == claimValue
	}) {
		return mockToolResponse("submit_claim", map[string]any{
			"text": text, "claim_key": claimKey, "claim_value": claimValue,
			"subject": dataset, "predicate": experiment, "object": claimValue,
			"confidence": 0.95, "evidence_ids": []string{evidenceID},
		}), nil
	}
	return mockToolResponse("finish", map[string]any{"conclusion": text}), nil
}

func permittedExperiment(tools []harness.ToolDef) (string, string, bool) {
	for _, tool := range tools {
		if tool.Name != "run_experiment" {
			continue
		}
		var schema struct {
			Properties map[string]struct {
				Enum []any `json:"enum"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
			return "", "", false
		}
		if len(schema.Properties["experiment"].Enum) == 0 || len(schema.Properties["dataset"].Enum) == 0 {
			return "", "", false
		}
		experiment := stringValue(schema.Properties["experiment"].Enum[0])
		dataset := stringValue(schema.Properties["dataset"].Enum[0])
		return experiment, dataset, experiment != "" && dataset != ""
	}
	return "", "", false
}

func claimFromMeasurement(experiment, dataset string, result map[string]any) (string, string, string) {
	switch experiment {
	case "exact_permutation_test":
		value := "no_significant_difference"
		if result["significant"] == true {
			value = "significant_difference"
		}
		return "stats:" + dataset, value, fmt.Sprintf("The exact permutation test reports p=%v; the result is %s.", result["p_value"], value)
	case "integer_optimization":
		x, xOK := numberAsInt(result["minimizer"])
		y, yOK := numberAsInt(result["minimum"])
		if !xOK || !yOK {
			return "", "", ""
		}
		value := fmt.Sprintf("x=%d;value=%d", x, y)
		return "optimization:" + dataset, value, fmt.Sprintf("The exhaustive search found x=%d with objective value %d.", x, y)
	default:
		return "", "", ""
	}
}

func numberAsInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), typed == float64(int(typed))
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	default:
		return 0, false
	}
}

func mockPayloads(req *harness.ModelRequest) []mockToolPayload {
	payloads := []mockToolPayload{}
	for _, message := range req.Messages {
		if message.Role != harness.RoleTool {
			continue
		}
		var outer map[string]any
		if err := json.Unmarshal([]byte(message.Content), &outer); err != nil {
			continue
		}
		result, _ := outer["result"].(map[string]any)
		payload := mockToolPayload{Name: stringValue(outer["tool"]), Result: result}
		if ids, ok := outer["evidence_ids"].([]any); ok {
			for _, id := range ids {
				if value, ok := id.(string); ok {
					payload.EvidenceIDs = append(payload.EvidenceIDs, value)
				}
			}
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

func invalidationEvidence(req *harness.ModelRequest) string {
	for _, message := range req.Messages {
		if message.Role != harness.RoleUser || !strings.Contains(message.Content, "INVALIDATED") {
			continue
		}
		fields := strings.Fields(message.Content)
		for index, field := range fields {
			if field == "evidence" && index+1 < len(fields) {
				return strings.Trim(fields[index+1], ".,;:)")
			}
		}
	}
	return ""
}

func mockInvalidated(req *harness.ModelRequest) bool {
	for _, message := range req.Messages {
		if message.Role == harness.RoleUser && strings.Contains(message.Content, "INVALIDATED") {
			return true
		}
	}
	return false
}

func mockHasClaim(req *harness.ModelRequest, predicate func(map[string]any) bool) bool {
	for _, message := range req.Messages {
		if message.Role != harness.RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.Name != "submit_claim" {
				continue
			}
			var args map[string]any
			if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
				continue
			}
			if predicate(args) {
				return true
			}
		}
	}
	return false
}

func findScan(payloads []mockToolPayload, start, end int) *mockToolPayload {
	for index := range payloads {
		payload := &payloads[index]
		if stringValue(payload.Result["experiment"]) != "primality_scan" {
			continue
		}
		if intValue(payload.Result["start"]) == start && intValue(payload.Result["end"]) == end {
			return payload
		}
	}
	return nil
}

func findCompositeScan(payloads []mockToolPayload) *mockToolPayload {
	for index := range payloads {
		payload := &payloads[index]
		if stringValue(payload.Result["experiment"]) == "primality_scan" && intValue(payload.Result["composite_count"]) > 0 {
			return payload
		}
	}
	return nil
}

func firstEvidence(payload *mockToolPayload) string {
	if payload == nil || len(payload.EvidenceIDs) == 0 {
		return ""
	}
	return payload.EvidenceIDs[0]
}

func lastEvidence(payload *mockToolPayload) string {
	if payload == nil || len(payload.EvidenceIDs) == 0 {
		return ""
	}
	return payload.EvidenceIDs[len(payload.EvidenceIDs)-1]
}

func scopeEnd(args map[string]any) int {
	metadata, _ := args["metadata"].(map[string]any)
	scope, _ := metadata["scope"].(map[string]any)
	return intValue(scope["end"])
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	default:
		return 0
	}
}

func mockToolResponse(name string, args map[string]any) *harness.ModelResponse {
	return mockMultiToolResponse(name, []map[string]any{args})
}

func mockMultiToolResponse(name string, args []map[string]any) *harness.ModelResponse {
	calls := make([]harness.ToolCall, 0, len(args))
	for index, value := range args {
		encoded, _ := json.Marshal(value)
		calls = append(calls, harness.ToolCall{
			ID: fmt.Sprintf("call-%s-%d", name, index+1), Name: name, Arguments: string(encoded),
		})
	}
	return &harness.ModelResponse{
		ToolCalls: calls, FinishReason: "tool_calls",
		Usage: harness.Usage{
			PromptTokens: 120, CompletionTokens: 30, TotalTokens: 150,
		},
	}
}
