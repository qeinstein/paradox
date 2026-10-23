// Package cli implements the paradox command-line interface. Commands write
// machine-readable JSON when --json is set and human-readable summaries
// otherwise. They never print API keys.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/qeinstein/paradox/app"
	"github.com/qeinstein/paradox/harness"
)

const usage = `PARADOX - evidence-grounded, self-correcting agent harness

Usage:
  paradox run       Execute a bounded agent run
  paradox trace     Inspect a persisted execution trace
  paradox evidence  Inspect the evidence graph and retractions
  paradox eval      Run matched baseline/evidence/paradox comparisons
  paradox serve     Start the local API and dashboard
  paradox models    List documented or live provider models
  paradox version   Print build identity

Run "paradox <command> -h" for command-specific flags.

Environment:
  PARADOX_RUNS_DIR       Run artifact directory (default .paradox/runs)
  PARADOX_EVAL_DIR       Evaluation report directory (default .paradox/evals)
  PARADOX_PROVIDER       Default provider: mock, meta, openrouter, openai-compatible
  PARADOX_MODEL          Default model id
  META_API_KEY           Meta Model API key
  OPENROUTER_API_KEY     OpenRouter API key
  PARADOX_API_KEY        API key required by non-loopback "serve" bindings
`

// Run dispatches a CLI invocation and returns a process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, usage)
		return 2
	}
	command := args[0]
	rest := args[1:]
	switch command {
	case "run":
		return runCommand(ctx, rest, stdout, stderr)
	case "trace":
		return traceCommand(rest, stdout, stderr)
	case "evidence":
		return evidenceCommand(rest, stdout, stderr)
	case "eval":
		return evalCommand(ctx, rest, stdout, stderr)
	case "serve":
		return serveCommand(ctx, rest, stdout, stderr)
	case "models":
		return modelsCommand(ctx, rest, stdout, stderr)
	case "version":
		return versionCommand(rest, stdout, stderr)
	case "help", "-h", "--help":
		_, _ = io.WriteString(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", command, usage)
		return 2
	}
}

type providerFlags struct {
	name        string
	model       string
	apiKey      string
	baseURL     string
	inputPrice  float64
	outputPrice float64
}

func addProviderFlags(fs *flag.FlagSet, defaults *providerFlags) {
	fs.StringVar(&defaults.name, "provider", defaults.name, "provider: mock, meta, openrouter, or openai-compatible")
	fs.StringVar(&defaults.model, "model", defaults.model, "model id (required for OpenRouter; defaults to Muse Spark 1.3 for Meta)")
	fs.StringVar(&defaults.apiKey, "api-key", defaults.apiKey, "provider API key (prefer the provider's environment variable)")
	fs.StringVar(&defaults.baseURL, "base-url", defaults.baseURL, "base URL for openai-compatible providers")
	fs.Float64Var(&defaults.inputPrice, "input-price", defaults.inputPrice, "input price in USD per million tokens (openai-compatible cost accounting)")
	fs.Float64Var(&defaults.outputPrice, "output-price", defaults.outputPrice, "output price in USD per million tokens (openai-compatible cost accounting)")
}

func defaultProviderFlags() providerFlags {
	inputPrice, _ := strconv.ParseFloat(os.Getenv("PARADOX_INPUT_PRICE_PER_MILLION"), 64)
	outputPrice, _ := strconv.ParseFloat(os.Getenv("PARADOX_OUTPUT_PRICE_PER_MILLION"), 64)
	return providerFlags{
		name:        envFirst("PARADOX_PROVIDER", "mock"),
		model:       os.Getenv("PARADOX_MODEL"),
		baseURL:     os.Getenv("PARADOX_BASE_URL"),
		inputPrice:  inputPrice,
		outputPrice: outputPrice,
	}
}

func (p providerFlags) resolve() (app.ProviderConfig, error) {
	name := strings.ToLower(strings.TrimSpace(p.name))
	if name == "" {
		name = "mock"
	}
	key := strings.TrimSpace(p.apiKey)
	if key == "" {
		switch name {
		case "meta":
			key = envFirst("META_API_KEY", "MODEL_API_KEY")
		case "openrouter":
			key = os.Getenv("OPENROUTER_API_KEY")
		case "openai-compatible":
			key = envFirst("MODEL_API_KEY", "OPENAI_API_KEY")
		}
	}
	cfg := app.ProviderConfig{
		Name: name, Model: strings.TrimSpace(p.model), APIKey: key,
		BaseURL:              strings.TrimSpace(p.baseURL),
		InputPricePerMillion: p.inputPrice, OutputPricePerMillion: p.outputPrice,
	}
	if name == "mock" && cfg.Model == "" {
		cfg.Model = "mock-science-v1"
	}
	if name == "meta" && cfg.Model == "" {
		cfg.Model = "muse-spark-1.3"
	}
	return cfg, nil
}

