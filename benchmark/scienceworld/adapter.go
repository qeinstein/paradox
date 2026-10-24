// Package scienceworld adapts the official AllenAI ScienceWorld Python/Java
// environment to the PARADOX tool and evaluation interfaces. It invokes the
// installed package through a fixed JSON bridge; model output is only a
// ScienceWorld action string and is never executed as host code.
package scienceworld

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
	"sync"

	"github.com/qeinstein/paradox/evidence"
	"github.com/qeinstein/paradox/harness"
)

const DomainName = "scienceworld"

const maxEpisodeTraceBytes = 16 << 20

type Adapter struct {
	mu            sync.Mutex
	env           *processEnv
	task          harness.Task
	initial       Step
	stepLimit     int
	steps         int
	finalScore    int
	completed     bool
	traceBytes    int64
	tracePath     string
	traceArtifact evidence.ArtifactRef
	closed        bool
}

func New() *Adapter { return &Adapter{} }

// NewTask constructs a reproducible ScienceWorld task using an official task
// name and variation index. Variation selection is explicit; no random
// variation or scorer-derived task hints are inserted into the agent prompt.
func NewTask(taskName string, variation int, simplifications string, stepLimit int) harness.Task {
	if stepLimit <= 0 {
		stepLimit = 100
	}
	return harness.Task{
		ID:     fmt.Sprintf("scienceworld:%s:variation:%d", taskName, variation),
		Domain: DomainName,
		Prompt: "Complete the ScienceWorld task shown by the environment. Use the observation and valid actions; report an evidence-grounded conclusion when the episode ends.",
		Metadata: map[string]any{
			"task_name": taskName, "variation": variation,
			"simplifications": simplifications, "step_limit": stepLimit,
		},
	}
}

func (a *Adapter) Name() string { return DomainName }

func (a *Adapter) Init(ctx context.Context, task harness.Task) (*harness.DomainContext, error) {
	if task.Domain != "" && task.Domain != DomainName {
		return nil, fmt.Errorf("ScienceWorld adapter cannot run domain %q", task.Domain)
	}
	if a.env != nil {
		_ = a.env.Close()
	}
	name, _ := task.Metadata["task_name"].(string)
	variation, ok := intMetadata(task.Metadata["variation"])
	if !ok {
		variation = 0
	}
	stepLimit, ok := intMetadata(task.Metadata["step_limit"])
	if !ok || stepLimit <= 0 {
		stepLimit = 100
	}
	simplifications, _ := task.Metadata["simplifications"].(string)
	env, initial, err := startEnvironment(ctx, Config{
		TaskName: name, Variation: variation,
		Simplifications: simplifications, StepLimit: stepLimit,
	})
	if err != nil {
		return nil, err
	}
	if initial.TaskName != name || initial.Variation != variation {
		_ = env.Close()
		return nil, fmt.Errorf("ScienceWorld initialized task/variation %q/%d, requested %q/%d", initial.TaskName, initial.Variation, name, variation)
	}
	a.mu.Lock()
	a.env, a.task, a.initial, a.stepLimit = env, task, initial, stepLimit
	a.steps, a.finalScore, a.completed = 0, 0, false
	a.traceBytes, a.tracePath = 0, ""
	a.traceArtifact = evidence.ArtifactRef{}
	a.closed = false
	a.mu.Unlock()
	return &harness.DomainContext{
		Instructions: fmt.Sprintf(
			"Official ScienceWorld task %q, variation %d. Use `scienceworld_step` with an action from the environment's valid action list. The initial observation is:\n%s\nValid actions:\n%s",
			name, variation, truncateObservation(initial.Observation), encodeActions(initial.ValidActions),
		),
		Metadata: map[string]any{"task_name": name, "variation": variation, "step_limit": stepLimit},
	}, nil
}

func (a *Adapter) Tools() []harness.Tool { return nil }

func (a *Adapter) ToolsForTask(task harness.Task) []harness.Tool {
	return []harness.Tool{{
		Name:        "scienceworld_step",
		Description: "Execute one action in the active official ScienceWorld episode and return the next textual observation and available actions. Actions are passed to the official environment parser; no code is executed.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","minLength":1,"maxLength":1000}},"required":["action"]}`),
		Handler: func(ctx context.Context, tc *harness.ToolContext, raw json.RawMessage) (*harness.ToolResult, error) {
			var args struct {
				Action string `json:"action"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, fmt.Errorf("invalid ScienceWorld action: %w", err)
			}
			return a.step(ctx, tc, args.Action)
		},
	}}
}

