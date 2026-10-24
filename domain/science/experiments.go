package science

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/qeinstein/paradox/evidence"
	"github.com/qeinstein/paradox/harness"
)

type experimentArgs struct {
	Experiment string `json:"experiment"`
	Dataset    string `json:"dataset"`
	Batch      string `json:"batch,omitempty"`
}

var groupDifference = struct {
	Control   []float64
	Treatment []float64
}{
	Control:   []float64{1, 2, 3, 4, 5},
	Treatment: []float64{7, 8, 9, 10, 11},
}

func taskExperimentTool(taskID string) harness.Tool {
	allowed := map[string]string{
		TaskPermutationTest: "exact_permutation_test",
		TaskOptimization:    "integer_optimization",
		TaskMeasurement:     "collect_measurements",
	}
	experiment := allowed[taskID]
	if experiment == "" {
		return primalityScanTool()
	}
	schema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"experiment": map[string]any{"type": "string", "enum": []string{experiment}},
			"dataset":    map[string]any{"type": "string", "enum": []string{datasetForTask(taskID)}},
			"batch":      map[string]any{"type": "string", "enum": []string{"pilot", "replication"}},
		},
		"required": []string{"experiment", "dataset"},
	})
	return harness.Tool{
		Name: "run_experiment", Description: taskExperimentDescription(taskID), Parameters: schema,
		Handler: func(ctx context.Context, tc *harness.ToolContext, raw json.RawMessage) (*harness.ToolResult, error) {
			var args experimentArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, fmt.Errorf("invalid experiment arguments: %w", err)
			}
			if args.Experiment != experiment || args.Dataset != datasetForTask(taskID) {
				return nil, errors.New("experiment or dataset is not available for this task")
			}
			switch taskID {
			case TaskPermutationTest:
				return runPermutationTest(tc)
			case TaskOptimization:
				return runIntegerOptimization(tc)
			case TaskMeasurement:
				if args.Batch == "" {
					args.Batch = "pilot"
				}
				return runMeasurementBatch(tc, args.Batch)
			default:
				return nil, errors.New("no experiment implementation for task")
			}
		},
	}
}

func datasetForTask(taskID string) string {
	switch taskID {
	case TaskPermutationTest:
		return "group-difference-v1"
	case TaskOptimization:
		return "benchmark-function-1"
	case TaskMeasurement:
		return "sensor-stability-v1"
	default:
		return ""
	}
}

func taskExperimentDescription(taskID string) string {
	switch taskID {
	case TaskPermutationTest:
		return "Run the predefined exact two-sided permutation test for the task's fixed control and treatment samples. The tool returns the computed statistic and evidence."
	case TaskOptimization:
		return "Exhaustively evaluate the task's fixed integer objective on its stated interval and return the minimizing input and value as evidence."
	case TaskMeasurement:
		return "Measure the predefined sensor study. Run the pilot batch first; a replication batch is available to test stability."
	default:
		return "Run a predefined deterministic scientific experiment."
	}
}

func runPermutationTest(tc *harness.ToolContext) (*harness.ToolResult, error) {
	controlMean := mean(groupDifference.Control)
	treatmentMean := mean(groupDifference.Treatment)
	observed := treatmentMean - controlMean
	all := append(append([]float64(nil), groupDifference.Control...), groupDifference.Treatment...)
	extreme, total := 0, 0
	chosen := make([]float64, 0, len(groupDifference.Control))
	var enumerate func(int, int)
	enumerate = func(index, need int) {
		if need == 0 {
			chosenSum := 0.0
			chosenSet := map[int]bool{}
			for _, value := range chosen {
				chosenSum += value
				chosenSet[int(value)] = true
			}
			otherSum := 0.0
			for _, value := range all {
				if !chosenSet[int(value)] {
					otherSum += value
				}
			}
			difference := chosenSum/float64(len(chosen)) - otherSum/float64(len(all)-len(chosen))
			total++
			if math.Abs(difference)+1e-12 >= math.Abs(observed) {
				extreme++
			}
			return
		}
		if index >= len(all) || len(all)-index < need {
			return
		}
		chosen = append(chosen, all[index])
		enumerate(index+1, need-1)
		chosen = chosen[:len(chosen)-1]
		enumerate(index+1, need)
	}
	enumerate(0, len(groupDifference.Control))
	if total == 0 {
		return nil, errors.New("permutation test generated no assignments")
	}
	pValue := float64(extreme) / float64(total)
	data := map[string]any{
		"experiment": "exact_permutation_test", "dataset": "group-difference-v1",
		"control": groupDifference.Control, "treatment": groupDifference.Treatment,
		"control_mean": controlMean, "treatment_mean": treatmentMean,
		"observed_difference": observed, "extreme_assignments": extreme,
		"permutations": total, "p_value": pValue, "alpha": 0.05,
		"significant": pValue <= 0.05,
	}
	artifact, err := saveArtifact(tc, "permutation-test", data)
	if err != nil {
		return nil, err
	}
	claimValue := "no_significant_difference"
	text := fmt.Sprintf("Exact two-sided permutation test: difference %.6g, p=%.6g across %d assignments", observed, pValue, total)
	if pValue <= 0.05 {
		claimValue = "significant_difference"
	}
	observation := evidence.ObservationInput{
		Text: text, ClaimKey: "stats:group-difference-v1", ClaimValue: claimValue,
		Subject: "group-difference-v1", Predicate: "treatment_differs_from_control",
		Object: claimValue, Source: evidence.SourceExperiment,
		Provenance: map[string]any{"experiment": "exact_permutation_test", "dataset": "group-difference-v1"},
		Metadata:   data, Artifacts: []evidence.ArtifactRef{artifact},
	}
	return &harness.ToolResult{Content: text, Data: data, Evidence: []evidence.ObservationInput{observation}, Artifacts: []evidence.ArtifactRef{artifact}}, nil
}

