// Package eval runs matched comparisons across PARADOX's operating modes and
// produces machine-readable and human-readable reports. The internal suite is
// deterministic and uses the mock science provider by default; it measures the
// harness, not the quality of a remote model.
package eval

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/qeinstein/paradox/app"
	"github.com/qeinstein/paradox/benchmark/scienceworld"
	"github.com/qeinstein/paradox/domain/science"
	"github.com/qeinstein/paradox/evidence"
	"github.com/qeinstein/paradox/harness"
)

const (
	DefaultSuite      = "science"
	ScienceWorldSuite = "scienceworld"
	EvaluatorVersion  = "paradox-evaluator-v0.2"
)

type Options struct {
	ConfigID        string
	Suite           string
	Tasks           []harness.Task
	Modes           []harness.Mode
	Trials          int
	Provider        harness.ModelProvider
	Domain          harness.DomainAdapter
	Budget          harness.Budget
	Verification    harness.VerificationPolicy
	RunsDir         string
	OutputDir       string
	EvalID          string
	GitCommit       string
	ReasoningEffort string
	Temperature     *float64
}

type RunMetrics struct {
	Success                 bool    `json:"success"`
	Score                   float64 `json:"score"`
	Observations            int     `json:"observations"`
	Claims                  int     `json:"claims"`
	ActiveClaims            int     `json:"active_claims"`
	UnsupportedClaims       int     `json:"unsupported_claims"`
	UnsupportedClaimRate    float64 `json:"unsupported_claim_rate"`
	Contradictions          int     `json:"contradictions"`
	Retractions             int     `json:"retractions"`
	RetractionsEvaluated    int     `json:"retractions_evaluated"`
	IncorrectRetractions    int     `json:"incorrect_retractions"`
	IncorrectRetractionRate float64 `json:"incorrect_retraction_rate"`
	VerificationRecords     int     `json:"verification_records"`
	VerificationCorrect     int     `json:"verification_correct"`
	VerificationIncorrect   int     `json:"verification_incorrect"`
	VerificationUnknown     int     `json:"verification_unknown"`
	VerificationAccuracy    float64 `json:"verification_accuracy"`
	PromptTokens            int     `json:"prompt_tokens"`
	CompletionTokens        int     `json:"completion_tokens"`
	TotalTokens             int     `json:"total_tokens"`
	CostUSD                 float64 `json:"cost_usd"`
	CostAvailable           bool    `json:"cost_available"`
	DurationMS              int64   `json:"duration_ms"`
	Steps                   int     `json:"steps"`
	InferenceCalls          int     `json:"inference_calls"`
	ToolCalls               int     `json:"tool_calls"`
	ReproducibilityHash     string  `json:"reproducibility_hash"`
}

type RunRecord struct {
	RunID             string         `json:"run_id"`
	TaskID            string         `json:"task_id"`
	Mode              harness.Mode   `json:"mode"`
	Trial             int            `json:"trial"`
	Success           bool           `json:"success"`
	Score             float64        `json:"score"`
	Error             string         `json:"error,omitempty"`
	RunDir            string         `json:"run_dir,omitempty"`
	Metrics           RunMetrics     `json:"metrics"`
	EvaluationDetails map[string]any `json:"evaluation_details,omitempty"`
}

