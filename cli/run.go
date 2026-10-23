package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/qeinstein/paradox/app"
	"github.com/qeinstein/paradox/domain/science"
	"github.com/qeinstein/paradox/evidence"
	"github.com/qeinstein/paradox/harness"
)

func runCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	provider := defaultProviderFlags()
	addProviderFlags(fs, &provider)
	budget := defaultBudgetFlags()
	addBudgetFlags(fs, &budget)
	taskOptions := taskFlags{}
	addTaskFlags(fs, &taskOptions)

	mode := fs.String("mode", "paradox", "operating mode: baseline, evidence, or paradox")
	runsDir := fs.String("runs-dir", defaultRunsDir(), "directory for run artifacts")
	jsonOutput := fs.Bool("json", false, "print a machine-readable JSON result")
	allowFailure := fs.Bool("allow-failure", false, "exit 0 even when the domain evaluation does not pass")
	reasoningEffort := fs.String("reasoning-effort", "", "provider reasoning effort (for example low, medium, high)")
	temperature := fs.Float64("temperature", -1, "sampling temperature; negative leaves the provider default")
	verify := fs.Bool("verify", true, "enable executable verification plans in paradox mode")
	minAssumptionConfidence := fs.Float64("min-assumption-confidence", 0.95, "only actively verify assumptions at or below this confidence")
	maxPlans := fs.Int("max-plans", 2, "maximum verification plans per claim")
	verificationTimeout := fs.Duration("verification-timeout", time.Duration(harness.DefaultVerificationPolicy().TimeoutMS)*time.Millisecond, "verification timeout")
	runID := fs.String("run-id", "", "optional run id (default: generated)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "run: unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}

	task, err := taskOptions.task()
	if err != nil {
		fmt.Fprintf(stderr, "run: %v\n", err)
		return 2
	}
	modeValue, err := harness.ParseMode(*mode)
	if err != nil {
		fmt.Fprintf(stderr, "run: %v\n", err)
		return 2
	}
	modelProvider, _, err := buildProvider(provider)
	if err != nil {
		fmt.Fprintf(stderr, "run: %v\n", err)
		return 2
	}
	verification := harness.DefaultVerificationPolicy()
	verification.Enabled = *verify
	verification.MinAssumptionConfidence = *minAssumptionConfidence
	verification.MaxPlansPerClaim = *maxPlans
	verification.TimeoutMS = verificationTimeout.Milliseconds()

	var temperaturePointer *float64
	if *temperature >= 0 {
		temperaturePointer = temperature
	}
	output, err := app.Run(ctx, app.RunOptions{
		Task: task, Mode: modeValue, Provider: modelProvider, Domain: science.New(),
		Budget: budget.budget(), Verification: verification, RunsDir: *runsDir,
		GitCommit: app.GitCommit(), ReasoningEffort: strings.TrimSpace(*reasoningEffort),
		Temperature: temperaturePointer, RunID: strings.TrimSpace(*runID),
	})
	if err != nil {
		fmt.Fprintf(stderr, "run: %v\n", err)
		return 1
	}
	if *jsonOutput {
		payload := struct {
			Result   *harness.RunResult  `json:"result"`
			Manifest harness.RunManifest `json:"manifest"`
			RunDir   string              `json:"run_dir"`
		}{Result: output.Result, Manifest: output.Manifest, RunDir: output.RunDir}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(payload); err != nil {
			fmt.Fprintf(stderr, "run: write output: %v\n", err)
			return 1
		}
	} else {
		printRunSummary(stdout, output)
	}
	if output.Result == nil {
		return 1
	}
	if output.Result.Error != "" {
		return 1
	}
	if !output.Result.Success && !*allowFailure {
		return 1
	}
	return 0
}

func printRunSummary(w io.Writer, output *app.RunOutput) {
	result := output.Result
	manifest := output.Manifest
	fmt.Fprintf(w, "Run %s\n", result.RunID)
	fmt.Fprintf(w, "  task:       %s (%s)\n", manifest.TaskID, manifest.Domain)
	fmt.Fprintf(w, "  mode:       %s\n", manifest.Mode)
	fmt.Fprintf(w, "  provider:   %s / %s\n", manifest.Provider, manifest.Model)
	fmt.Fprintf(w, "  status:     %s\n", statusWord(result))
	if result.Evaluation != nil {
		fmt.Fprintf(w, "  evaluation: score=%.3f success=%t\n", result.Evaluation.Score, result.Evaluation.Success)
	}
	if result.Error != "" {
		fmt.Fprintf(w, "  error:      %s\n", result.Error)
	}
	fmt.Fprintf(w, "  duration:   %d ms, steps=%d\n", result.DurationMS, result.Steps)
	fmt.Fprintf(w, "  tokens:     prompt=%d completion=%d total=%d\n",
		result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens)
	fmt.Fprintf(w, "  cost:       %s\n", formatCost(result.Usage))
	fmt.Fprintf(w, "  calls:      inference=%d tools=%d\n", result.Usage.InferenceCalls, result.Usage.ToolCalls)
	stats := result.Snapshot.Stats
	fmt.Fprintf(w, "  evidence:   observations=%d claims=%d active=%d unsupported=%d contradictions=%d retractions=%d\n",
		stats.Observations, stats.Claims, stats.ActiveClaims, stats.UnsupportedClaims,
		stats.Contradictions, stats.Retractions)
	if result.Evaluation != nil && len(result.Evaluation.Details) > 0 {
		fmt.Fprintf(w, "  checks:     %s\n", formatDetails(result.Evaluation.Details))
	}
	if names := artifactNames(result.Snapshot); len(names) > 0 {
		fmt.Fprintf(w, "  artifacts:  %s\n", strings.Join(names, ", "))
	}
	if result.FinalText != "" {
		fmt.Fprintf(w, "  conclusion: %s\n", truncateText(result.FinalText, 500))
	}
	fmt.Fprintf(w, "  run dir:    %s\n", output.RunDir)
}

func statusWord(result *harness.RunResult) string {
	if result == nil {
		return "unknown"
	}
	if result.Error != "" {
		return "error"
	}
	if result.Success {
		return "success"
	}
	return "not passed"
}

func formatCost(usage harness.Usage) string {
	if !usage.CostAvailable {
		return "n/a (provider did not report cost)"
	}
	return fmt.Sprintf("$%.6f", usage.CostUSD)
}

func formatDetails(details map[string]any) string {
	keys := make([]string, 0, len(details))
	for key := range details {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", key, details[key]))
	}
	return strings.Join(parts, " ")
}

func artifactNames(snapshot evidence.Snapshot) []string {
	seen := map[string]bool{}
	names := []string{}
	for _, node := range snapshot.Nodes {
		for _, artifact := range node.Artifacts {
			if artifact.Name == "" || seen[artifact.Name] {
				continue
			}
			seen[artifact.Name] = true
			names = append(names, artifact.Name)
		}
	}
	sort.Strings(names)
	return names
}

func truncateText(value string, limit int) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}
