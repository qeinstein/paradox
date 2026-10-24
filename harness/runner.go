package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/qeinstein/paradox/evidence"
)

type RunConfig struct {
	RunID           string
	Task            Task
	Mode            Mode
	Provider        ModelProvider
	Domain          DomainAdapter
	Budget          Budget
	WorkDir         string
	ArtifactsDir    string
	Recorder        Recorder
	Verification    VerificationPolicy
	ReasoningEffort string
	Temperature     *float64
}

type Runner struct {
	cfg             RunConfig
	kernel          *evidence.Kernel
	domainCtx       *DomainContext
	tools           map[string]Tool
	messages        []Message
	usage           Usage
	eventSeq        int64
	step            int
	invalidations   []string
	finalText       string
	finishRequested bool
	startedAt       time.Time
}

func Run(ctx context.Context, cfg RunConfig) (*RunResult, error) {
	if cfg.Provider == nil {
		return nil, errors.New("model provider is required")
	}
	if cfg.Domain == nil {
		return nil, errors.New("domain adapter is required")
	}
	if err := ValidateTask(cfg.Task); err != nil {
		return nil, err
	}
	if cfg.Mode == "" {
		cfg.Mode = Paradox
	}
	if cfg.Recorder == nil {
		cfg.Recorder = NopRecorder{}
	}
	if cfg.Verification == (VerificationPolicy{}) {
		cfg.Verification = DefaultVerificationPolicy()
	}
	cfg.Budget = cfg.Budget.normalized()
	if cfg.RunID == "" {
		cfg.RunID = NewRunID()
	}
	if cfg.WorkDir == "" {
		dir, err := os.MkdirTemp("", "paradox-run-")
		if err != nil {
			return nil, err
		}
		cfg.WorkDir = dir
	}
	if cfg.ArtifactsDir == "" {
		cfg.ArtifactsDir = cfg.WorkDir + "/artifacts"
	}
	if err := os.MkdirAll(cfg.ArtifactsDir, 0o755); err != nil {
		return nil, err
	}

	// The run budget is authoritative even when the caller supplies a longer
	// context deadline. If the caller's deadline is sooner, WithTimeout keeps it.
	ctx, cancel := context.WithTimeout(ctx, cfg.Budget.Timeout())
	defer cancel()

	domainCtx, err := cfg.Domain.Init(ctx, cfg.Task)
	if err != nil {
		return nil, fmt.Errorf("domain initialization: %w", err)
	}
	r := &Runner{
		cfg:       cfg,
		kernel:    evidence.New(cfg.RunID),
		domainCtx: domainCtx,
		tools:     map[string]Tool{},
		startedAt: time.Now().UTC(),
	}
	for _, tool := range cfg.Domain.Tools() {
		r.tools[tool.Name] = tool
	}
	return r.run(ctx), nil
}

