package evidence_test

import (
	"testing"
	"time"

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

func TestAlternativeJustificationKeepsClaimValidAfterPrimarySupportFails(t *testing.T) {
	kernel := evidence.New("alternative-support")
	primary, err := kernel.AddObservation(evidence.ObservationInput{Text: "measurement from instrument A", Source: evidence.SourceExperiment})
	if err != nil {
		t.Fatal(err)
	}
	independent, err := kernel.AddObservation(evidence.ObservationInput{Text: "independent measurement from instrument B", Source: evidence.SourceExperiment})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := kernel.SubmitClaim(evidence.ClaimInput{
		Text:                   "two independent measurements support the result",
		EvidenceIDs:            []string{primary.ID},
		AlternativeEvidenceIDs: [][]string{{independent.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := kernel.Retract(primary.ID, "instrument A failed calibration", "verification"); err != nil {
		t.Fatal(err)
	}
	stillSupported, ok := kernel.Node(claim.Claim.ID)
	if !ok || !stillSupported.Active || stillSupported.Status < 0.6 {
		t.Fatalf("claim was invalidated despite independent support: %+v", stillSupported)
	}
	if snapshot := kernel.Snapshot(); snapshot.Stats.UnsupportedClaims != 0 {
		t.Fatalf("independent live justification should remain supported: %+v", snapshot.Stats)
	}
	if err := kernel.Retract(independent.ID, "instrument B evidence withdrawn", "verification"); err != nil {
		t.Fatal(err)
	}
	invalidated, _ := kernel.Node(claim.Claim.ID)
	if invalidated.Active || invalidated.Status >= 0.6 {
		t.Fatalf("claim should retract when every justification fails: %+v", invalidated)
	}
}

func TestClaimCannotCiteNonexistentEvidence(t *testing.T) {
	kernel := evidence.New("nonexistent-evidence")
	if _, err := kernel.SubmitClaim(evidence.ClaimInput{
		Text: "fabricated support", EvidenceIDs: []string{"obs-does-not-exist"},
	}); err == nil {
		t.Fatal("expected nonexistent evidence reference to be rejected")
	}
	if snapshot := kernel.Snapshot(); snapshot.Stats.Claims != 0 {
		t.Fatalf("rejected claim modified evidence state: %+v", snapshot.Stats)
	}
}

func TestConflictingDirectObservationsAreReported(t *testing.T) {
	kernel := evidence.New("conflicting-observations")
	for _, value := range []string{"stable", "drift"} {
		if _, err := kernel.AddObservation(evidence.ObservationInput{
			Text:     "independent measurement says " + value,
			ClaimKey: "sensor:calibration", ClaimValue: value,
			Subject: "sensor", Predicate: "calibration", Object: value,
			Source: evidence.SourceExperiment,
		}); err != nil {
			t.Fatal(err)
		}
	}
	report := kernel.CheckConsistency()
	if report.IssueCount == 0 {
		t.Fatalf("contradictory direct measurements were not detected: %+v", report)
	}
}

func TestExpiredAssumptionInvalidatesOnlyClaimsThatLoseTheirSupport(t *testing.T) {
	kernel := evidence.New("assumption-expiry")
	observation, err := kernel.AddObservation(evidence.ObservationInput{Text: "measured value", Source: evidence.SourceExperiment})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := kernel.SubmitClaim(evidence.ClaimInput{
		Text: "a time-limited interpretation", EvidenceIDs: []string{observation.ID},
		Assumptions: []evidence.AssumptionInput{{
			Text: "the calibration remains valid", Kind: "calibration",
			ExpiresAt: time.Now().Add(-time.Second).UnixMilli(),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	expirations := kernel.ExpireAssumptions(time.Now())
	if len(expirations) != 1 || len(expirations[0].InvalidatedClaims) != 1 || expirations[0].InvalidatedClaims[0] != claim.Claim.ID {
		t.Fatalf("expired premise did not identify the lost conclusion: %+v", expirations)
	}
	updated, _ := kernel.Node(claim.Claim.ID)
	if updated.Active || updated.Status >= 0.6 {
		t.Fatalf("conclusion stayed active after its only support expired: %+v", updated)
	}
	snapshot := kernel.Snapshot()
	foundExpiryRetraction := false
	for _, retraction := range snapshot.Retractions {
		if retraction.NodeID == claim.Assumptions[0].ID && retraction.Cause == "expiration" {
			foundExpiryRetraction = true
		}
	}
	if !foundExpiryRetraction {
		t.Fatalf("assumption expiry was not recorded in provenance: %+v", snapshot.Retractions)
	}
}
