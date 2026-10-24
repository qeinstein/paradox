// Package harness contains PARADOX's model-agnostic agent runtime: provider
// interfaces, the bounded execution loop, tool dispatch, event tracing, and the
// domain-adapter contract. It knows nothing about any particular science.
package harness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/qeinstein/paradox/evidence"
)

type Mode string

const (
	// Baseline runs the same loop and tools without active evidence revision.
	Baseline Mode = "baseline"
	// Evidence records dependencies and invalidates dependents when evidence
	// changes, but does not actively generate falsification tests.
	EvidenceMode Mode = "evidence"
	// Paradox enables evidence tracking, active verification, contradiction
	// detection, retraction, and recovery.
	Paradox Mode = "paradox"
)

func ParseMode(value string) (Mode, error) {
	switch Mode(value) {
	case Baseline, EvidenceMode, Paradox:
		return Mode(value), nil
	default:
		return "", fmt.Errorf("unknown mode %q (want baseline, evidence, or paradox)", value)
	}
}

type Task struct {
	ID       string         `json:"id"`
	Prompt   string         `json:"prompt"`
	Domain   string         `json:"domain,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type Budget struct {
	MaxSteps      int     `json:"max_steps"`
	MaxToolCalls  int     `json:"max_tool_calls"`
	MaxTokens     int     `json:"max_tokens"`
	MaxCostUSD    float64 `json:"max_cost_usd"`
	TimeoutMS     int64   `json:"timeout_ms"`
	MaxRetries    int     `json:"max_retries"`
	ToolTimeoutMS int64   `json:"tool_timeout_ms"`
	RequireCost   bool    `json:"require_cost"`
}

func DefaultBudget() Budget {
	return Budget{
		MaxSteps: 20, MaxToolCalls: 40, MaxTokens: 200000,
		MaxCostUSD: 5.0, TimeoutMS: 600000, MaxRetries: 2,
		ToolTimeoutMS: 120000,
	}
}

func (b Budget) normalized() Budget {
	defaults := DefaultBudget()
	if b.MaxSteps <= 0 {
		b.MaxSteps = defaults.MaxSteps
	}
	if b.MaxToolCalls <= 0 {
		b.MaxToolCalls = defaults.MaxToolCalls
	}
	if b.MaxTokens <= 0 {
		b.MaxTokens = defaults.MaxTokens
	}
	if b.MaxCostUSD <= 0 {
		b.MaxCostUSD = defaults.MaxCostUSD
	}
	if b.TimeoutMS <= 0 {
		b.TimeoutMS = defaults.TimeoutMS
	}
	if b.MaxRetries < 0 {
		b.MaxRetries = 0
	}
	if b.ToolTimeoutMS <= 0 {
		b.ToolTimeoutMS = defaults.ToolTimeoutMS
	}
	return b
}

func (b Budget) Timeout() time.Duration { return time.Duration(b.TimeoutMS) * time.Millisecond }
func (b Budget) ToolTimeout() time.Duration {
	return time.Duration(b.ToolTimeoutMS) * time.Millisecond
}

type Usage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	ReasoningTokens  int     `json:"reasoning_tokens"`
	CachedTokens     int     `json:"cached_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	CostAvailable    bool    `json:"cost_available"`
	InferenceCalls   int     `json:"inference_calls"`
	ToolCalls        int     `json:"tool_calls"`
	Steps            int     `json:"steps"`
}

func (u *Usage) Add(other Usage) {
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.ReasoningTokens += other.ReasoningTokens
	u.CachedTokens += other.CachedTokens
	if other.TotalTokens > 0 {
		u.TotalTokens += other.TotalTokens
	} else {
		u.TotalTokens += other.PromptTokens + other.CompletionTokens
	}
	if other.CostAvailable {
		u.CostUSD += other.CostUSD
		u.CostAvailable = true
	}
	u.InferenceCalls += other.InferenceCalls
	u.ToolCalls += other.ToolCalls
	u.Steps += other.Steps
}

type ToolContext struct {
	RunID        string
	WorkDir      string
	ArtifactsDir string
	Kernel       *evidence.Kernel
}

type ToolResult struct {
	Content     string                      `json:"content,omitempty"`
	Data        map[string]any              `json:"data,omitempty"`
	IsError     bool                        `json:"is_error,omitempty"`
	Evidence    []evidence.ObservationInput `json:"-"`
	EvidenceIDs []string                    `json:"evidence_ids,omitempty"`
	Artifacts   []evidence.ArtifactRef      `json:"artifacts,omitempty"`
}

type ToolHandler func(ctx context.Context, tc *ToolContext, args json.RawMessage) (*ToolResult, error)

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Handler     ToolHandler     `json:"-"`
}

