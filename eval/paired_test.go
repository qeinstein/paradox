package eval

import (
	"math"
	"testing"

	"github.com/qeinstein/paradox/harness"
)

func TestPairedGroupUsesTaskTrialPairsAndReportsUncertainty(t *testing.T) {
	records := []RunRecord{
		{TaskID: "a", Mode: harness.Baseline, Trial: 1, Score: 0.2},
		{TaskID: "a", Mode: harness.Paradox, Trial: 1, Score: 0.5},
		{TaskID: "a", Mode: harness.Baseline, Trial: 2, Score: 0.4},
		{TaskID: "a", Mode: harness.Paradox, Trial: 2, Score: 0.8},
		{TaskID: "b", Mode: harness.Baseline, Trial: 1, Score: 0.7},
		{TaskID: "b", Mode: harness.Paradox, Trial: 1, Score: 0.9},
		{TaskID: "b", Mode: harness.Baseline, Trial: 2, Score: 0.1},
		{TaskID: "b", Mode: harness.Paradox, Trial: 2, Score: 0.2, Error: "tool failure"},
	}
	paired := pairedGroup("*", records, harness.Baseline, harness.Paradox)
	if paired.Pairs != 3 {
		t.Fatalf("expected three task/trial pairs after excluding failed run, got %+v", paired)
	}
	if math.Abs(paired.MeanScoreDelta-0.3) > 1e-12 {
		t.Fatalf("expected mean paired delta 0.3, got %+v", paired)
	}
	if !paired.UncertaintyAvailable || paired.ScoreDeltaStdError == nil || paired.ScoreDeltaCI95Lower == nil || paired.ScoreDeltaCI95Upper == nil {
		t.Fatalf("expected paired uncertainty for three pairs, got %+v", paired)
	}
	if *paired.ScoreDeltaCI95Lower >= paired.MeanScoreDelta || *paired.ScoreDeltaCI95Upper <= paired.MeanScoreDelta {
		t.Fatalf("confidence interval should contain the mean: %+v", paired)
	}
}

func TestPairedGroupLeavesSingleTrialUncertaintyUnavailable(t *testing.T) {
	paired := pairedGroup("task", []RunRecord{
		{TaskID: "task", Mode: harness.Baseline, Trial: 1, Score: 0.25},
		{TaskID: "task", Mode: harness.Paradox, Trial: 1, Score: 0.75},
	}, harness.Baseline, harness.Paradox)
	if paired.Pairs != 1 || paired.UncertaintyAvailable || paired.ScoreDeltaCI95Lower != nil {
		t.Fatalf("single pair must remain descriptive: %+v", paired)
	}
}
