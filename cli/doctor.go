package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/qeinstein/paradox/app"
	"github.com/qeinstein/paradox/benchmark/scienceworld"
	"github.com/qeinstein/paradox/domain/science"
	"github.com/qeinstein/paradox/eval"
	"github.com/qeinstein/paradox/harness"
	"github.com/qeinstein/paradox/provider"
)

type doctorCheck struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Required bool   `json:"required"`
	Detail   string `json:"detail"`
}

type dryRunSummary struct {
	BodySHA256 string   `json:"body_sha256"`
	BodyBytes  int      `json:"body_bytes"`
	Tools      []string `json:"tools"`
	Network    bool     `json:"network_contacted"`
}

type doctorReport struct {
	Provider             string         `json:"provider"`
	Model                string         `json:"model"`
	TaskID               string         `json:"task_id"`
	CredentialConfigured bool           `json:"credential_configured"`
	Overall              string         `json:"overall"`
	Checks               []doctorCheck  `json:"checks"`
	DryRun               *dryRunSummary `json:"dry_run,omitempty"`
}

func doctorCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	providerFlagsValue := defaultProviderFlags()
	addProviderFlags(fs, &providerFlagsValue)
	taskOptions := taskFlags{}
	addTaskFlags(fs, &taskOptions)
	budgetFlagsValue := defaultBudgetFlags()
	addBudgetFlags(fs, &budgetFlagsValue)
	runsDir := fs.String("runs-dir", defaultRunsDir(), "local artifact directory to check")
	evalConfigPath := fs.String("eval-config", "", "credential-free evaluation config JSON to validate (default: built-in internal science config)")
	reasoning := fs.String("reasoning-effort", "", "reasoning parameter to validate and serialize")
	dryRun := fs.Bool("dry-run", true, "construct and validate the exact provider request body without network access")
	jsonOutput := fs.Bool("json", false, "print a machine-readable readiness report")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "doctor: unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}

	config, configErr := providerFlagsValue.resolve()
	if config.Name == "" {
		config.Name = "mock"
	}
	if config.Model == "" {
		switch config.Name {
		case "mock":
			config.Model = "mock-science-v1"
		case "meta":
			config.Model = "muse-spark-1.3"
		}
	}
	task, taskErr := taskOptions.task()
	if taskErr != nil {
		task = app.DefaultScienceTask()
	}
	report := doctorReport{
		Provider: config.Name, Model: config.Model, TaskID: task.ID,
		CredentialConfigured: config.APIKey != "",
		Overall:              "PASS", Checks: []doctorCheck{},
	}
	add := func(name, status string, required bool, detail string) {
		report.Checks = append(report.Checks, doctorCheck{Name: name, Status: status, Required: required, Detail: detail})
		if required && status != "PASS" {
			report.Overall = "FAIL"
		}
	}

	if configErr != nil {
		add("provider_configuration", "FAIL", true, sanitizeDoctorError(configErr.Error(), config.APIKey))
	} else if err := validateOfflineProviderConfig(config); err != nil {
		add("provider_configuration", "FAIL", true, sanitizeDoctorError(err.Error(), config.APIKey))
	} else {
		add("provider_configuration", "PASS", true, "provider name, model identifier syntax, and endpoint fields are valid")
	}
	if (config.Name == "meta" || config.Name == "openrouter") && config.APIKey == "" {
		add("credentials", "BLOCKED", false, "credential is not configured; offline preflight does not need credentials and made no inference request")
	} else if config.Name == "meta" || config.Name == "openrouter" {
		add("credentials", "PASS", false, "credential is configured; its value is redacted and no inference request was made")
	} else {
		add("credentials", "PASS", false, "this selected provider does not require an inference credential")
	}
	if taskErr != nil {
		add("task_configuration", "FAIL", true, sanitizeDoctorError(taskErr.Error(), config.APIKey))
	} else {
		add("task_configuration", "PASS", true, "task ID and prompt are valid; evaluator metadata is not serialized into the model request")
	}
	if budgetErr := validateDoctorBudget(budgetFlagsValue.budget()); budgetErr != nil {
		add("resource_limits", "FAIL", true, budgetErr.Error())
	} else {
		add("resource_limits", "PASS", true, "step, tool, token, cost, timeout, and retry limits are positive and bounded")
	}
	if err := checkWritableDirectory(*runsDir); err != nil {
		add("artifact_directory", "FAIL", true, err.Error())
	} else {
		add("artifact_directory", "PASS", true, "configured run directory is writable; probe file used mode 0600")
	}
	if ok, detail := checkPythonExperimentRuntime(ctx); ok {
		add("science_experiment_runtime", "PASS", true, detail)
	} else {
		add("science_experiment_runtime", "FAIL", true, detail)
	}
	if toolErr := validateScienceToolSchemas(task); toolErr != nil {
		add("tool_schemas", "FAIL", true, toolErr.Error())
	} else {
		add("tool_schemas", "PASS", true, "selected science tool parameter schemas parse as JSON and satisfy local validation")
	}
	if task.Domain != science.DomainName {
		add("domain_adapter", "FAIL", true, fmt.Sprintf("CLI doctor supports the science domain; task domain is %q", task.Domain))
	} else {
		add("domain_adapter", "PASS", true, "science adapter is available")
	}
	if status := scienceworld.CheckDependencies(ctx); status.Ready() != nil {
		add("scienceworld_benchmark", "BLOCKED", false, status.Ready().Error())
	} else {
		add("scienceworld_benchmark", "PASS", false, "official Python package and Java runtime are available")
	}
	if config.Name == "openrouter" {
		if config.OpenRouterToolCallsSupported {
			add("model_tool_capability", "PASS", false, "tool calls were explicitly enabled by the operator; independently confirm the selected model lists tools in OpenRouter supported_parameters before live inference")
		} else {
			add("model_tool_capability", "BLOCKED", false, "OpenRouter tool support is model-specific; offline mode cannot verify the selected model against the live catalog. Check supported_parameters for tools, then pass --tool-calls-supported.")
		}
		if strings.TrimSpace(*reasoning) != "" {
			if config.OpenRouterReasoningSupported {
				add("reasoning_capability", "PASS", false, "reasoning parameters were explicitly enabled by the operator; verify this model supports the requested setting before live inference")
			} else {
				add("reasoning_capability", "BLOCKED", false, "the selected OpenRouter model's reasoning support is unconfirmed; check the model catalog and pass --reasoning-supported")
			}
		}
		if config.OpenRouterStructuredOutputSupported {
			add("structured_output_capability", "PASS", false, "structured output was explicitly enabled by the operator; verify response_format support in the model catalog")
		} else {
			add("structured_output_capability", "BLOCKED", false, "structured output remains disabled unless response_format support is confirmed")
		}
	} else if config.Name == "meta" && documentedMetaModel(config.Model) {
		add("model_tool_capability", "PASS", false, "the selected model appears in the locally documented Meta catalog with tool-call capability")
	} else {
		add("model_tool_capability", "BLOCKED", false, "selected model capability is not known from the local documented catalog")
	}
	var evaluationConfig eval.Config
	var evaluationConfigErr error
	if strings.TrimSpace(*evalConfigPath) == "" {
		evaluationConfig = eval.DefaultConfig()
		evaluationConfigErr = evaluationConfig.Validate()
	} else {
		evaluationConfig, evaluationConfigErr = eval.LoadConfig(strings.TrimSpace(*evalConfigPath))
	}
	if evaluationConfigErr != nil {
		add("evaluation_config", "FAIL", true, sanitizeDoctorError(evaluationConfigErr.Error(), config.APIKey))
	} else {
		add("evaluation_config", "PASS", true, fmt.Sprintf("%s selects %d task(s), %d mode(s), and %d trial(s); no credentials are stored in the config", evaluationConfig.ID, len(evaluationConfig.TaskIDs), len(evaluationConfig.Modes), evaluationConfig.Trials))
	}
	if *dryRun && configErr == nil && taskErr == nil {
		body, tools, err := serializeDryRunRequest(config, task, budgetFlagsValue.budget(), strings.TrimSpace(*reasoning))
		if err != nil {
			add("provider_serialization", "FAIL", true, sanitizeDoctorError(err.Error(), config.APIKey))
		} else {
			hash := sha256.Sum256(body)
			report.DryRun = &dryRunSummary{BodySHA256: hex.EncodeToString(hash[:]), BodyBytes: len(body), Tools: tools, Network: false}
			add("provider_serialization", "PASS", true, "exact provider request body constructed locally and parsed; no HTTP request was made")
		}
	} else {
		add("provider_serialization", "BLOCKED", true, "request dry-run was disabled")
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintf(stderr, "doctor: encode report: %v\n", err)
			return 1
		}
	} else {
		fmt.Fprintf(stdout, "PARADOX offline doctor: %s\n", report.Overall)
		fmt.Fprintf(stdout, "  provider/model: %s / %s\n", report.Provider, report.Model)
		fmt.Fprintf(stdout, "  task:           %s\n", report.TaskID)
		for _, check := range report.Checks {
			fmt.Fprintf(stdout, "  %-26s %-7s %s\n", check.Name, check.Status, check.Detail)
		}
		if report.DryRun != nil {
			fmt.Fprintf(stdout, "  request body:   %d bytes sha256=%s (not displayed)\n", report.DryRun.BodyBytes, report.DryRun.BodySHA256)
		}
	}
	if report.Overall == "FAIL" {
		return 1
	}
	return 0
}