type Aggregate struct {
	Runs                    int     `json:"runs"`
	Successes               int     `json:"successes"`
	SuccessRate             float64 `json:"success_rate"`
	MeanScore               float64 `json:"mean_score"`
	Observations            int     `json:"observations"`
	Claims                  int     `json:"claims"`
	ActiveClaims            int     `json:"active_claims"`
	UnsupportedClaims       int     `json:"unsupported_claims"`
	UnsupportedClaimRate    float64 `json:"unsupported_claim_rate"`
	Contradictions          int     `json:"contradictions"`
	Retractions             int     `json:"retractions"`
	RetractionsEvaluated    int     `json:"retractions_evaluated"`
	IncorrectRetractions    int     `json:"incorrect_retractions"`
	IncorrectRetractionRate float64 `json:"incorrect_retraction_rate"`
	VerificationRecords     int     `json:"verification_records"`
	VerificationCorrect     int     `json:"verification_correct"`
	VerificationIncorrect   int     `json:"verification_incorrect"`
	VerificationUnknown     int     `json:"verification_unknown"`
	VerificationAccuracy    float64 `json:"verification_accuracy"`
	PromptTokens            int     `json:"prompt_tokens"`
	CompletionTokens        int     `json:"completion_tokens"`
	TotalTokens             int     `json:"total_tokens"`
	CostUSD                 float64 `json:"cost_usd"`
	CostComplete            bool    `json:"cost_complete"`
	TotalDurationMS         int64   `json:"total_duration_ms"`
	MeanDurationMS          float64 `json:"mean_duration_ms"`
	MeanSteps               float64 `json:"mean_steps"`
	InferenceCalls          int     `json:"inference_calls"`
	ToolCalls               int     `json:"tool_calls"`
	Reproducibility         float64 `json:"reproducibility"`
	ReproducibilityRuns     int     `json:"reproducibility_runs"`
}

type ModeReport struct {
	Mode      harness.Mode `json:"mode"`
	Aggregate Aggregate    `json:"aggregate"`
}

type TaskSummary struct {
	ID              string `json:"id"`
	Domain          string `json:"domain"`
	Prompt          string `json:"prompt"`
	Benchmark       string `json:"benchmark,omitempty"`
	TaskName        string `json:"task_name,omitempty"`
	Variation       *int   `json:"variation,omitempty"`
	Seed            *int   `json:"seed,omitempty"`
	Simplifications string `json:"simplifications,omitempty"`
}

// PairedComparison reports within-task and within-trial differences, avoiding
// an unpaired comparison that could confound mode with task difficulty.
type PairedComparison struct {
	TaskID                 string       `json:"task_id"`
	BaselineMode           harness.Mode `json:"baseline_mode"`
	ComparisonMode         harness.Mode `json:"comparison_mode"`
	Pairs                  int          `json:"pairs"`
	BaselineMeanScore      float64      `json:"baseline_mean_score"`
	ComparisonMeanScore    float64      `json:"comparison_mean_score"`
	MeanScoreDelta         float64      `json:"mean_score_delta"`
	MeanSuccessDelta       float64      `json:"mean_success_delta"`
	MeanTokenDelta         float64      `json:"mean_token_delta"`
	MeanInferenceCallDelta float64      `json:"mean_inference_call_delta"`
	MeanToolCallDelta      float64      `json:"mean_tool_call_delta"`
	UncertaintyAvailable   bool         `json:"uncertainty_available"`
	ScoreDeltaStdError     *float64     `json:"score_delta_standard_error,omitempty"`
	ScoreDeltaCI95Lower    *float64     `json:"score_delta_ci95_lower,omitempty"`
	ScoreDeltaCI95Upper    *float64     `json:"score_delta_ci95_upper,omitempty"`
}

type Report struct {
	EvalID          string                     `json:"eval_id"`
	ConfigID        string                     `json:"config_id"`
	Suite           string                     `json:"suite"`
	CreatedAt       time.Time                  `json:"created_at"`
	GitCommit       string                     `json:"git_commit,omitempty"`
	Provider        string                     `json:"provider"`
	Model           string                     `json:"model"`
	Evaluator       string                     `json:"evaluator_version"`
	EvaluationClass string                     `json:"evaluation_class"`
	ReasoningEffort string                     `json:"reasoning_effort,omitempty"`
	Temperature     *float64                   `json:"temperature,omitempty"`
	ToolNames       []string                   `json:"tool_names"`
	Budget          harness.Budget             `json:"budget"`
	Verification    harness.VerificationPolicy `json:"verification"`
	Trials          int                        `json:"trials"`
	Modes           []harness.Mode             `json:"modes"`
	Tasks           []TaskSummary              `json:"tasks"`
	Runs            []RunRecord                `json:"runs"`
	Overall         Aggregate                  `json:"overall"`
	ByMode          map[string]Aggregate       `json:"by_mode"`
	Paired          []PairedComparison         `json:"paired_comparisons"`
	Files           map[string]string          `json:"files,omitempty"`
	Notes           []string                   `json:"notes"`
}