func (r *Runner) run(ctx context.Context) *RunResult {
	r.emit("run_started", map[string]any{
		"task_id": r.cfg.Task.ID, "mode": string(r.cfg.Mode),
		"provider": r.cfg.Provider.Name(), "model": r.cfg.Provider.Model(),
		"budget": r.cfg.Budget,
	})
	r.messages = append(r.messages, Message{
		Role:    RoleUser,
		Content: fmt.Sprintf("Research task:\n%s\n\nBegin by using a tool. Do not answer from memory.", r.cfg.Task.Prompt),
	})

	var runErr error
	for r.step < r.cfg.Budget.MaxSteps && !r.finishRequested {
		if err := ctx.Err(); err != nil {
			runErr = fmt.Errorf("run cancelled or timed out: %w", err)
			break
		}
		if r.usage.TotalTokens >= r.cfg.Budget.MaxTokens {
			runErr = fmt.Errorf("token budget exhausted (%d/%d)", r.usage.TotalTokens, r.cfg.Budget.MaxTokens)
			r.emit("budget_exhausted", map[string]any{"kind": "tokens", "used": r.usage.TotalTokens, "limit": r.cfg.Budget.MaxTokens})
			break
		}
		if r.usage.CostAvailable && r.usage.CostUSD >= r.cfg.Budget.MaxCostUSD {
			runErr = fmt.Errorf("cost budget exhausted ($%.4f/$%.4f)", r.usage.CostUSD, r.cfg.Budget.MaxCostUSD)
			r.emit("budget_exhausted", map[string]any{"kind": "cost", "used": r.usage.CostUSD, "limit": r.cfg.Budget.MaxCostUSD})
			break
		}

		r.step++
		r.usage.Steps++
		response, err := r.complete(ctx)
		if err != nil {
			runErr = err
			break
		}
		if r.cfg.Budget.RequireCost && !r.usage.CostAvailable {
			runErr = errors.New("provider did not report inference cost and RequireCost is set")
			break
		}
		if err := r.checkInferenceBudget(); err != nil {
			runErr = err
			break
		}
		if len(response.ToolCalls) == 0 {
			r.finalText = strings.TrimSpace(response.Content)
			if r.finalText == "" {
				runErr = errors.New("model returned neither tool calls nor final text")
			} else {
				r.finishRequested = true
			}
			r.messages = append(r.messages, Message{Role: RoleAssistant, Content: response.Content})
			r.emit("assistant_message", map[string]any{"content": truncate(response.Content, 12000)})
			break
		}

		assistant := Message{Role: RoleAssistant, Content: response.Content, ToolCalls: response.ToolCalls}
		r.messages = append(r.messages, assistant)
		r.emit("assistant_message", map[string]any{
			"content":    truncate(response.Content, 12000),
			"tool_calls": toolCallSummary(response.ToolCalls),
		})

		for _, call := range response.ToolCalls {
			if r.finishRequested {
				r.messages = append(r.messages, Message{
					Role: RoleTool, ToolCallID: call.ID, Name: call.Name,
					Content: `{"ok":false,"error":"not executed because the run finished"}`,
				})
				continue
			}
			if r.usage.ToolCalls >= r.cfg.Budget.MaxToolCalls {
				runErr = fmt.Errorf("tool-call budget exhausted (%d)", r.cfg.Budget.MaxToolCalls)
				r.emit("budget_exhausted", map[string]any{"kind": "tool_calls", "used": r.usage.ToolCalls, "limit": r.cfg.Budget.MaxToolCalls})
				break
			}
			r.usage.ToolCalls++
			result, err := r.executeToolCall(ctx, call)
			if err != nil {
				result = &ToolResult{IsError: true, Content: err.Error(), Data: map[string]any{"error": err.Error()}}
				r.emit("tool_error", map[string]any{"tool": call.Name, "error": err.Error()})
			}
			content := encodeToolResult(call.Name, result)
			r.messages = append(r.messages, Message{
				Role: RoleTool, ToolCallID: call.ID, Name: call.Name, Content: content,
			})
			r.emit("tool_result", map[string]any{
				"tool": call.Name, "ok": !result.IsError,
				"content":      truncate(content, 12000),
				"evidence_ids": result.EvidenceIDs,
			})
			if len(r.invalidations) > 0 {
				for _, message := range r.invalidations {
					r.messages = append(r.messages, Message{
						Role:    RoleUser,
						Content: "[HARNESS] " + message + "\nRevise the affected conclusion using the new evidence. Do not repeat an invalidated claim.",
					})
					r.emit("replan_required", map[string]any{"reason": message})
				}
				r.invalidations = nil
			}
		}
		if runErr != nil {
			break
		}
	}

	if !r.finishRequested && runErr == nil {
		runErr = fmt.Errorf("step budget exhausted after %d steps", r.cfg.Budget.MaxSteps)
		r.emit("budget_exhausted", map[string]any{"kind": "steps", "used": r.step, "limit": r.cfg.Budget.MaxSteps})
	}
	if runErr != nil {
		r.emit("error", map[string]any{"error": runErr.Error()})
	}

	snapshot := r.kernel.Snapshot()
	evaluation, evalErr := r.cfg.Domain.Evaluate(ctx, EvaluationInput{
		Task: r.cfg.Task, Snapshot: snapshot, FinalText: r.finalText,
		Completed: r.finishRequested, Usage: r.usage,
	})
	if evalErr != nil {
		evaluation = &Evaluation{Success: false, Score: 0, Details: map[string]any{"evaluation_error": evalErr.Error()}}
		if runErr == nil {
			runErr = evalErr
		}
	}
	ended := time.Now().UTC()
	result := &RunResult{
		RunID: r.cfg.RunID, Mode: r.cfg.Mode, Provider: r.cfg.Provider.Name(),
		Model: r.cfg.Provider.Model(), TaskID: r.cfg.Task.ID,
		Success: runErr == nil && evaluation != nil && evaluation.Success,
		Score: func() float64 {
			if evaluation == nil {
				return 0
			}
			return evaluation.Score
		}(),
		FinalText: r.finalText, Usage: r.usage, StartedAt: r.startedAt,
		EndedAt: ended, DurationMS: ended.Sub(r.startedAt).Milliseconds(),
		Steps: r.step, Evaluation: evaluation, Snapshot: snapshot,
	}
	if runErr != nil {
		result.Error = runErr.Error()
	}
	if err := r.cfg.Recorder.SaveEvidence(snapshot); err != nil {
		r.emit("persistence_error", map[string]any{"error": err.Error()})
	}
	r.emit("run_finished", map[string]any{
		"success": result.Success, "score": result.Score,
		"error": result.Error, "usage": result.Usage, "stats": snapshot.Stats,
	})
	if err := r.cfg.Recorder.SaveResult(result); err != nil {
		// The event stream already contains the result summary; persistence is
		// best-effort at this point because the run is complete.
		_ = err
	}
	return result
}