func (a *Adapter) step(ctx context.Context, tc *harness.ToolContext, action string) (*harness.ToolResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.env == nil || a.closed {
		return nil, errors.New("ScienceWorld episode is not active")
	}
	if a.completed {
		return nil, errors.New("ScienceWorld task is already complete")
	}
	if a.steps >= a.stepLimit {
		return nil, fmt.Errorf("ScienceWorld step budget exhausted (%d)", a.stepLimit)
	}
	step, err := a.env.Step(ctx, action)
	if err != nil {
		return nil, err
	}
	if step.TaskName != a.initial.TaskName {
		return nil, errors.New("ScienceWorld response changed task name during the episode")
	}
	if step.Variation != a.initial.Variation {
		return nil, errors.New("ScienceWorld response changed variation during the episode")
	}
	a.steps++
	a.finalScore = step.Score
	a.completed = step.Completed
	trace := map[string]any{
		"task_id": a.task.ID, "step": a.steps, "action": action,
		"observation": step.Observation, "reward": step.Reward,
		"official_score": step.Score, "completed": step.Completed,
		"moves": step.Moves, "variation": step.Variation,
	}
	artifact := a.appendTrace(tc, trace)
	claimKey := fmt.Sprintf("scienceworld:%s:variation:%d:step:%d", a.initial.TaskName, a.initial.Variation, a.steps)
	observation := evidence.ObservationInput{
		Text:     fmt.Sprintf("ScienceWorld action %d returned an environment observation.", a.steps),
		ClaimKey: claimKey, ClaimValue: "observation_recorded",
		Subject: a.initial.TaskName, Predicate: "environment_step", Object: action,
		Source: evidence.SourceTool,
		Provenance: map[string]any{
			"benchmark": "ScienceWorld", "task_name": a.initial.TaskName,
			"variation": a.initial.Variation, "move": a.steps,
		},
		Metadata: map[string]any{
			"action": action, "observation": step.Observation,
			"reward": step.Reward, "official_score": step.Score,
			"completed": step.Completed, "moves": step.Moves,
			"variation": step.Variation,
		},
	}
	if artifact.Name != "" {
		observation.Artifacts = []evidence.ArtifactRef{artifact}
	}
	data, err := json.Marshal(map[string]any{
		"observation":   step.Observation,
		"valid_actions": step.ValidActions,
		"completed":     step.Completed,
		"moves":         step.Moves,
	})
	if err != nil {
		return nil, err
	}
	return &harness.ToolResult{
		Content: string(data), Data: map[string]any{
			"observation":   step.Observation,
			"valid_actions": step.ValidActions,
			"completed":     step.Completed,
			"moves":         step.Moves,
		},
		Evidence: []evidence.ObservationInput{observation},
		Artifacts: func() []evidence.ArtifactRef {
			if artifact.Name == "" {
				return nil
			}
			return []evidence.ArtifactRef{artifact}
		}(),
	}, nil
}

func (a *Adapter) appendTrace(tc *harness.ToolContext, entry map[string]any) evidence.ArtifactRef {
	if tc == nil || strings.TrimSpace(tc.ArtifactsDir) == "" || a.traceBytes >= maxEpisodeTraceBytes {
		return evidence.ArtifactRef{}
	}
	encoded, err := json.Marshal(entry)
	if err != nil || int64(len(encoded)+1)+a.traceBytes > maxEpisodeTraceBytes {
		return evidence.ArtifactRef{}
	}
	if err := os.MkdirAll(tc.ArtifactsDir, 0o700); err != nil {
		return evidence.ArtifactRef{}
	}
	if a.tracePath == "" {
		a.tracePath = filepath.Join(tc.ArtifactsDir, "scienceworld-episode.jsonl")
	}
	file, err := os.OpenFile(a.tracePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return evidence.ArtifactRef{}
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return evidence.ArtifactRef{}
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		_ = file.Close()
		return evidence.ArtifactRef{}
	}
	if err := file.Close(); err != nil {
		return evidence.ArtifactRef{}
	}
	a.traceBytes += int64(len(encoded) + 1)
	contents, err := os.ReadFile(a.tracePath)
	if err != nil {
		return evidence.ArtifactRef{}
	}
	hash := sha256.Sum256(contents)
	encodedHash := hex.EncodeToString(hash[:])
	return evidence.ArtifactRef{
		Name: filepath.Base(a.tracePath), Path: a.tracePath,
		SHA256: encodedHash, MediaType: "application/x-ndjson",
	}
}

func (a *Adapter) VerificationPlans(context.Context, evidence.Node, evidence.Snapshot, harness.VerificationPolicy) ([]harness.VerificationPlan, error) {
	return nil, nil
}

func (a *Adapter) EvaluateVerification(context.Context, harness.VerificationPlan, *harness.ToolResult, evidence.Snapshot) (*harness.VerificationOutcome, error) {
	return &harness.VerificationOutcome{}, errors.New("ScienceWorld does not define an automatic claim-verification method")
}

func (a *Adapter) Evaluate(_ context.Context, input harness.EvaluationInput) (*harness.Evaluation, error) {
	a.mu.Lock()
	score, completed, env := a.finalScore, a.completed, a.env
	a.closed = true
	a.mu.Unlock()
	if env != nil {
		_ = env.Close()
	}
	if score < -1 || score > 100 {
		return nil, fmt.Errorf("ScienceWorld returned out-of-range official score %d", score)
	}
	if score < 0 {
		score = 0
	}
	if !completed {
		return &harness.Evaluation{Success: false, Score: float64(score) / 100, Details: map[string]any{
			"benchmark": "ScienceWorld", "official_score": score,
			"official_completion": false, "variation": a.initial.Variation,
		}}, nil
	}
	return &harness.Evaluation{Success: score >= 100, Score: float64(score) / 100, Details: map[string]any{
		"benchmark": "ScienceWorld", "official_score": score,
		"official_completion": completed, "variation": a.initial.Variation,
		"steps": a.steps, "task_name": a.initial.TaskName,
	}}, nil
}

func (a *Adapter) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	if a.env == nil {
		return nil
	}
	err := a.env.Close()
	a.env = nil
	return err
}

func intMetadata(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		if math.Trunc(typed) == typed && typed >= math.MinInt32 && typed <= math.MaxInt32 {
			return int(typed), true
		}
	}
	return 0, false
}

func encodeActions(actions []map[string]any) string {
	if len(actions) == 0 {
		return "(the environment returned no listed actions)"
	}
	encoded, err := json.Marshal(actions)
	if err != nil {
		return "(could not encode the environment action list)"
	}
	return string(encoded)
}

func truncateObservation(value string) string {
	const limit = 32 << 10
	if len(value) > limit {
		return value[:limit] + "\n[observation truncated at 32 KiB]"
	}
	return value
}
