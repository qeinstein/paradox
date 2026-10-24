// Package app is the composition root shared by the CLI, HTTP server, and
// evaluation harness. It wires providers, domains, persistence, and the runtime.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qeinstein/paradox/domain/science"
	"github.com/qeinstein/paradox/evidence"
	"github.com/qeinstein/paradox/harness"
	"github.com/qeinstein/paradox/provider"
	"github.com/qeinstein/paradox/runstore"
)

type RunOptions struct {
	Task            harness.Task
	Mode            harness.Mode
	Provider        harness.ModelProvider
	Domain          harness.DomainAdapter
	Budget          harness.Budget
	Verification    harness.VerificationPolicy
	RunsDir         string
	Trial           int
	GitCommit       string
	ReasoningEffort string
	Temperature     *float64
	RunID           string
	// ManifestMetadata is merged into the persisted run manifest. It is used by
	// the evaluation harness to record suite, trial, and comparison metadata.
	ManifestMetadata map[string]any
	// EventSink receives every runtime event as it is persisted. The HTTP server
	// uses it to stream live runs; it must not block the runner.
	EventSink func(harness.Event)
}

type RunOutput struct {
	Result   *harness.RunResult
	RunDir   string
	Store    *runstore.Store
	Manifest harness.RunManifest
}

type eventRecorder struct {
	inner harness.Recorder
	sink  func(harness.Event)
}

func (r *eventRecorder) Record(event harness.Event) error {
	err := r.inner.Record(event)
	if r.sink != nil {
		r.sink(event)
	}
	return err
}

func (r *eventRecorder) SaveResult(result *harness.RunResult) error {
	return r.inner.SaveResult(result)
}

func (r *eventRecorder) SaveEvidence(snapshot evidence.Snapshot) error {
	return r.inner.SaveEvidence(snapshot)
}

func Run(ctx context.Context, options RunOptions) (*RunOutput, error) {
	if options.Provider == nil {
		return nil, errors.New("provider is required")
	}
	if options.Domain == nil {
		options.Domain = science.New()
	}
	if options.Mode == "" {
		options.Mode = harness.Paradox
	}
	if options.Budget == (harness.Budget{}) {
		options.Budget = harness.DefaultBudget()
	}
	if options.Verification == (harness.VerificationPolicy{}) {
		options.Verification = harness.DefaultVerificationPolicy()
	}
	if options.RunID == "" {
		options.RunID = harness.NewRunID()
	}
	if err := runstore.ValidateRunID(options.RunID); err != nil {
		return nil, err
	}
	store, err := runstore.New(options.RunsDir)
	if err != nil {
		return nil, err
	}
	metadata := map[string]any{
		"provider_capabilities": options.Provider.Capabilities(),
		"verification_policy":   options.Verification,
	}
	for key, value := range options.ManifestMetadata {
		metadata[key] = value
	}
	manifest := harness.RunManifest{
		RunID: options.RunID, CreatedAt: time.Now().UTC(), TaskID: options.Task.ID,
		TaskPrompt: options.Task.Prompt, Domain: options.Task.Domain,
		Mode: options.Mode, Provider: options.Provider.Name(),
		Model: options.Provider.Model(), Budget: options.Budget,
		Trial: options.Trial, GitCommit: options.GitCommit,
		Metadata: metadata,
	}
	writer, err := store.Create(manifest)
	if err != nil {
		return nil, err
	}
	defer writer.Close()
	var recorder harness.Recorder = writer
	if options.EventSink != nil {
		recorder = &eventRecorder{inner: writer, sink: options.EventSink}
	}
	runDir := store.Dir(options.RunID)
	workDir := filepath.Join(runDir, "work")
	artifactsDir := filepath.Join(runDir, "artifacts")
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(artifactsDir, 0o700); err != nil {
		return nil, err
	}
	result, err := harness.Run(ctx, harness.RunConfig{
		RunID: options.RunID, Task: options.Task, Mode: options.Mode,
		Provider: options.Provider, Domain: options.Domain,
		Budget: options.Budget, WorkDir: workDir, ArtifactsDir: artifactsDir,
		Recorder: recorder, Verification: options.Verification,
		ReasoningEffort: options.ReasoningEffort, Temperature: options.Temperature,
	})
	if err != nil {
		return nil, err
	}
	return &RunOutput{Result: result, RunDir: runDir, Store: store, Manifest: manifest}, nil
}

