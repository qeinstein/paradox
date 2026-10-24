package eval

import (
	"testing"

	"github.com/qeinstein/paradox/evidence"
)

func TestMeasurementVerificationAndRetractionScoringUsesIndependentBatches(t *testing.T) {
	snapshot := measurementVerificationFixture(true)
	records, correct, incorrect, unknown := assessVerifications(snapshot)
	if records != 1 || correct != 1 || incorrect != 0 || unknown != 0 {
		t.Fatalf("expected correct measured drift assessment, got %d/%d/%d/%d", records, correct, incorrect, unknown)
	}
	evaluated, bad := assessRetractions(snapshot)
	if evaluated != 2 || bad != 0 {
		t.Fatalf("correct verifier should justify both dependent retractions, got evaluated=%d incorrect=%d", evaluated, bad)
	}

	snapshot = measurementVerificationFixture(false)
	_, correct, incorrect, unknown = assessVerifications(snapshot)
	if correct != 0 || incorrect != 1 || unknown != 0 {
		t.Fatalf("retaining a stability assumption despite measured drift must be incorrect, got %d/%d/%d", correct, incorrect, unknown)
	}
	evaluated, bad = assessRetractions(snapshot)
	if evaluated != 2 || bad != 2 {
		t.Fatalf("contradicted stability without measured drift should count as incorrect retractions, got evaluated=%d incorrect=%d", evaluated, bad)
	}
}

func measurementVerificationFixture(contradicted bool) evidence.Snapshot {
	return evidence.Snapshot{
		Nodes: []evidence.Node{
			{ID: "claim-1", Kind: evidence.KindClaim, Active: false},
			{ID: "asm-1", Kind: evidence.KindAssumption, Active: false},
			{ID: "pilot", Kind: evidence.KindObservation, Metadata: map[string]any{
				"dataset": "sensor-stability-v1", "batch": "pilot", "mean": 20.0,
			}},
			{ID: "replication", Kind: evidence.KindObservation, Metadata: map[string]any{
				"dataset": "sensor-stability-v1", "batch": "replication", "mean": 23.0,
				"tolerance": 0.5,
			}},
		},
		Verifications: []evidence.VerificationRecord{{
			ID: "ver-1", ClaimID: "claim-1", AssumptionID: "asm-1",
			EvidenceID: "replication", Method: "measurement_stability", Contradicted: contradicted,
		}},
		Retractions: []evidence.Retraction{
			{NodeID: "asm-1", Cause: "verification"},
			{NodeID: "claim-1", Cause: "contradiction"},
		},
	}
}