type VerificationPolicy struct {
	Enabled                 bool    `json:"enabled"`
	MaxPlansPerClaim        int     `json:"max_plans_per_claim"`
	MinAssumptionConfidence float64 `json:"min_assumption_confidence"`
	TimeoutMS               int64   `json:"timeout_ms"`
}

func DefaultVerificationPolicy() VerificationPolicy {
	return VerificationPolicy{
		Enabled: true, MaxPlansPerClaim: 2, MinAssumptionConfidence: 0.95,
		TimeoutMS: 120000,
	}
}

type VerificationPlan struct {
	ID           string         `json:"id"`
	ClaimID      string         `json:"claim_id"`
	AssumptionID string         `json:"assumption_id,omitempty"`
	Rationale    string         `json:"rationale"`
	Tool         string         `json:"tool"`
	Args         map[string]any `json:"args"`
	Check        string         `json:"check,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type VerificationOutcome struct {
	Contradicted bool   `json:"contradicted"`
	AssumptionID string `json:"assumption_id,omitempty"`
	EvidenceID   string `json:"evidence_id,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Method       string `json:"method,omitempty"`
}

type DomainContext struct {
	Instructions string         `json:"instructions,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type EvaluationInput struct {
	Task      Task
	Snapshot  evidence.Snapshot
	FinalText string
	Completed bool
	Usage     Usage
}

type Evaluation struct {
	Success bool           `json:"success"`
	Score   float64        `json:"score"`
	Details map[string]any `json:"details,omitempty"`
}

type DomainAdapter interface {
	Name() string
	Init(ctx context.Context, task Task) (*DomainContext, error)
	Tools() []Tool
	VerificationPlans(ctx context.Context, claim evidence.Node, snapshot evidence.Snapshot, policy VerificationPolicy) ([]VerificationPlan, error)
	EvaluateVerification(ctx context.Context, plan VerificationPlan, result *ToolResult, snapshot evidence.Snapshot) (*VerificationOutcome, error)
	Evaluate(ctx context.Context, in EvaluationInput) (*Evaluation, error)
}

// TaskToolAdapter optionally provides task-scoped tools. Domain adapters use
// this when experiments or actions differ by task, without keeping mutable
// per-run state on a shared adapter instance.
type TaskToolAdapter interface {
	ToolsForTask(task Task) []Tool
}

type Event struct {
	Seq   int64          `json:"seq"`
	RunID string         `json:"run_id"`
	Time  time.Time      `json:"time"`
	Type  string         `json:"type"`
	Step  int            `json:"step,omitempty"`
	Data  map[string]any `json:"data,omitempty"`
}

type Recorder interface {
	Record(Event) error
	SaveResult(*RunResult) error
	SaveEvidence(evidence.Snapshot) error
}

type NopRecorder struct{}

func (NopRecorder) Record(Event) error                   { return nil }
func (NopRecorder) SaveResult(*RunResult) error          { return nil }
func (NopRecorder) SaveEvidence(evidence.Snapshot) error { return nil }

type RunResult struct {
	RunID      string            `json:"run_id"`
	Mode       Mode              `json:"mode"`
	Provider   string            `json:"provider"`
	Model      string            `json:"model"`
	TaskID     string            `json:"task_id"`
	Success    bool              `json:"success"`
	Score      float64           `json:"score"`
	FinalText  string            `json:"final_text,omitempty"`
	Error      string            `json:"error,omitempty"`
	Usage      Usage             `json:"usage"`
	StartedAt  time.Time         `json:"started_at"`
	EndedAt    time.Time         `json:"ended_at"`
	DurationMS int64             `json:"duration_ms"`
	Steps      int               `json:"steps"`
	Evaluation *Evaluation       `json:"evaluation,omitempty"`
	Snapshot   evidence.Snapshot `json:"snapshot"`
}

type RunManifest struct {
	RunID      string         `json:"run_id"`
	CreatedAt  time.Time      `json:"created_at"`
	TaskID     string         `json:"task_id"`
	TaskPrompt string         `json:"task_prompt"`
	Domain     string         `json:"domain"`
	Mode       Mode           `json:"mode"`
	Provider   string         `json:"provider"`
	Model      string         `json:"model"`
	Budget     Budget         `json:"budget"`
	Trial      int            `json:"trial"`
	GitCommit  string         `json:"git_commit,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

func NewRunID() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return "run-" + time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(buf[:])
}

func ValidateTask(task Task) error {
	if task.ID == "" {
		return errors.New("task id is required")
	}
	if task.Prompt == "" {
		return errors.New("task prompt is required")
	}
	if task.Domain == "" {
		return errors.New("task domain is required")
	}
	return nil
}

func truncate(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + fmt.Sprintf("... [%d runes truncated]", len(runes)-limit)
}
