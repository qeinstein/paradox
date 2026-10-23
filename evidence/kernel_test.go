package evidence_test

import (
	"testing"

	"github.com/qeinstein/paradox/evidence"
)

func TestClaimRequiresExplicitEvidenceAndRetractsDependents(t *testing.T) {
	kernel := evidence.New("test-run")
	obs, err := kernel.AddObservation(evidence.ObservationInput{
		Text: "the sample contains 10 observations", ClaimKey: "sample:size", ClaimValue: "10",
		Source: evidence.SourceExperiment,
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := kernel.SubmitClaim(evidence.ClaimInput{
		Text: "the sample is large enough", ClaimKey: "sample:large", ClaimValue: "true",
		EvidenceIDs: []string{obs.ID},
		Assumptions: []evidence.AssumptionInput{{
			Text: "ten observations are sufficient for this analysis", Kind: "sufficiency",
			Confidence: 0.8,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !claim.Claim.Active {
		t.Fatalf("expected supported claim to be active: %+v", claim.Claim)
	}
	dependent, err := kernel.SubmitClaim(evidence.ClaimInput{
		Text: "we can proceed with the conclusion", ClaimKey: "decision:proceed", ClaimValue: "true",
		EvidenceIDs: []string{claim.Claim.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !dependent.Claim.Active {
		t.Fatal("expected dependent claim to be active")
	}
	contradiction, err := kernel.MarkContradiction(claim.Claim.ID, claim.Assumptions[0].ID, obs.ID, "insufficient sample")
	if err != nil {
		t.Fatal(err)
	}
	if len(contradiction.Invalidated) == 0 {
		t.Fatal("expected invalidated claim")
	}
	claimNode, _ := kernel.Node(claim.Claim.ID)
	if claimNode.Active || claimNode.Status >= 0.6 {
		t.Fatalf("claim should be invalid after contradiction: %+v", claimNode)
	}
	dependentNode, _ := kernel.Node(dependent.Claim.ID)
	if dependentNode.Active || dependentNode.Status >= 0.6 {
		t.Fatalf("dependent claim should be invalid after parent retraction: %+v", dependentNode)
	}
	snapshot := kernel.Snapshot()
	if snapshot.Stats.Contradictions != 1 {
		t.Fatalf("expected one contradiction, got %d", snapshot.Stats.Contradictions)
	}
	if snapshot.Stats.Retractions < 2 {
		t.Fatalf("expected assumption and claim retractions, got %d", snapshot.Stats.Retractions)
	}
	if snapshot.Stats.UnsupportedClaims != 0 {
		t.Fatalf("invalid claims should not count as active unsupported claims: %+v", snapshot.Stats)
	}
}

func TestUnsupportedClaimIsCounted(t *testing.T) {
	kernel := evidence.New("test-run")
	claim, err := kernel.SubmitClaim(evidence.ClaimInput{
		Text: "the model believes the answer is 42", ClaimKey: "answer", ClaimValue: "42",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !claim.Claim.Active {
		t.Fatal("a bare claim is an explicit unverified assertion and remains active")
	}
	snapshot := kernel.Snapshot()
	if snapshot.Stats.UnsupportedClaims != 1 {
		t.Fatalf("expected one unsupported claim, got %+v", snapshot.Stats)
	}
	if snapshot.Stats.UnsupportedRate != 1 {
		t.Fatalf("expected unsupported rate 1, got %f", snapshot.Stats.UnsupportedRate)
	}
}

func TestGraphContainsContradictionGuard(t *testing.T) {
	kernel := evidence.New("test-run")
	claim, err := kernel.SubmitClaim(evidence.ClaimInput{Text: "a claim"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := kernel.Snapshot()
	found := false
	for _, edge := range snapshot.Edges {
		if edge.To == claim.Claim.ID && !edge.Negated && edge.From == claim.GuardID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected positive contradiction-guard edge, edges: %+v", snapshot.Edges)
	}

	foundContradiction := false
	for _, node := range snapshot.Nodes {
		if node.Kind == evidence.KindContradiction && !node.Internal && node.Active {
			foundContradiction = true
		}
	}
	if foundContradiction {
		t.Fatalf("a guard should not appear as a user-facing active contradiction: %+v", snapshot.Nodes)
	}
}