// checkInferenceBudget runs after each provider response. Providers can report
// prompt usage that could not be known exactly before the request, so reject an
// over-budget response before accepting its answer or executing its tools.
func (r *Runner) checkInferenceBudget() error {
	if r.usage.TotalTokens > r.cfg.Budget.MaxTokens {
		r.emit("budget_exhausted", map[string]any{
			"kind": "tokens", "used": r.usage.TotalTokens, "limit": r.cfg.Budget.MaxTokens,
		})
		return fmt.Errorf("token budget exceeded (%d/%d)", r.usage.TotalTokens, r.cfg.Budget.MaxTokens)
	}
	if r.usage.CostAvailable && r.usage.CostUSD > r.cfg.Budget.MaxCostUSD {
		r.emit("budget_exhausted", map[string]any{
			"kind": "cost", "used": r.usage.CostUSD, "limit": r.cfg.Budget.MaxCostUSD,
		})
		return fmt.Errorf("cost budget exceeded ($%.4f/$%.4f)", r.usage.CostUSD, r.cfg.Budget.MaxCostUSD)
	}
	return nil
}

func (r *Runner) complete(ctx context.Context) (*ModelResponse, error) {
	remaining := r.cfg.Budget.MaxTokens - r.usage.TotalTokens
	maxTokens := 8192
	if remaining > 0 && remaining < maxTokens {
		maxTokens = remaining
	}
	request := &ModelRequest{
		Model: r.cfg.Provider.Model(), System: r.systemPrompt(),
		Messages: append([]Message(nil), r.messages...),
		Tools:    r.toolDefs(), MaxTokens: maxTokens,
		ReasoningEffort: r.cfg.ReasoningEffort,
		Temperature:     r.cfg.Temperature,
	}
	r.emit("provider_request", map[string]any{
		"model": request.Model, "messages": len(request.Messages),
		"tools": len(request.Tools), "max_tokens": maxTokens,
	})
	var lastErr error
	for attempt := 0; attempt <= r.cfg.Budget.MaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		response, err := r.cfg.Provider.Complete(ctx, request)
		if err == nil {
			r.usage.InferenceCalls++
			r.usage.Add(response.Usage)
			if !response.Usage.CostAvailable {
				r.emit("cost_unavailable", map[string]any{
					"provider": r.cfg.Provider.Name(),
					"message":  "provider did not report a cost; configure per-model prices for cost accounting",
				})
			}
			r.emit("provider_response", map[string]any{
				"content":    truncate(response.Content, 12000),
				"tool_calls": toolCallSummary(response.ToolCalls),
				"usage":      response.Usage, "finish_reason": response.FinishReason,
			})
			return response, nil
		}
		lastErr = err
		r.emit("provider_error", map[string]any{"attempt": attempt + 1, "error": err.Error()})
		if attempt < r.cfg.Budget.MaxRetries {
			delay := time.Duration(150*(attempt+1)) * time.Millisecond
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return nil, fmt.Errorf("model call failed after %d attempts: %w", r.cfg.Budget.MaxRetries+1, lastErr)
}

func (r *Runner) executeToolCall(ctx context.Context, call ToolCall) (*ToolResult, error) {
	switch call.Name {
	case "submit_claim":
		return r.submitClaim(ctx, call.Arguments)
	case "finish":
		return r.finish(call.Arguments)
	}
	tool, ok := r.tools[call.Name]
	if !ok {
		return nil, fmt.Errorf("unknown tool %q", call.Name)
	}
	return r.runDomainTool(ctx, call.Name, tool, json.RawMessage(call.Arguments), "agent", r.cfg.Budget.ToolTimeout())
}

func (r *Runner) runDomainTool(ctx context.Context, name string, tool Tool, args json.RawMessage, origin string, timeout time.Duration) (*ToolResult, error) {
	r.emit("tool_call", map[string]any{"tool": name, "origin": origin, "arguments": truncate(string(args), 4000)})
	if timeout <= 0 {
		timeout = r.cfg.Budget.ToolTimeout()
	}
	toolCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := tool.Handler(toolCtx, &ToolContext{
		RunID: r.cfg.RunID, WorkDir: r.cfg.WorkDir,
		ArtifactsDir: r.cfg.ArtifactsDir, Kernel: r.kernel,
	}, args)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("tool returned no result")
	}
	added := make([]evidence.Node, 0, len(result.Evidence))
	for _, observation := range result.Evidence {
		node, err := r.kernel.AddObservation(observation)
		if err != nil {
			return nil, fmt.Errorf("record evidence: %w", err)
		}
		result.EvidenceIDs = append(result.EvidenceIDs, node.ID)
		added = append(added, node)
	}
	if len(added) > 0 {
		r.saveEvidence()
		for _, node := range added {
			r.emit("observation", map[string]any{"node": node, "tool": name})
		}
	}
	if len(result.EvidenceIDs) > 0 && r.cfg.Mode != Baseline {
		r.checkConsistency()
	}
	return result, nil
}