func validateOfflineProviderConfig(config app.ProviderConfig) error {
	switch config.Name {
	case "mock":
	case "meta":
		if strings.TrimSpace(config.Model) == "" || strings.ContainsAny(config.Model, " \t\r\n") {
			return errors.New("Meta model identifier must be non-empty and contain no whitespace")
		}
	case "openrouter":
		if strings.TrimSpace(config.Model) == "" || strings.ContainsAny(config.Model, " \t\r\n") || !strings.Contains(config.Model, "/") {
			return errors.New("OpenRouter model ID must be non-empty and use the provider/model form")
		}
	case "openai-compatible":
		parsed, err := url.Parse(config.BaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" {
			return errors.New("OpenAI-compatible base URL must be an http(s) origin without credentials or query parameters")
		}
		if strings.TrimSpace(config.Model) == "" {
			return errors.New("OpenAI-compatible model ID is required")
		}
	default:
		return fmt.Errorf("unsupported provider %q", config.Name)
	}
	return nil
}

func validateDoctorBudget(budget harness.Budget) error {
	if budget.MaxSteps <= 0 || budget.MaxSteps > 1000 || budget.MaxToolCalls <= 0 || budget.MaxToolCalls > 10000 ||
		budget.MaxTokens <= 0 || budget.MaxTokens > 10_000_000 || budget.MaxCostUSD <= 0 || budget.TimeoutMS <= 0 ||
		budget.TimeoutMS > int64((24*time.Hour)/time.Millisecond) || budget.ToolTimeoutMS <= 0 || budget.MaxRetries < 0 || budget.MaxRetries > 10 {
		return errors.New("limits must use positive step/tool/token/cost/time values and no more than 10 retries")
	}
	return nil
}

