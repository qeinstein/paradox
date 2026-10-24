package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/qeinstein/paradox/app"
	"github.com/qeinstein/paradox/benchmark/scienceworld"
	"github.com/qeinstein/paradox/domain/science"
	"github.com/qeinstein/paradox/eval"
	"github.com/qeinstein/paradox/harness"
)

func evalCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(stderr)
	providerFlagsValue := defaultProviderFlags()
	addProviderFlags(fs, &providerFlagsValue)
	configPath := fs.String("config", "", "credential-free reproducible evaluation JSON configuration; its run settings override corresponding flags")
	budgetFlagsValue := defaultBudgetFlags()
	addBudgetFlags(fs, &budgetFlagsValue)
	taskOptions := taskFlags{}
	addTaskFlags(fs, &taskOptions)

	suite := fs.String("suite", eval.DefaultSuite, "evaluation suite (science or scienceworld)")
	scienceWorldTask := fs.String("benchmark-task", "", "official ScienceWorld task name (required for --suite scienceworld)")
	scienceWorldVariation := fs.Int("variation", 0, "official ScienceWorld variation index")
	scienceWorldSimplifications := fs.String("simplifications", "", "official ScienceWorld simplification string")
	scienceWorldStepLimit := fs.Int("step-limit", 100, "maximum official ScienceWorld environment steps per episode")
	modesValue := fs.String("modes", "baseline,evidence,paradox", "comma-separated modes, or all")
	trials := fs.Int("trials", 3, "trials per mode")
	outputDir := fs.String("output", defaultEvalDir(), "directory for evaluation reports")
	runsDir := fs.String("runs-dir", defaultRunsDir(), "directory for run artifacts")
	evalID := fs.String("eval-id", "", "optional evaluation id")
	jsonOutput := fs.Bool("json", false, "print the report as JSON")
	markdownPath := fs.String("markdown", "", "also write the Markdown report to this path, or - for stdout")
	allowErrors := fs.Bool("allow-errors", false, "exit 0 even when individual runs fail")
	reasoningEffort := fs.String("reasoning-effort", "", "provider reasoning effort")
	temperature := fs.Float64("temperature", -1, "sampling temperature; negative leaves the provider default")
	verify := fs.Bool("verify", true, "enable executable verification in paradox mode")
	minAssumptionConfidence := fs.Float64("min-assumption-confidence", 0.95, "only actively verify assumptions at or below this confidence")
	maxPlans := fs.Int("max-plans", 2, "maximum verification plans per claim")
	verificationTimeout := fs.Duration("verification-timeout", time.Duration(harness.DefaultVerificationPolicy().TimeoutMS)*time.Millisecond, "verification timeout")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "eval: unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}
	var configuration *eval.Config
	if strings.TrimSpace(*configPath) != "" {
		loaded, err := eval.LoadConfig(strings.TrimSpace(*configPath))
		if err != nil {
			fmt.Fprintf(stderr, "eval: %v\n", err)
			return 2
		}
		configuration = &loaded
		*suite = loaded.Suite
		*modesValue = joinModes(loaded.Modes)
		*trials = loaded.Trials
		providerFlagsValue.name, providerFlagsValue.model = loaded.Provider, loaded.Model
		providerFlagsValue.baseURL = loaded.BaseURL
		providerFlagsValue.openRouterToolCalls = loaded.OpenRouterToolCallsSupported
		providerFlagsValue.openRouterReasoning = loaded.OpenRouterReasoningSupported
		providerFlagsValue.openRouterStructuredOutput = loaded.OpenRouterStructuredOutputSupported
		budgetFlagsValue = budgetFlags{
			maxSteps: loaded.Budget.MaxSteps, maxToolCalls: loaded.Budget.MaxToolCalls,
			maxTokens: loaded.Budget.MaxTokens, maxCost: loaded.Budget.MaxCostUSD,
			timeout:     time.Duration(loaded.Budget.TimeoutMS) * time.Millisecond,
			toolTimeout: time.Duration(loaded.Budget.ToolTimeoutMS) * time.Millisecond,
			maxRetries:  loaded.Budget.MaxRetries, requireCost: loaded.Budget.RequireCost,
		}
		*reasoningEffort = loaded.ReasoningEffort
		*temperature = -1
		if loaded.Temperature != nil {
			*temperature = *loaded.Temperature
		}
		*verify = loaded.Verification.Enabled
		*minAssumptionConfidence = loaded.Verification.MinAssumptionConfidence
		*maxPlans = loaded.Verification.MaxPlansPerClaim
		*verificationTimeout = time.Duration(loaded.Verification.TimeoutMS) * time.Millisecond
	}
	var task harness.Task
	var domain harness.DomainAdapter
	var tasks []harness.Task
	if configuration != nil {
		var err error
		tasks, domain, err = configuration.TasksAndDomain()
		if err != nil {
			fmt.Fprintf(stderr, "eval: %v\n", err)
			return 2
		}
	} else if *suite == eval.ScienceWorldSuite {
		if taskOptions.file != "" || taskOptions.id != "" || taskOptions.prompt != "" || taskOptions.expression != "" {
			fmt.Fprintln(stderr, "eval: use --benchmark-task and ScienceWorld task options with --suite scienceworld; science task flags are not applicable")
			return 2
		}
		if strings.TrimSpace(*scienceWorldTask) == "" {
			fmt.Fprintln(stderr, "eval: --benchmark-task is required for --suite scienceworld")
			return 2
		}
		if *scienceWorldVariation < 0 || *scienceWorldStepLimit <= 0 {
			fmt.Fprintln(stderr, "eval: ScienceWorld variation must be non-negative and step limit must be positive")
			return 2
		}
		task = scienceworld.NewTask(strings.TrimSpace(*scienceWorldTask), *scienceWorldVariation, *scienceWorldSimplifications, *scienceWorldStepLimit)
		tasks = []harness.Task{task}
		domain = scienceworld.New()
	} else {
		var err error
		task, err = taskOptions.task()
		if err != nil {
			fmt.Fprintf(stderr, "eval: %v\n", err)
			return 2
		}
		tasks = []harness.Task{task}
		domain = science.New()
	}
	modes, err := parseModes(*modesValue)
	if err != nil {
		fmt.Fprintf(stderr, "eval: %v\n", err)
		return 2
	}
	modelProvider, _, err := buildProvider(providerFlagsValue)
	if err != nil {
		fmt.Fprintf(stderr, "eval: %v\n", err)
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
	configID := ""
	if configuration != nil {
		configID = configuration.ID
	}
	report, runErr := eval.Run(ctx, eval.Options{
		ConfigID: configID, Suite: *suite, Tasks: tasks, Modes: modes, Trials: *trials,
		Provider: modelProvider, Domain: domain,
		Budget: budgetFlagsValue.budget(), Verification: verification,
		RunsDir: *runsDir, OutputDir: *outputDir, EvalID: strings.TrimSpace(*evalID),
		GitCommit: app.GitCommit(), ReasoningEffort: strings.TrimSpace(*reasoningEffort),
		Temperature: temperaturePointer,
	})
	if report == nil {
		fmt.Fprintf(stderr, "eval: %v\n", runErr)
		return 1
	}
	if *markdownPath != "" {
		markdown := eval.RenderMarkdown(report)
		if *markdownPath == "-" {
			fmt.Fprint(stdout, markdown)
		} else if err := os.WriteFile(*markdownPath, []byte(markdown), 0o600); err != nil {
			fmt.Fprintf(stderr, "eval: write markdown: %v\n", err)
			return 1
		}
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintf(stderr, "eval: %v\n", err)
			return 1
		}
	} else {
		printEvalSummary(stdout, report)
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "eval: %v\n", runErr)
		return 1
	}
	if !*allowErrors {
		for _, run := range report.Runs {
			if run.Error != "" {
				return 1
			}
		}
	}
	return 0
}