// saveEvidence persists an intermediate snapshot so live clients can inspect
// the symbolic state while a run is still executing. Final persistence remains
// in run(), and a failed intermediate write is reported without aborting.
func (r *Runner) saveEvidence() {
	if err := r.cfg.Recorder.SaveEvidence(r.kernel.Snapshot()); err != nil {
		r.emit("persistence_error", map[string]any{"error": "save evidence: " + err.Error()})
	}
}

func (r *Runner) submitClaim(ctx context.Context, raw string) (*ToolResult, error) {
	var input evidence.ClaimInput
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return nil, fmt.Errorf("invalid submit_claim arguments: %w", err)
	}
	claim, err := r.kernel.SubmitClaim(input)
	if err != nil {
		return nil, err
	}
	r.saveEvidence()
	r.emit("claim_submitted", map[string]any{
		"claim": claim.Claim, "assumptions": claim.Assumptions, "guard_id": claim.GuardID,
	})
	for _, assumption := range claim.Assumptions {
		r.emit("assumption_recorded", map[string]any{"node": assumption})
	}

	invalidated := false
	if r.cfg.Mode == Paradox && r.cfg.Verification.Enabled {
		invalidated = r.verifyClaim(ctx, claim.Claim)
	}
	current, _ := r.kernel.Node(claim.Claim.ID)
	status := current.Status
	message := "claim recorded"
	if invalidated || status < 0.6 {
		message = "claim is not justified after verification"
	}
	data := map[string]any{
		"claim_id": claim.Claim.ID, "status": status,
		"invalidated": invalidated || status < 0.6,
		"message":     message, "assumptions": nodeIDs(claim.Assumptions),
	}
	return &ToolResult{Data: data, Content: message}, nil
}