func checkWritableDirectory(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("artifact directory is empty")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create artifact directory: %w", err)
	}
	probe, err := os.CreateTemp(path, ".doctor-*.tmp")
	if err != nil {
		return fmt.Errorf("artifact directory is not writable: %w", err)
	}
	name := probe.Name()
	if err := probe.Chmod(0o600); err != nil {
		_ = probe.Close()
		_ = os.Remove(name)
		return fmt.Errorf("secure artifact probe: %w", err)
	}
	if err := probe.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

func checkPythonExperimentRuntime(ctx context.Context) (bool, string) {
	python, err := exec.LookPath("python3")
	if err != nil {
		return false, "Python 3 is required for the fixed primality experiment runtime"
	}
	output, err := exec.CommandContext(ctx, python, "-I", "-c", "import sys, resource; print('%d.%d' % sys.version_info[:2])").CombinedOutput()
	if err != nil {
		return false, "Python 3 runtime or resource module unavailable"
	}
	version := strings.TrimSpace(string(output))
	parts := strings.Split(version, ".")
	if len(parts) != 2 {
		return false, "could not parse Python runtime version"
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil || major < 3 || (major == 3 && minor < 8) {
		return false, "Python 3.8+ is required for the fixed science experiment runtime"
	}
	return true, "Python " + version + " and resource limits are available"
}

func documentedMetaModel(model string) bool {
	for _, candidate := range provider.MetaModels() {
		if candidate.ID == model && candidate.Capabilities.ToolCalls {
			return true
		}
	}
	return false
}

func validateScienceToolSchemas(task harness.Task) error {
	adapter := science.New()
	tools := adapter.ToolsForTask(task)
	for _, tool := range tools {
		var schema map[string]any
		if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
			return fmt.Errorf("tool %s schema: %w", tool.Name, err)
		}
		if schema["type"] != "object" {
			return fmt.Errorf("tool %s schema must be an object", tool.Name)
		}
		if err := harness.ValidateToolSchema(tool.Parameters); err != nil {
			return fmt.Errorf("tool %s schema: %w", tool.Name, err)
		}
	}
	return nil
}

