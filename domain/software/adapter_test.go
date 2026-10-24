package software

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/qeinstein/paradox/harness"
)

func TestSoftwareDomainRevisesAfterRegressionAndVerifiesCorrectedPatch(t *testing.T) {
	provider := harness.NewMock("software-fixture", "software-fixture-v1", func(ctx context.Context, request *harness.ModelRequest) (*harness.ModelResponse, error) {
		observations := map[string]string{}
		claimSubmitted := false
		for _, message := range request.Messages {
			if message.Role == harness.RoleTool {
				var payload struct {
					Tool        string         `json:"tool"`
					Result      map[string]any `json:"result"`
					EvidenceIDs []string       `json:"evidence_ids"`
				}
				if json.Unmarshal([]byte(message.Content), &payload) == nil && payload.Tool == "run_test_suite" && len(payload.EvidenceIDs) > 0 {
					candidate, _ := payload.Result["candidate"].(string)
					observations[candidate] = payload.EvidenceIDs[0]
				}
			}
			if message.Role == harness.RoleAssistant {
				for _, call := range message.ToolCalls {
					if call.Name == "submit_claim" {
						claimSubmitted = true
					}
				}
			}
		}
		switch {
		case observations["candidate-a"] == "":
			return toolCall("run_test_suite", `{"candidate":"candidate-a"}`), nil
		case observations["candidate-b"] == "":
			return toolCall("run_test_suite", `{"candidate":"candidate-b"}`), nil
		case !claimSubmitted:
			args, _ := json.Marshal(map[string]any{
				"text":      "The revised boundary check passes the complete regression suite.",
				"claim_key": "patch:boundary-check", "claim_value": "verified",
				"evidence_ids": []string{observations["candidate-b"]},
			})
			return toolCall("submit_claim", string(args)), nil
		default:
			return toolCall("finish", `{"conclusion":"candidate-b passed all regression tests after candidate-a failed the upper-bound test."}`), nil
		}
	})
	result, err := harness.Run(context.Background(), harness.RunConfig{
		RunID: "software-transfer", Task: Task(), Mode: harness.EvidenceMode,
		Provider: provider, Domain: New(), WorkDir: t.TempDir(), ArtifactsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("software transfer workflow failed: %+v", result)
	}
	if result.Snapshot.Stats.Observations != 2 || result.Usage.ToolCalls != 4 {
		t.Fatalf("expected failing and passing test evidence plus revised claim: stats=%+v calls=%+v", result.Snapshot.Stats, result.Usage)
	}
}

func toolCall(name, args string) *harness.ModelResponse {
	return &harness.ModelResponse{ToolCalls: []harness.ToolCall{{
		ID: fmt.Sprintf("call-%s", name), Name: name, Arguments: args,
	}}}
}