func (r *Runner) verifyClaim(ctx context.Context, claim evidence.Node) bool {
	plans, err := r.cfg.Domain.VerificationPlans(ctx, claim, r.kernel.Snapshot(), r.cfg.Verification)
	if err != nil {
		r.emit("verification_error", map[string]any{"claim_id": claim.ID, "error": err.Error()})
		return false
	}
	maxPlans := r.cfg.Verification.MaxPlansPerClaim
	if maxPlans <= 0 {
		maxPlans = 1
	}
	timeout := time.Duration(r.cfg.Verification.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = r.cfg.Budget.ToolTimeout()
	}
	invalidated := false
	for index, plan := range plans {
		if index >= maxPlans {
			break
		}
		if plan.ClaimID == "" {
			plan.ClaimID = claim.ID
		}
		if plan.AssumptionID == "" && len(claim.AssumptionIDs) > 0 {
			plan.AssumptionID = claim.AssumptionIDs[0]
		}
		if plan.ID == "" {
			plan.ID = fmt.Sprintf("vplan-%s-%d", claim.ID, index+1)
		}
		tool, ok := r.tools[plan.Tool]
		if !ok {
			r.emit("verification_error", map[string]any{"plan_id": plan.ID, "error": "unknown verification tool " + plan.Tool})
			continue
		}
		if r.usage.ToolCalls >= r.cfg.Budget.MaxToolCalls {
			r.emit("budget_exhausted", map[string]any{"kind": "tool_calls", "used": r.usage.ToolCalls, "limit": r.cfg.Budget.MaxToolCalls})
			break
		}
		r.usage.ToolCalls++
		r.emit("verification_planned", map[string]any{
			"plan": plan, "claim_id": claim.ID, "rationale": plan.Rationale,
		})
		args, err := json.Marshal(plan.Args)
		if err != nil {
			r.emit("verification_error", map[string]any{"plan_id": plan.ID, "error": err.Error()})
			continue
		}
		result, err := r.runDomainTool(ctx, plan.Tool, tool, args, "verification", timeout)
		if err != nil {
			r.emit("verification_error", map[string]any{"plan_id": plan.ID, "error": err.Error()})
			continue
		}
		outcome, err := r.cfg.Domain.EvaluateVerification(ctx, plan, result, r.kernel.Snapshot())
		if err != nil {
			r.emit("verification_error", map[string]any{"plan_id": plan.ID, "error": err.Error()})
			continue
		}
		if outcome == nil {
			outcome = &VerificationOutcome{}
		}
		if outcome.AssumptionID == "" {
			outcome.AssumptionID = plan.AssumptionID
		}
		if outcome.EvidenceID == "" && len(result.EvidenceIDs) > 0 {
			outcome.EvidenceID = result.EvidenceIDs[len(result.EvidenceIDs)-1]
		}
		if outcome.Method == "" {
			outcome.Method = plan.Check
		}
		r.kernel.RecordVerification(plan.ID, plan.ClaimID, outcome.AssumptionID, outcome.EvidenceID, outcome.Method, outcome.Contradicted, outcome.Reason)
		if outcome.Contradicted {
			contradiction, err := r.kernel.MarkContradiction(plan.ClaimID, outcome.AssumptionID, outcome.EvidenceID, outcome.Reason)
			if err != nil {
				r.saveEvidence()
				r.emit("verification_error", map[string]any{"plan_id": plan.ID, "error": err.Error()})
				continue
			}
			invalidated = true
			r.saveEvidence()
			r.emit("contradiction_detected", map[string]any{"contradiction": contradiction.Contradiction})
			r.emit("claim_invalidated", map[string]any{
				"claim_id": plan.ClaimID, "assumption_id": outcome.AssumptionID,
				"evidence_id": outcome.EvidenceID, "reason": outcome.Reason,
				"invalidated": contradiction.Invalidated, "retracted": contradiction.Retracted,
			})
			r.invalidations = append(r.invalidations, fmt.Sprintf(
				"CLAIM %s INVALIDATED: %s. Assumption %s was retracted after evidence %s.",
				plan.ClaimID, outcome.Reason, outcome.AssumptionID, outcome.EvidenceID))
			break
		}
		if outcome.AssumptionID != "" {
			_ = r.kernel.SetVerification(outcome.AssumptionID, evidence.Verified, outcome.Method)
		}
		r.saveEvidence()
		r.emit("verification_passed", map[string]any{
			"plan_id": plan.ID, "claim_id": plan.ClaimID,
			"assumption_id": outcome.AssumptionID, "evidence_id": outcome.EvidenceID,
			"reason": outcome.Reason,
		})
	}
	return invalidated
}