func DefaultScienceTask() harness.Task {
	return harness.Task{
		ID: science.TaskPrimality, Domain: science.DomainName,
		Prompt: "Investigate f(n)=n^2+n+41 for every integer n from 0 through 100. " +
			"Determine whether f(n) is prime for all n in that interval. " +
			"Run computational experiments, record a precise claim with explicit evidence and assumptions, " +
			"and if a claim is invalidated, revise it and run a follow-up experiment. " +
			"Report any counterexample you find.",
		Metadata: map[string]any{
			"expression": "n*n+n+41",
			"evaluation": map[string]any{
				"claim_key":               "primality:poly:n^2+n+41",
				"expected_counterexample": 40,
				"full_range_end":          100,
			},
		},
	}
}

type ProviderConfig struct {
	Name                  string
	Model                 string
	APIKey                string
	BaseURL               string
	InputPricePerMillion  float64
	OutputPricePerMillion float64
	// OpenRouter capabilities are model-specific. Tool calls remain disabled
	// unless the caller explicitly confirms the selected model supports them.
	OpenRouterToolCallsSupported        bool
	OpenRouterReasoningSupported        bool
	OpenRouterStructuredOutputSupported bool
}

func BuildProvider(cfg ProviderConfig) (harness.ModelProvider, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Name)) {
	case "mock":
		return science.NewMockProvider(), nil
	case "meta":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return nil, errors.New("Meta Model API requires META_API_KEY or MODEL_API_KEY")
		}
		return provider.NewMetaWithPricing(cfg.APIKey, cfg.Model, cfg.InputPricePerMillion, cfg.OutputPricePerMillion), nil
	case "openrouter":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return nil, errors.New("OpenRouter requires OPENROUTER_API_KEY")
		}
		if strings.TrimSpace(cfg.Model) == "" {
			return nil, errors.New("OpenRouter requires an explicit --model; use 'paradox models --provider openrouter' to list current models")
		}
		capabilities := harness.Capabilities{
			ToolCalls:        cfg.OpenRouterToolCallsSupported,
			Reasoning:        cfg.OpenRouterReasoningSupported,
			StructuredOutput: cfg.OpenRouterStructuredOutputSupported,
		}
		return provider.NewOpenRouterWithCapabilities(cfg.APIKey, cfg.Model, capabilities, cfg.InputPricePerMillion, cfg.OutputPricePerMillion), nil
	case "openai-compatible":
		if strings.TrimSpace(cfg.BaseURL) == "" {
			return nil, errors.New("openai-compatible requires --base-url")
		}
		if strings.TrimSpace(cfg.Model) == "" {
			return nil, errors.New("openai-compatible requires --model")
		}
		return provider.NewOpenAICompatible(provider.OpenAIConfig{
			Name: "openai-compatible", BaseURL: cfg.BaseURL, APIKey: cfg.APIKey,
			Model: cfg.Model, InputPricePerMillion: cfg.InputPricePerMillion,
			OutputPricePerMillion: cfg.OutputPricePerMillion,
			Capabilities:          harness.Capabilities{ToolCalls: true, StructuredOutput: true, Reasoning: true},
		}), nil
	default:
		return nil, fmt.Errorf("unknown provider %q (want mock, meta, openrouter, or openai-compatible)", cfg.Name)
	}
}

func TaskFromFile(path string) (harness.Task, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return harness.Task{}, err
	}
	var task harness.Task
	if err := json.Unmarshal(data, &task); err != nil {
		return harness.Task{}, err
	}
	if task.Domain == "" {
		task.Domain = science.DomainName
	}
	if err := harness.ValidateTask(task); err != nil {
		return harness.Task{}, err
	}
	return task, nil
}

func GitCommit() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		head := filepath.Join(dir, ".git", "HEAD")
		if data, err := os.ReadFile(head); err == nil {
			ref := strings.TrimSpace(string(data))
			if strings.HasPrefix(ref, "ref: ") {
				refPath := filepath.Join(dir, ".git", strings.TrimPrefix(ref, "ref: "))
				if sha, err := os.ReadFile(refPath); err == nil {
					return strings.TrimSpace(string(sha))
				}
			}
			return ref
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
