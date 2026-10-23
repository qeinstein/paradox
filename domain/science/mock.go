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
			CostUSD: 0.0001, CostAvailable: true,
		},
	}
}