func (r *Runner) checkConsistency() {
	report := r.kernel.CheckConsistency()
	if report.IssueCount == 0 {
		return
	}
	r.emit("consistency_check", map[string]any{"issue_count": report.IssueCount, "issues": report.Issues})
	if r.cfg.Mode != EvidenceMode && r.cfg.Mode != Paradox {
		return
	}
	for _, issue := range report.Issues {
		if issue.Type != "explicit_contradiction" {
			continue
		}
		for _, factID := range issue.FactIDs {
			node, ok := r.kernel.Node(factID)
			if !ok || node.Kind != evidence.KindClaim || !node.Active {
				continue
			}
			if err := r.kernel.Retract(factID, issue.Message, "consistency"); err == nil {
				r.saveEvidence()
				r.emit("claim_invalidated", map[string]any{"claim_id": factID, "reason": issue.Message})
				r.invalidations = append(r.invalidations, "CLAIM "+factID+" INVALIDATED: "+issue.Message)
			}
		}
	}
}

func (r *Runner) finish(raw string) (*ToolResult, error) {
	var args struct {
		Conclusion string `json:"conclusion"`
		Summary    string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("invalid finish arguments: %w", err)
	}
	r.finalText = strings.TrimSpace(firstNonEmptyString(args.Conclusion, args.Summary))
	if r.finalText == "" {
		return nil, errors.New("finish requires a non-empty conclusion")
	}
	r.finishRequested = true
	r.emit("agent_finished", map[string]any{"conclusion": truncate(r.finalText, 12000)})
	return &ToolResult{Data: map[string]any{"accepted": true, "conclusion": r.finalText}}, nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func nodeIDs(nodes []evidence.Node) []string {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	return ids
}

func (r *Runner) emit(eventType string, data map[string]any) {
	r.eventSeq++
	event := Event{
		Seq: r.eventSeq, RunID: r.cfg.RunID, Time: time.Now().UTC(),
		Type: eventType, Step: r.step, Data: data,
	}
	_ = r.cfg.Recorder.Record(event)
}

func (r *Runner) toolDefs() []ToolDef {
	defs := make([]ToolDef, 0, len(r.tools)+2)
	for _, tool := range r.tools {
		parameters := tool.Parameters
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		defs = append(defs, ToolDef{Name: tool.Name, Description: tool.Description, Parameters: parameters})
	}
	defs = append(defs,
		ToolDef{Name: "submit_claim", Description: "Record a conclusion with explicit evidence IDs and explicit assumptions. Use this for every hypothesis or conclusion. An LLM assertion is not ground truth.", Parameters: json.RawMessage(submitClaimSchema)},
		ToolDef{Name: "finish", Description: "Finish the run with a concise conclusion. The harness evaluates the evidence separately.", Parameters: json.RawMessage(finishSchema)},
	)
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

func (r *Runner) systemPrompt() string {
	var b strings.Builder
	b.WriteString("You are PARADOX, a model-agnostic, evidence-grounded scientific agent harness.\n")
	b.WriteString("You operate in a bounded execution loop with real tools. You must not invent observations.\n\n")
	b.WriteString("TASK:\n")
	b.WriteString(r.cfg.Task.Prompt)
	b.WriteString("\n\n")
	if r.domainCtx != nil && r.domainCtx.Instructions != "" {
		b.WriteString("DOMAIN INSTRUCTIONS:\n")
		b.WriteString(r.domainCtx.Instructions)
		b.WriteString("\n\n")
	}
	b.WriteString("RULES:\n")
	b.WriteString("- Execute experiments with tools before drawing conclusions.\n")
	b.WriteString("- Every claim must cite evidence_ids copied exactly from tool results. If a claim depends on an untested premise, list that premise in assumptions.\n")
	b.WriteString("- Distinguish direct observations (tool output) from your interpretation.\n")
	b.WriteString("- A model assertion is not evidence. Do not cite yourself as evidence.\n")
	b.WriteString("- If the harness reports that a claim was invalidated, retract or revise it using the new evidence and run a follow-up experiment if needed.\n")
	b.WriteString("- Call finish only when you have a supported conclusion.\n\n")
	switch r.cfg.Mode {
	case Baseline:
		b.WriteString("MODE baseline: record claims and tools normally. No active verification or automatic belief revision is performed.\n\n")
	case EvidenceMode:
		b.WriteString("MODE evidence: dependencies are tracked. New evidence may invalidate dependent claims. No active falsification tests are generated automatically.\n\n")
	case Paradox:
		b.WriteString("MODE paradox: dependencies are tracked and uncertain, consequential assumptions may be tested automatically. When a supporting assumption fails, dependent claims are invalidated and you must replan.\n\n")
	}
	b.WriteString("AVAILABLE TOOLS:\n")
	for _, def := range r.toolDefs() {
		b.WriteString("- ")
		b.WriteString(def.Name)
		b.WriteString(": ")
		b.WriteString(def.Description)
		b.WriteString("\n  parameters: ")
		b.WriteString(string(def.Parameters))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(r.evidenceBrief())
	return b.String()
}

func (r *Runner) evidenceBrief() string {
	snapshot := r.kernel.Snapshot()
	var b strings.Builder
	b.WriteString("EVIDENCE STATE (authoritative; generated from the symbolic kernel):\n")
	if len(snapshot.Nodes) == 0 {
		b.WriteString("(empty)\n")
	}
	for _, node := range snapshot.Nodes {
		if node.Kind == evidence.KindContradiction && strings.HasPrefix(node.ID, "guard-") {
			continue
		}
		state := "active"
		if !node.Active {
			state = "invalid"
		}
		fmt.Fprintf(&b, "- %s [%s/%s/%s] %s", node.ID, node.Kind, node.Verification, state, truncate(node.Text, 240))
		if node.ClaimKey != "" {
			fmt.Fprintf(&b, " (claim_key=%s value=%s)", node.ClaimKey, node.ClaimValue)
		}
		b.WriteString("\n")
	}
	if len(snapshot.Retractions) > 0 {
		b.WriteString("RETRACTIONS:\n")
		for _, retraction := range snapshot.Retractions {
			fmt.Fprintf(&b, "- %s: %s (%s)\n", retraction.NodeID, retraction.Reason, retraction.Cause)
		}
	}
	if len(snapshot.Contradictions) > 0 {
		b.WriteString("CONTRADICTIONS:\n")
		for _, contradiction := range snapshot.Contradictions {
			fmt.Fprintf(&b, "- %s vs evidence %s: %s\n", contradiction.ClaimID, contradiction.EvidenceID, contradiction.Reason)
		}
	}
	return b.String()
}

func encodeToolResult(name string, result *ToolResult) string {
	payload := map[string]any{"ok": !result.IsError, "tool": name}
	if result.Data != nil {
		payload["result"] = result.Data
	}
	if result.Content != "" {
		if result.IsError {
			payload["error"] = result.Content
		} else {
			payload["content"] = result.Content
		}
	}
	if len(result.EvidenceIDs) > 0 {
		payload["evidence_ids"] = result.EvidenceIDs
	}
	if len(result.Artifacts) > 0 {
		payload["artifacts"] = result.Artifacts
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return `{"ok":false,"error":"could not encode tool result"}`
	}
	return string(data)
}

func toolCallSummary(calls []ToolCall) []map[string]any {
	out := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		out = append(out, map[string]any{
			"id": call.ID, "name": call.Name,
			"arguments": truncate(call.Arguments, 3000),
		})
	}
	return out
}

const submitClaimSchema = `{
  "type": "object",
  "properties": {
    "text": {"type": "string", "description": "The claim or hypothesis in precise language."},
    "claim_key": {"type": "string", "description": "Stable key used to compare claims about the same thing."},
    "claim_value": {"type": "string", "description": "Short value for this claim key."},
    "subject": {"type": "string"},
    "predicate": {"type": "string"},
    "object": {"type": "string"},
    "confidence": {"type": "number", "minimum": 0, "maximum": 1},
    "evidence_ids": {"type": "array", "items": {"type": "string"}, "description": "Observation IDs copied exactly from tool results."},
    "assumptions": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "text": {"type": "string"},
          "kind": {"type": "string"},
          "confidence": {"type": "number"},
          "metadata": {"type": "object"}
        },
        "required": ["text"]
      }
    },
    "metadata": {"type": "object", "description": "Structured scope or provenance for the claim."}
  },
  "required": ["text"]
}`

const finishSchema = `{
  "type": "object",
  "properties": {
    "conclusion": {"type": "string", "description": "Final evidence-grounded conclusion."}
  },
  "required": ["conclusion"]
}`