func serializeDryRunRequest(config app.ProviderConfig, task harness.Task, budget harness.Budget, reasoning string) ([]byte, []string, error) {
	domain := science.New()
	domainContext, err := domain.Init(context.Background(), task)
	if err != nil {
		return nil, nil, err
	}
	tools := domain.ToolsForTask(task)
	definitions := make([]harness.ToolDef, 0, len(tools)+2)
	toolNames := make([]string, 0, len(tools)+2)
	for _, tool := range tools {
		definitions = append(definitions, harness.ToolDef{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters})
		toolNames = append(toolNames, tool.Name)
	}
	definitions = append(definitions,
		harness.ToolDef{Name: "submit_claim", Description: "Record a conclusion with explicit evidence references.", Parameters: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`)},
		harness.ToolDef{Name: "finish", Description: "Finish the bounded task.", Parameters: json.RawMessage(`{"type":"object","properties":{"conclusion":{"type":"string"}},"required":["conclusion"]}`)},
	)
	toolNames = append(toolNames, "submit_claim", "finish")
	request := &harness.ModelRequest{
		Model: config.Model, System: domainContext.Instructions,
		Messages: []harness.Message{{Role: harness.RoleUser, Content: task.Prompt}},
		Tools:    definitions, MaxTokens: min(budget.MaxTokens, 8192), ReasoningEffort: reasoning,
	}
	var body []byte
	switch config.Name {
	case "mock":
		body, err = json.Marshal(request)
	case "meta":
		client := provider.NewOpenAICompatible(provider.OpenAIConfig{Name: "meta", BaseURL: provider.MetaBaseURL, Model: config.Model})
		body, err = client.DryRun(request)
	case "openrouter":
		client := provider.NewOpenAICompatible(provider.OpenAIConfig{Name: "openrouter", BaseURL: provider.OpenRouterBaseURL, Model: config.Model, ReasoningParam: "reasoning"})
		body, err = client.DryRun(request)
	case "openai-compatible":
		client := provider.NewOpenAICompatible(provider.OpenAIConfig{Name: config.Name, BaseURL: config.BaseURL, Model: config.Model})
		body, err = client.DryRun(request)
	default:
		return nil, nil, fmt.Errorf("unsupported provider %q", config.Name)
	}
	if err != nil {
		return nil, nil, err
	}
	if !json.Valid(body) {
		return nil, nil, errors.New("provider serializer produced invalid JSON")
	}
	return body, toolNames, nil
}

func sanitizeDoctorError(value, secret string) string {
	if strings.TrimSpace(secret) != "" {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	if len(value) > 300 {
		value = value[:300] + "..."
	}
	return value
}