func buildProvider(flags providerFlags) (harness.ModelProvider, app.ProviderConfig, error) {
	cfg, err := flags.resolve()
	if err != nil {
		return nil, cfg, err
	}
	modelProvider, err := app.BuildProvider(cfg)
	return modelProvider, cfg, err
}

type budgetFlags struct {
	maxSteps     int
	maxToolCalls int
	maxTokens    int
	maxCost      float64
	timeout      time.Duration
	toolTimeout  time.Duration
	maxRetries   int
	requireCost  bool
}

func addBudgetFlags(fs *flag.FlagSet, defaults *budgetFlags) {
	fs.IntVar(&defaults.maxSteps, "max-steps", defaults.maxSteps, "maximum agent loop steps")
	fs.IntVar(&defaults.maxToolCalls, "max-tool-calls", defaults.maxToolCalls, "maximum tool calls (including verification)")
	fs.IntVar(&defaults.maxTokens, "max-tokens", defaults.maxTokens, "maximum total model tokens")
	fs.Float64Var(&defaults.maxCost, "max-cost", defaults.maxCost, "maximum inference cost in USD")
	fs.DurationVar(&defaults.timeout, "timeout", defaults.timeout, "wall-clock timeout for the run")
	fs.DurationVar(&defaults.toolTimeout, "tool-timeout", defaults.toolTimeout, "timeout for a single tool call")
	fs.IntVar(&defaults.maxRetries, "max-retries", defaults.maxRetries, "provider retries per model call")
	fs.BoolVar(&defaults.requireCost, "require-cost", defaults.requireCost, "fail when the provider does not report cost")
}

func defaultBudgetFlags() budgetFlags {
	budget := harness.DefaultBudget()
	return budgetFlags{
		maxSteps: budget.MaxSteps, maxToolCalls: budget.MaxToolCalls,
		maxTokens: budget.MaxTokens, maxCost: budget.MaxCostUSD,
		timeout: budget.Timeout(), toolTimeout: budget.ToolTimeout(),
		maxRetries: budget.MaxRetries, requireCost: budget.RequireCost,
	}
}

func (b budgetFlags) budget() harness.Budget {
	return harness.Budget{
		MaxSteps: b.maxSteps, MaxToolCalls: b.maxToolCalls, MaxTokens: b.maxTokens,
		MaxCostUSD: b.maxCost, TimeoutMS: b.timeout.Milliseconds(),
		ToolTimeoutMS: b.toolTimeout.Milliseconds(), MaxRetries: b.maxRetries,
		RequireCost: b.requireCost,
	}
}

type taskFlags struct {
	file       string
	id         string
	prompt     string
	expression string
}

func addTaskFlags(fs *flag.FlagSet, defaults *taskFlags) {
	fs.StringVar(&defaults.file, "task", defaults.file, "JSON task file")
	fs.StringVar(&defaults.id, "task-id", defaults.id, "task id (default: science-primality-v1)")
	fs.StringVar(&defaults.prompt, "prompt", defaults.prompt, "override the task prompt")
	fs.StringVar(&defaults.expression, "expression", defaults.expression, "integer polynomial for the science adapter")
}

func (t taskFlags) task() (harness.Task, error) {
	if strings.TrimSpace(t.file) != "" {
		return app.TaskFromFile(t.file)
	}
	task := app.DefaultScienceTask()
	if strings.TrimSpace(t.id) != "" {
		task.ID = strings.TrimSpace(t.id)
	}
	if strings.TrimSpace(t.prompt) != "" {
		task.Prompt = strings.TrimSpace(t.prompt)
	}
	if strings.TrimSpace(t.expression) != "" {
		if task.Metadata == nil {
			task.Metadata = map[string]any{}
		}
		task.Metadata["expression"] = strings.TrimSpace(t.expression)
	}
	if err := harness.ValidateTask(task); err != nil {
		return harness.Task{}, err
	}
	return task, nil
}

func defaultRunsDir() string {
	if value := strings.TrimSpace(os.Getenv("PARADOX_RUNS_DIR")); value != "" {
		return value
	}
	return ".paradox/runs"
}

func defaultEvalDir() string {
	if value := strings.TrimSpace(os.Getenv("PARADOX_EVAL_DIR")); value != "" {
		return value
	}
	return ".paradox/evals"
}

func envFirst(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}