func runIntegerOptimization(tc *harness.ToolContext) (*harness.ToolResult, error) {
	bestX, bestY := -10, math.Inf(1)
	for x := -10; x <= 10; x++ {
		value := float64(3*x*x - 24*x + 11)
		if value < bestY {
			bestX, bestY = x, value
		}
	}
	data := map[string]any{
		"experiment": "integer_optimization", "dataset": "benchmark-function-1",
		"function": "3*x^2-24*x+11", "start": -10, "end": 10,
		"minimizer": bestX, "minimum": bestY, "evaluated": 21,
	}
	artifact, err := saveArtifact(tc, "integer-optimization", data)
	if err != nil {
		return nil, err
	}
	claimValue := fmt.Sprintf("x=%d;value=%.0f", bestX, bestY)
	text := fmt.Sprintf("Exhaustive integer optimization found x=%d with f(x)=%.0f on [-10,10]", bestX, bestY)
	observation := evidence.ObservationInput{
		Text: text, ClaimKey: "optimization:benchmark-function-1", ClaimValue: claimValue,
		Subject: "benchmark-function-1", Predicate: "integer_minimizer", Object: claimValue,
		Source:     evidence.SourceExperiment,
		Provenance: map[string]any{"experiment": "integer_optimization", "dataset": "benchmark-function-1"},
		Metadata:   data, Artifacts: []evidence.ArtifactRef{artifact},
	}
	return &harness.ToolResult{Content: text, Data: data, Evidence: []evidence.ObservationInput{observation}, Artifacts: []evidence.ArtifactRef{artifact}}, nil
}

func runMeasurementBatch(tc *harness.ToolContext, batch string) (*harness.ToolResult, error) {
	measurements := map[string][]float64{
		"pilot":       {19.9, 20.0, 20.1},
		"replication": {22.9, 23.0, 23.1},
	}
	values, ok := measurements[batch]
	if !ok {
		return nil, fmt.Errorf("unknown measurement batch %q", batch)
	}
	data := map[string]any{
		"experiment": "collect_measurements", "dataset": "sensor-stability-v1",
		"batch": batch, "measurements": values, "mean": mean(values),
		"reference": 20.0, "tolerance": 0.5,
	}
	artifact, err := saveArtifact(tc, "sensor-measurements-"+batch, data)
	if err != nil {
		return nil, err
	}
	key := "measurement:sensor-stability-v1:" + batch
	value := fmt.Sprintf("mean=%.1f", mean(values))
	text := fmt.Sprintf("Sensor study %s batch measured %v (mean %.3f)", batch, values, mean(values))
	observation := evidence.ObservationInput{
		Text: text, ClaimKey: key, ClaimValue: value,
		Subject: "sensor-stability-v1", Predicate: "measured_temperature", Object: value,
		Source:     evidence.SourceExperiment,
		Provenance: map[string]any{"experiment": "collect_measurements", "dataset": "sensor-stability-v1", "batch": batch},
		Metadata:   data, Artifacts: []evidence.ArtifactRef{artifact},
	}
	return &harness.ToolResult{Content: text, Data: data, Evidence: []evidence.ObservationInput{observation}, Artifacts: []evidence.ArtifactRef{artifact}}, nil
}

func mean(values []float64) float64 {
	var total float64
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func saveArtifact(tc *harness.ToolContext, prefix string, data map[string]any) (evidence.ArtifactRef, error) {
	if tc == nil || strings.TrimSpace(tc.ArtifactsDir) == "" {
		return evidence.ArtifactRef{}, errors.New("experiment requires an artifacts directory")
	}
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return evidence.ArtifactRef{}, err
	}
	sum := sha256.Sum256(encoded)
	hash := hex.EncodeToString(sum[:])
	name := fmt.Sprintf("%s-%s.json", prefix, hash[:16])
	if err := os.MkdirAll(tc.ArtifactsDir, 0o700); err != nil {
		return evidence.ArtifactRef{}, err
	}
	path := filepath.Join(tc.ArtifactsDir, name)
	if err := writeArtifact(path, encoded); err != nil {
		return evidence.ArtifactRef{}, err
	}
	return evidence.ArtifactRef{Name: name, Path: path, SHA256: hash, MediaType: "application/json"}, nil
}