func joinModes(modes []harness.Mode) string {
	values := make([]string, 0, len(modes))
	for _, mode := range modes {
		values = append(values, string(mode))
	}
	return strings.Join(values, ",")
}

func parseModes(value string) ([]harness.Mode, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "all" {
		return []harness.Mode{harness.Baseline, harness.EvidenceMode, harness.Paradox}, nil
	}
	modes := []harness.Mode{}
	seen := map[harness.Mode]bool{}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		mode, err := harness.ParseMode(item)
		if err != nil {
			return nil, err
		}
		if !seen[mode] {
			modes = append(modes, mode)
			seen[mode] = true
		}
	}
	if len(modes) == 0 {
		return nil, errors.New("no modes selected")
	}
	return modes, nil
}

func printEvalSummary(w io.Writer, report *eval.Report) {
	fmt.Fprintf(w, "Evaluation %s\n", report.EvalID)
	fmt.Fprintf(w, "  suite:    %s\n", report.Suite)
	fmt.Fprintf(w, "  provider: %s / %s\n", report.Provider, report.Model)
	fmt.Fprintf(w, "  trials:   %d per mode\n", report.Trials)
	fmt.Fprintf(w, "  runs:     %d\n", report.Overall.Runs)
	if report.GitCommit != "" {
		fmt.Fprintf(w, "  commit:   %s\n", report.GitCommit)
	}
	fmt.Fprintln(w)
	writer := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "MODE\tRUNS\tSUCCESS\tSCORE\tUNSUPPORTED\tBAD RETRACTIONS\tVERIFICATION\tREPRODUCIBLE\tTOKENS\tCOST\tMEAN TIME")
	for _, row := range eval.SummaryRows(report) {
		fmt.Fprintf(writer, "%s\t%d\t%s\t%.3f\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			row.Mode, row.Runs, eval.FormatPercent(row.SuccessRate), row.MeanScore,
			row.Unsupported, row.IncorrectRetr, row.Verification, row.Reproducible,
			row.Tokens, row.Cost, row.MeanTime)
	}
	_ = writer.Flush()
	if report.Files["json"] != "" {
		fmt.Fprintf(w, "\nReport: %s\n", report.Files["json"])
	}
	if report.Files["md"] != "" {
		fmt.Fprintf(w, "Markdown: %s\n", report.Files["md"])
	}
	failures := 0
	for _, run := range report.Runs {
		if run.Error != "" {
			failures++
		}
	}
	if failures > 0 {
		fmt.Fprintf(w, "Execution errors: %d\n", failures)
	}
}
