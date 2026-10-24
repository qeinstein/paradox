package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/qeinstein/paradox/app"
	"github.com/qeinstein/paradox/benchmark/scienceworld"
	"github.com/qeinstein/paradox/domain/science"
	"github.com/qeinstein/paradox/harness"
)

// Config is a credential-free, reproducible recipe. API keys remain external
// environment configuration and are never accepted in evaluation manifests.
type Config struct {
	ID                                  string                     `json:"id"`
	Suite                               string                     `json:"suite"`
	Provider                            string                     `json:"provider"`
	Model                               string                     `json:"model"`
	BaseURL                             string                     `json:"base_url,omitempty"`
	ReasoningEffort                     string                     `json:"reasoning_effort,omitempty"`
	Temperature                         *float64                   `json:"temperature,omitempty"`
	Modes                               []harness.Mode             `json:"modes"`
	Trials                              int                        `json:"trials"`
	TaskIDs                             []string                   `json:"task_ids,omitempty"`
	BenchmarkTask                       string                     `json:"benchmark_task,omitempty"`
	Variation                           int                        `json:"variation,omitempty"`
	Simplifications                     string                     `json:"simplifications,omitempty"`
	StepLimit                           int                        `json:"step_limit,omitempty"`
	Budget                              harness.Budget             `json:"budget"`
	Verification                        harness.VerificationPolicy `json:"verification"`
	OpenRouterToolCallsSupported        bool                       `json:"openrouter_tool_calls_supported,omitempty"`
	OpenRouterReasoningSupported        bool                       `json:"openrouter_reasoning_supported,omitempty"`
	OpenRouterStructuredOutputSupported bool                       `json:"openrouter_structured_output_supported,omitempty"`
}

func DefaultConfig() Config {
	return Config{
		ID: "internal-science-v0.2-development", Suite: DefaultSuite,
		Provider: "mock", Model: "mock-science-v1",
		Modes:  []harness.Mode{harness.Baseline, harness.EvidenceMode, harness.Paradox},
		Trials: 1,
		TaskIDs: []string{
			science.TaskPrimality, science.TaskPermutationTest,
			science.TaskOptimization, science.TaskMeasurement,
		},
		Budget: harness.Budget{
			MaxSteps: 20, MaxToolCalls: 40, MaxTokens: 20000,
			MaxCostUSD: 1, TimeoutMS: 120000, MaxRetries: 0, ToolTimeoutMS: 30000,
		},
		Verification: harness.DefaultVerificationPolicy(),
	}
}

func LoadConfig(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode evaluation config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("evaluation config must contain one JSON object")
		}
		return Config{}, fmt.Errorf("decode trailing evaluation config data: %w", err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.ID) != c.ID {
		return errors.New("evaluation config id is required")
	}
	if c.Suite != DefaultSuite && c.Suite != ScienceWorldSuite {
		return fmt.Errorf("unsupported evaluation suite %q", c.Suite)
	}
	switch c.Provider {
	case "mock", "meta", "openrouter", "openai-compatible":
	default:
		return fmt.Errorf("unsupported evaluation provider %q", c.Provider)
	}
	if strings.TrimSpace(c.Model) == "" || strings.ContainsAny(c.Model, "\r\n\t ") {
		return errors.New("evaluation config model must be non-empty and contain no whitespace")
	}
	if c.Provider == "openrouter" && !strings.Contains(c.Model, "/") {
		return errors.New("OpenRouter evaluation model ID must use the provider/model form")
	}
	if c.Provider == "openai-compatible" && strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("OpenAI-compatible evaluation config requires base_url")
	}
	if c.Provider == "openrouter" && !c.OpenRouterToolCallsSupported {
		return errors.New("OpenRouter evaluation config must explicitly confirm tool support with openrouter_tool_calls_supported")
	}
	if c.Provider == "openrouter" && c.ReasoningEffort != "" && !c.OpenRouterReasoningSupported {
		return errors.New("OpenRouter evaluation config must explicitly confirm reasoning support for reasoning_effort")
	}
	if c.Trials < 1 || c.Trials > 1000 {
		return errors.New("evaluation config trials must be between 1 and 1000")
	}
	if len(c.Modes) == 0 {
		return errors.New("evaluation config must select at least one mode")
	}
	seen := map[harness.Mode]bool{}
	for _, mode := range c.Modes {
		if _, err := harness.ParseMode(string(mode)); err != nil {
			return err
		}
		if seen[mode] {
			return fmt.Errorf("duplicate evaluation mode %q", mode)
		}
		seen[mode] = true
	}
	if c.Budget.MaxSteps <= 0 || c.Budget.MaxSteps > 1000 || c.Budget.MaxToolCalls <= 0 || c.Budget.MaxToolCalls > 10000 ||
		c.Budget.MaxTokens <= 0 || c.Budget.MaxTokens > 10_000_000 || c.Budget.MaxCostUSD <= 0 || c.Budget.TimeoutMS <= 0 ||
		c.Budget.TimeoutMS > 24*60*60*1000 || c.Budget.ToolTimeoutMS <= 0 || c.Budget.ToolTimeoutMS > 24*60*60*1000 ||
		c.Budget.MaxRetries < 0 || c.Budget.MaxRetries > 10 {
		return errors.New("evaluation config must set positive bounded steps, tools, tokens, cost, and timeout, with non-negative retries")
	}
	if c.Verification.MaxPlansPerClaim < 0 || c.Verification.TimeoutMS < 0 || c.Verification.MinAssumptionConfidence < 0 || c.Verification.MinAssumptionConfidence > 1 {
		return errors.New("evaluation config verification settings are invalid")
	}
	if c.Suite == DefaultSuite {
		if len(c.TaskIDs) == 0 || c.BenchmarkTask != "" {
			return errors.New("science evaluation config requires task_ids and cannot select a ScienceWorld task")
		}
		known := map[string]bool{science.TaskPrimality: true}
		for _, task := range science.Tasks() {
			known[task.ID] = true
		}
		seenTasks := map[string]bool{}
		for _, taskID := range c.TaskIDs {
			if !known[taskID] {
				return fmt.Errorf("unknown internal science task %q", taskID)
			}
			if seenTasks[taskID] {
				return fmt.Errorf("duplicate task ID %q", taskID)
			}
			seenTasks[taskID] = true
		}
	} else if c.BenchmarkTask == "" || c.Variation < 0 || c.StepLimit < 1 || c.StepLimit > 1000 || len(c.TaskIDs) != 0 {
		return errors.New("ScienceWorld config requires benchmark_task, non-negative variation, a 1-1000 step_limit, and no task_ids")
	}
	if c.Temperature != nil && (*c.Temperature < 0 || *c.Temperature > 2) {
		return errors.New("evaluation temperature must be between 0 and 2")
	}
	return nil
}

func (c Config) TasksAndDomain() ([]harness.Task, harness.DomainAdapter, error) {
	if err := c.Validate(); err != nil {
		return nil, nil, err
	}
	if c.Suite == ScienceWorldSuite {
		return []harness.Task{scienceworld.NewTask(c.BenchmarkTask, c.Variation, c.Simplifications, c.StepLimit)}, scienceworld.New(), nil
	}
	available := map[string]harness.Task{science.TaskPrimality: app.DefaultScienceTask()}
	for _, task := range science.Tasks() {
		available[task.ID] = task
	}
	tasks := make([]harness.Task, 0, len(c.TaskIDs))
	for _, taskID := range c.TaskIDs {
		tasks = append(tasks, available[taskID])
	}
	return tasks, science.New(), nil
}