func Run(ctx context.Context, options Options) (*Report, error) {
	if options.Provider == nil {
		return nil, errors.New("evaluation provider is required")
	}
	if options.Suite == "" {
		options.Suite = DefaultSuite
	}
	if options.Suite != DefaultSuite && options.Suite != ScienceWorldSuite {
		return nil, fmt.Errorf("unknown evaluation suite %q (available: %s, %s)", options.Suite, DefaultSuite, ScienceWorldSuite)
	}
	tasks := options.Tasks
	if len(tasks) == 0 {
		if options.Suite == ScienceWorldSuite {
			return nil, errors.New("ScienceWorld evaluation requires an explicit task selection")
		}
		tasks = []harness.Task{app.DefaultScienceTask()}
	}
	modes := options.Modes
	if len(modes) == 0 {
		modes = []harness.Mode{harness.Baseline, harness.EvidenceMode, harness.Paradox}
	}
	seenModes := map[harness.Mode]bool{}
	for _, mode := range modes {
		if seenModes[mode] {
			return nil, fmt.Errorf("duplicate mode %q", mode)
		}
		if _, err := harness.ParseMode(string(mode)); err != nil {
			return nil, err
		}
		seenModes[mode] = true
	}
	if options.Trials <= 0 {
		options.Trials = 1
	}
	if options.Budget == (harness.Budget{}) {
		options.Budget = harness.DefaultBudget()
	}
	if options.Verification == (harness.VerificationPolicy{}) {
		options.Verification = harness.DefaultVerificationPolicy()
	}
	if options.RunsDir == "" {
		options.RunsDir = ".paradox/runs"
	}
	if options.OutputDir == "" {
		options.OutputDir = ".paradox/evals"
	}
	if options.EvalID == "" {
		options.EvalID = newEvalID()
	}
	if options.ConfigID == "" {
		options.ConfigID = "ad-hoc"
	}
	if err := validateEvalID(options.EvalID); err != nil {
		return nil, err
	}
	reportDir := filepath.Join(options.OutputDir, options.EvalID)
	if _, err := os.Stat(filepath.Join(reportDir, "report.json")); err == nil {
		return nil, fmt.Errorf("evaluation %s already exists at %s", options.EvalID, reportDir)
	}
	if err := os.MkdirAll(reportDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(reportDir, 0o700); err != nil {
		return nil, err
	}

	report := &Report{
		EvalID: options.EvalID, ConfigID: options.ConfigID, Suite: options.Suite, CreatedAt: time.Now().UTC(),
		GitCommit: options.GitCommit, Provider: options.Provider.Name(),
		Model: options.Provider.Model(), Evaluator: EvaluatorVersion,
		EvaluationClass: "development", ReasoningEffort: options.ReasoningEffort,
		Temperature: options.Temperature, ToolNames: collectToolNames(options.Domain, tasks),
		Budget:       options.Budget,
		Verification: options.Verification, Trials: options.Trials,
		Modes: append([]harness.Mode(nil), modes...),
		Notes: []string{
			"The science task set contains internal development tasks; it is not a held-out scientific benchmark.",
			"The ScienceWorld suite uses official environment rewards and scores when its official Python/Java environment is installed.",
			"All modes use the same task, model, budget, retry policy, and tool timeout.",
			"Cost is aggregated only when the provider reports it or prices are configured; unavailable cost is never estimated.",
			"Reproducibility is reported only when more than one trial exists for a mode; it compares canonical evidence signatures.",
		},
	}
	if options.Suite == ScienceWorldSuite {
		report.EvaluationClass = "external_benchmark"
	}
	for _, task := range tasks {
		report.Tasks = append(report.Tasks, summarizeTask(task))
	}

	var runErr error
	for _, task := range tasks {
		for _, mode := range modes {
			for trial := 1; trial <= options.Trials; trial++ {
				if err := ctx.Err(); err != nil {
					runErr = err
					break
				}
				runID := harness.NewRunID()
				domain := options.Domain
				if domain == nil {
					if task.Domain == scienceworld.DomainName {
						domain = scienceworld.New()
					} else {
						domain = science.New()
					}
				}
				output, err := app.Run(ctx, app.RunOptions{
					Task: task, Mode: mode, Provider: options.Provider, Domain: domain,
					Budget: options.Budget, Verification: options.Verification,
					RunsDir: options.RunsDir, Trial: trial, GitCommit: options.GitCommit,
					ReasoningEffort: options.ReasoningEffort, Temperature: options.Temperature,
					RunID: runID,
					ManifestMetadata: map[string]any{
						"eval_id": options.EvalID, "eval_config_id": options.ConfigID,
						"eval_suite": options.Suite, "eval_trial": trial,
						"evaluator_version": EvaluatorVersion,
					},
				})
				record := RunRecord{
					RunID: runID, TaskID: task.ID, Mode: mode, Trial: trial,
				}
				if err != nil {
					record.Error = err.Error()
					report.Runs = append(report.Runs, record)
					continue
				}
				record.RunDir = output.RunDir
				record.Success = output.Result.Success
				record.Score = output.Result.Score
				record.Error = output.Result.Error
				record.Metrics = measure(output.Result)
				if output.Result.Evaluation != nil {
					record.EvaluationDetails = output.Result.Evaluation.Details
				}
				report.Runs = append(report.Runs, record)
			}
			if runErr != nil {
				break
			}
		}
		if runErr != nil {
			break
		}
	}

	report.ByMode = map[string]Aggregate{}
	reproducibility := map[string]float64{}
	reproducibilityRuns := map[string]int{}
	for _, mode := range modes {
		records := recordsForMode(report.Runs, mode)
		aggregate := aggregateRuns(records)
		aggregate.Reproducibility, aggregate.ReproducibilityRuns = reproducibilityFor(records, options.Trials)
		reproducibility[string(mode)] = aggregate.Reproducibility
		reproducibilityRuns[string(mode)] = aggregate.ReproducibilityRuns
		report.ByMode[string(mode)] = aggregate
	}
	report.Overall = aggregateRuns(report.Runs)
	for mode, value := range reproducibility {
		if runs := reproducibilityRuns[mode]; runs > 0 {
			report.Overall.Reproducibility += value * float64(runs)
			report.Overall.ReproducibilityRuns += runs
		}
	}
	if report.Overall.ReproducibilityRuns > 0 {
		report.Overall.Reproducibility /= float64(report.Overall.ReproducibilityRuns)
	}
	report.Paired = pairedComparisons(report.Runs, modes, tasks)

	report.Files = map[string]string{
		"json": filepath.Join(reportDir, "report.json"),
		"md":   filepath.Join(reportDir, "report.md"),
	}
	if err := writeReport(report, reportDir); err != nil {
		return nil, err
	}
	if runErr != nil {
		return report, runErr
	}
	return report, nil
}

func measure(result *harness.RunResult) RunMetrics {
	if result == nil {
		return RunMetrics{}
	}
	snapshot := result.Snapshot
	metrics := RunMetrics{
		Success: result.Success, Score: result.Score,
		Observations: snapshot.Stats.Observations, Claims: snapshot.Stats.Claims,
		ActiveClaims:         snapshot.Stats.ActiveClaims,
		UnsupportedClaims:    snapshot.Stats.UnsupportedClaims,
		UnsupportedClaimRate: snapshot.Stats.UnsupportedRate,
		Contradictions:       snapshot.Stats.Contradictions,
		Retractions:          snapshot.Stats.Retractions,
		PromptTokens:         result.Usage.PromptTokens, CompletionTokens: result.Usage.CompletionTokens,
		TotalTokens: result.Usage.TotalTokens, CostUSD: result.Usage.CostUSD,
		CostAvailable: result.Usage.CostAvailable, DurationMS: result.DurationMS,
		Steps: result.Steps, InferenceCalls: result.Usage.InferenceCalls,
		ToolCalls: result.Usage.ToolCalls,
	}
	metrics.RetractionsEvaluated, metrics.IncorrectRetractions = assessRetractions(snapshot)
	if metrics.RetractionsEvaluated > 0 {
		metrics.IncorrectRetractionRate = float64(metrics.IncorrectRetractions) / float64(metrics.RetractionsEvaluated)
	}
	metrics.VerificationRecords, metrics.VerificationCorrect, metrics.VerificationIncorrect, metrics.VerificationUnknown = assessVerifications(snapshot)
	if denominator := metrics.VerificationCorrect + metrics.VerificationIncorrect; denominator > 0 {
		metrics.VerificationAccuracy = float64(metrics.VerificationCorrect) / float64(denominator)
	}
	metrics.ReproducibilityHash = evidenceSignature(snapshot)
	return metrics
}

func assessRetractions(snapshot evidence.Snapshot) (evaluated, incorrect int) {
	byID := map[string]evidence.Node{}
	for _, node := range snapshot.Nodes {
		byID[node.ID] = node
	}
	for _, retraction := range snapshot.Retractions {
		if retraction.Cause != "verification" && retraction.Cause != "contradiction" {
			continue
		}
		node, ok := byID[retraction.NodeID]
		if !ok {
			continue
		}
		if node.Kind != evidence.KindAssumption && node.Kind != evidence.KindClaim {
			continue
		}
		for index := len(snapshot.Verifications) - 1; index >= 0; index-- {
			verification := snapshot.Verifications[index]
			if retraction.NodeID != verification.ClaimID && retraction.NodeID != verification.AssumptionID {
				continue
			}
			correct, known := assessVerification(snapshot, verification)
			if known {
				evaluated++
				if !correct {
					incorrect++
				}
			}
			break
		}
	}
	return evaluated, incorrect
}

func assessVerifications(snapshot evidence.Snapshot) (records, correct, incorrect, unknown int) {
	for _, verification := range snapshot.Verifications {
		records++
		isCorrect, known := assessVerification(snapshot, verification)
		if !known {
			unknown++
			continue
		}
		if isCorrect {
			correct++
		} else {
			incorrect++
		}
	}
	return records, correct, incorrect, unknown
}

func assessVerification(snapshot evidence.Snapshot, verification evidence.VerificationRecord) (correct, known bool) {
	byID := map[string]evidence.Node{}
	for _, node := range snapshot.Nodes {
		byID[node.ID] = node
	}
	evidenceNode, ok := byID[verification.EvidenceID]
	if !ok || evidenceNode.Kind != evidence.KindObservation {
		return false, false
	}
	switch verification.Method {
	case "range_exhaustive_counterexample":
		composites := intValue(evidenceNode.Metadata["composite_count"])
		if stringValue(evidenceNode.Metadata["experiment"]) != "primality_scan" {
			return false, false
		}
		return verification.Contradicted == (evidenceNode.ClaimValue == "composite" || composites > 0), true
	case "measurement_stability":
		if stringValue(evidenceNode.Metadata["batch"]) != "replication" {
			return false, false
		}
		dataset := stringValue(evidenceNode.Metadata["dataset"])
		replicationMean, ok := numericValue(evidenceNode.Metadata["mean"])
		if !ok || dataset == "" {
			return false, false
		}
		pilotMean, pilotFound := 0.0, false
		for _, node := range snapshot.Nodes {
			if node.Kind != evidence.KindObservation ||
				stringValue(node.Metadata["batch"]) != "pilot" ||
				stringValue(node.Metadata["dataset"]) != dataset {
				continue
			}
			if value, valid := numericValue(node.Metadata["mean"]); valid {
				pilotMean, pilotFound = value, true
				break
			}
		}
		if !pilotFound {
			return false, false
		}
		tolerance, ok := numericValue(evidenceNode.Metadata["tolerance"])
		if !ok || tolerance < 0 {
			return false, false
		}
		driftObserved := math.Abs(replicationMean-pilotMean) > tolerance
		return verification.Contradicted == driftObserved, true
	default:
		return false, false
	}
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func evidenceSignature(snapshot evidence.Snapshot) string {
	type nodeSignature struct {
		Kind         evidence.Kind         `json:"kind"`
		Text         string                `json:"text"`
		ClaimKey     string                `json:"claim_key,omitempty"`
		ClaimValue   string                `json:"claim_value,omitempty"`
		Verification evidence.Verification `json:"verification"`
		Active       bool                  `json:"active"`
		Internal     bool                  `json:"internal,omitempty"`
	}
	nodes := make([]nodeSignature, 0, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		if node.Internal {
			continue
		}
		nodes = append(nodes, nodeSignature{
			Kind: node.Kind, Text: node.Text, ClaimKey: node.ClaimKey,
			ClaimValue: node.ClaimValue, Verification: node.Verification,
			Active: node.Active, Internal: node.Internal,
		})
	}
	sort.Slice(nodes, func(i, j int) bool {
		left, _ := json.Marshal(nodes[i])
		right, _ := json.Marshal(nodes[j])
		return string(left) < string(right)
	})
	contradictions := make([]string, 0, len(snapshot.Contradictions))
	for _, contradiction := range snapshot.Contradictions {
		contradictions = append(contradictions, contradiction.Reason)
	}
	sort.Strings(contradictions)
	retractions := make([]string, 0, len(snapshot.Retractions))
	for _, retraction := range snapshot.Retractions {
		retractions = append(retractions, retraction.NodeID+"|"+retraction.Reason+"|"+retraction.Cause)
	}
	sort.Strings(retractions)
	verifications := make([]string, 0, len(snapshot.Verifications))
	for _, verification := range snapshot.Verifications {
		verifications = append(verifications, fmt.Sprintf("%s|%s|%t|%s", verification.PlanID, verification.Method, verification.Contradicted, verification.Reason))
	}
	sort.Strings(verifications)
	payload, _ := json.Marshal(map[string]any{
		"nodes": nodes, "contradictions": contradictions,
		"retractions": retractions, "verifications": verifications,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func recordsForMode(records []RunRecord, mode harness.Mode) []RunRecord {
	out := []RunRecord{}
	for _, record := range records {
		if record.Mode == mode {
			out = append(out, record)
		}
	}
	return out
}

func summarizeTask(task harness.Task) TaskSummary {
	summary := TaskSummary{ID: task.ID, Domain: task.Domain, Prompt: task.Prompt}
	if task.Domain == scienceworld.DomainName {
		summary.Benchmark = "ScienceWorld"
	} else if task.Domain == science.DomainName {
		summary.Benchmark = "PARADOX internal science development task"
	}
	summary.TaskName, _ = task.Metadata["task_name"].(string)
	summary.Simplifications, _ = task.Metadata["simplifications"].(string)
	if value, ok := metadataInt(task.Metadata["variation"]); ok {
		summary.Variation = &value
	}
	if value, ok := metadataInt(task.Metadata["seed"]); ok {
		summary.Seed = &value
	}
	return summary
}

func metadataInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		if math.Trunc(typed) == typed {
			return int(typed), true
		}
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	}
	return 0, false
}

func collectToolNames(domain harness.DomainAdapter, tasks []harness.Task) []string {
	seen := map[string]bool{"submit_claim": true, "finish": true}
	for _, task := range tasks {
		adapter := domain
		if adapter == nil {
			if task.Domain == scienceworld.DomainName {
				adapter = scienceworld.New()
			} else {
				adapter = science.New()
			}
		}
		tools := adapter.Tools()
		if taskAdapter, ok := adapter.(harness.TaskToolAdapter); ok {
			tools = taskAdapter.ToolsForTask(task)
		}
		for _, tool := range tools {
			seen[tool.Name] = true
		}
	}
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func pairedComparisons(records []RunRecord, modes []harness.Mode, tasks []harness.Task) []PairedComparison {
	baselinePresent := false
	for _, mode := range modes {
		baselinePresent = baselinePresent || mode == harness.Baseline
	}
	if !baselinePresent {
		return nil
	}
	comparisons := []PairedComparison{}
	for _, mode := range modes {
		if mode == harness.Baseline {
			continue
		}
		comparisons = append(comparisons, pairedGroup("*", records, harness.Baseline, mode))
		for _, task := range tasks {
			comparisons = append(comparisons, pairedGroup(task.ID, recordsForTask(records, task.ID), harness.Baseline, mode))
		}
	}
	return comparisons
}

func recordsForTask(records []RunRecord, taskID string) []RunRecord {
	filtered := make([]RunRecord, 0)
	for _, record := range records {
		if record.TaskID == taskID {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

func pairedGroup(taskID string, records []RunRecord, baseline, comparison harness.Mode) PairedComparison {
	result := PairedComparison{TaskID: taskID, BaselineMode: baseline, ComparisonMode: comparison}
	base := map[string]RunRecord{}
	other := map[string]RunRecord{}
	for _, record := range records {
		if record.Error != "" {
			continue
		}
		key := fmt.Sprintf("%s:%d", record.TaskID, record.Trial)
		if record.Mode == baseline {
			base[key] = record
		} else if record.Mode == comparison {
			other[key] = record
		}
	}
	var deltas []float64
	var scoreBase, scoreOther, successDelta, tokenDelta, inferenceDelta, toolDelta float64
	keys := make([]string, 0, len(base))
	for key := range base {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		baseRun := base[key]
		otherRun, ok := other[key]
		if !ok {
			continue
		}
		result.Pairs++
		scoreBase += baseRun.Score
		scoreOther += otherRun.Score
		delta := otherRun.Score - baseRun.Score
		deltas = append(deltas, delta)
		if otherRun.Success {
			successDelta++
		}
		if baseRun.Success {
			successDelta--
		}
		tokenDelta += float64(otherRun.Metrics.TotalTokens - baseRun.Metrics.TotalTokens)
		inferenceDelta += float64(otherRun.Metrics.InferenceCalls - baseRun.Metrics.InferenceCalls)
		toolDelta += float64(otherRun.Metrics.ToolCalls - baseRun.Metrics.ToolCalls)
	}
	if result.Pairs == 0 {
		return result
	}
	count := float64(result.Pairs)
	result.BaselineMeanScore = scoreBase / count
	result.ComparisonMeanScore = scoreOther / count
	result.MeanScoreDelta = result.ComparisonMeanScore - result.BaselineMeanScore
	result.MeanSuccessDelta = successDelta / count
	result.MeanTokenDelta = tokenDelta / count
	result.MeanInferenceCallDelta = inferenceDelta / count
	result.MeanToolCallDelta = toolDelta / count
	if result.Pairs >= 2 {
		mean := result.MeanScoreDelta
		var sumSquares float64
		for _, delta := range deltas {
			difference := delta - mean
			sumSquares += difference * difference
		}
		standardError := math.Sqrt(sumSquares / float64(result.Pairs-1) / count)
		critical := studentT95(result.Pairs - 1)
		margin := critical * standardError
		lower, upper := mean-margin, mean+margin
		result.UncertaintyAvailable = true
		result.ScoreDeltaStdError = &standardError
		result.ScoreDeltaCI95Lower = &lower
		result.ScoreDeltaCI95Upper = &upper
	}
	return result
}

func studentT95(degreesOfFreedom int) float64 {
	critical := []float64{0, 12.706, 4.303, 3.182, 2.776, 2.571, 2.447, 2.365, 2.306, 2.262, 2.228, 2.201, 2.179, 2.160, 2.145, 2.131, 2.120, 2.110, 2.101, 2.093, 2.086, 2.080, 2.074, 2.069, 2.064, 2.060, 2.056, 2.052, 2.048, 2.045, 2.042}
	if degreesOfFreedom >= len(critical) {
		return 1.96
	}
	return critical[degreesOfFreedom]
}

func aggregateRuns(records []RunRecord) Aggregate {
	aggregate := Aggregate{Runs: len(records)}
	if len(records) == 0 {
		return aggregate
	}
	aggregate.CostComplete = true
	var scoreSum float64
	for _, record := range records {
		metrics := record.Metrics
		if record.Success {
			aggregate.Successes++
		}
		scoreSum += record.Score
		aggregate.Observations += metrics.Observations
		aggregate.Claims += metrics.Claims
		aggregate.ActiveClaims += metrics.ActiveClaims
		aggregate.UnsupportedClaims += metrics.UnsupportedClaims
		aggregate.Contradictions += metrics.Contradictions
		aggregate.Retractions += metrics.Retractions
		aggregate.RetractionsEvaluated += metrics.RetractionsEvaluated
		aggregate.IncorrectRetractions += metrics.IncorrectRetractions
		aggregate.VerificationRecords += metrics.VerificationRecords
		aggregate.VerificationCorrect += metrics.VerificationCorrect
		aggregate.VerificationIncorrect += metrics.VerificationIncorrect
		aggregate.VerificationUnknown += metrics.VerificationUnknown
		aggregate.PromptTokens += metrics.PromptTokens
		aggregate.CompletionTokens += metrics.CompletionTokens
		aggregate.TotalTokens += metrics.TotalTokens
		aggregate.CostUSD += metrics.CostUSD
		aggregate.TotalDurationMS += metrics.DurationMS
		aggregate.InferenceCalls += metrics.InferenceCalls
		aggregate.ToolCalls += metrics.ToolCalls
		if !metrics.CostAvailable {
			aggregate.CostComplete = false
		}
	}
	aggregate.SuccessRate = float64(aggregate.Successes) / float64(aggregate.Runs)
	aggregate.MeanScore = scoreSum / float64(aggregate.Runs)
	aggregate.MeanDurationMS = float64(aggregate.TotalDurationMS) / float64(aggregate.Runs)
	if aggregate.ActiveClaims > 0 {
		aggregate.UnsupportedClaimRate = float64(aggregate.UnsupportedClaims) / float64(aggregate.ActiveClaims)
	}
	if aggregate.RetractionsEvaluated > 0 {
		aggregate.IncorrectRetractionRate = float64(aggregate.IncorrectRetractions) / float64(aggregate.RetractionsEvaluated)
	}
	if denominator := aggregate.VerificationCorrect + aggregate.VerificationIncorrect; denominator > 0 {
		aggregate.VerificationAccuracy = float64(aggregate.VerificationCorrect) / float64(denominator)
	}
	var steps int
	for _, record := range records {
		steps += record.Metrics.Steps
	}
	aggregate.MeanSteps = float64(steps) / float64(aggregate.Runs)
	return aggregate
}

func reproducibilityFor(records []RunRecord, trials int) (float64, int) {
	if trials <= 1 || len(records) <= 1 {
		return 0, 0
	}
	counts := map[string]int{}
	for _, record := range records {
		counts[record.Metrics.ReproducibilityHash]++
	}
	best := 0
	for _, count := range counts {
		if count > best {
			best = count
		}
	}
	return float64(best) / float64(len(records)), len(records)
}

func newEvalID() string {
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	return "eval-" + time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(buf[:])
}

func validateEvalID(value string) error {
	if strings.TrimSpace(value) != value || value == "" || value == "." || value == ".." {
		return errors.New("evaluation id must be a non-empty filename component")
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return fmt.Errorf("evaluation id contains unsupported character %q", char)
	}
	return nil
}

func writeReport(report *Report, dir string) error {
	if err := writeJSONAtomic(filepath.Join(dir, "report.json"), report); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(RenderMarkdown(report)), 0o600)
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".eval-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	default:
		return 0
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

// FormatPercent renders a ratio for reports without implying precision that the
// sample size does not support.
func FormatPercent(value float64) string {
	if value < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", value*100)
}

func modeNames(modes []harness.Mode) string {
	names := make([]string, 0, len(modes))
	for _, mode := range modes {
		names = append(names, string(mode))
	}
	return strings.Join(names, ", ")
}
