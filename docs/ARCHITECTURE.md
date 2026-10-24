# PARADOX Architecture

PARADOX separates a domain-independent runtime from domain-specific tools, verification plans, and evaluation. The symbolic evidence kernel is the authority for belief state; the model is a proposal generator.

## Package map

| Package | Responsibility |
|---|---|
| `core/` | Preserved symbolic truth-maintenance engine: facts, justification sets, negated dependencies, invalidation, dominator pruning, consistency checks, explanations. No external dependencies. |
| `evidence/` | Typed epistemic layer: observations, assumptions, claims, guards, contradictions, retractions, verification records, snapshots, statistics. |
| `harness/` | Provider and tool interfaces, budgets, bounded execution loop, retries, cancellation, event recording, active verification orchestration, replan messages. |
| `domain/science/` | Task-scoped scientific operations, fixed primality regression, statistical test, integer optimization, measurement stability/replication falsification, and deterministic mock provider. |
| `domain/software/` | Small test-driven change/retest fixture proving the evidence contract is not science-specific. |
| `benchmark/scienceworld/` | Process-isolated bridge and adapter for the official Python/Java ScienceWorld environment, official score/reward capture, variation metadata, and episode artifacts. |
| `provider/` | Meta Responses API, OpenRouter/OpenAI-compatible adapters, custom endpoints, model catalogs, and usage/cost parsing. |
| `app/` | Composition root used by the CLI, HTTP server, and evaluator. |
| `eval/` | Matched cross-mode evaluation, metrics, JSON/Markdown reports. |
| `runstore/` | Durable local run artifacts. |
| `server/` | Local API, run manager, event hub, SSE, eval report access. |
| `web/` | Embedded static dashboard. |
| `cli/` | CLI commands and human/machine renderers. |
| `extractor/` | Retained optional fact-extraction package. It is not on the runtime hot path. |

## Run lifecycle

```text
app.Run
  ├─ create run directory and manifest
  ├─ construct domain adapter context
  ├─ harness.Run
  │   ├─ emit run_started
  │   ├─ append task prompt
  │   ├─ loop: provider request → tool calls → observations → claims
  │   │         ↘ budget / timeout / cancellation checks
  │   │         ↘ active verification plans → tool execution
  │   │         ↘ contradiction → retraction → replan message
  │   ├─ domain evaluation from the final snapshot
  │   ├─ persist evidence snapshot and result
  │   └─ emit run_finished
  └─ close writer
```

The loop is bounded by steps, tool calls, tokens, cost, wall-clock time, retries, and per-tool timeouts. A cancellation from the CLI or HTTP API propagates through the provider request, tool execution, and subprocess context.

## Evidence semantics

### Roots

Observations are `core.Fact` roots with empirical assertion kind and verified status. Assumptions are explicit roots with unverified status. The model cannot create an observation through text; only a tool result can.

### Claims

A claim is a derived fact. Its justification set contains:

```text
[evidence IDs..., assumption IDs..., guard ID]
```

The guard is a positive root representing "this claim has not been contradicted." Using a positive guard rather than a negated internal root avoids the kernel's dominator pruning treating the guard as an invalid ancestor at assertion time.

Claims may carry alternative evidence groups. Evidence and assumptions within one group are conjunctive; groups are disjunctive. Losing one supporting group does not retract a claim while another complete justification remains active. The evidence facade validates referenced nodes before sending justifications to `core/`.

When a contradiction occurs:

1. The failed assumption is retracted and marked rejected.
2. The claim's guard is retracted.
3. The claim and any dependent claims lose their justification and become inactive.
4. A contradiction fact is asserted.
5. A retraction record is appended.
6. The harness sends the agent a replan message.

Guards are marked `internal` and excluded from user-facing node counts and graphs.

### Verification

A domain adapter receives a claim, the current snapshot, and a verification policy. It may return plans such as:

```json
{
  "id": "vplan-...",
  "claim_id": "claim-0002",
  "assumption_id": "asm-0003",
  "tool": "run_experiment",
  "args": {"experiment": "primality_scan", "start": 0, "end": 100},
  "check": "range_exhaustive_counterexample"
}
```

The runner executes the tool, records any produced observations as evidence, asks the adapter to interpret the result, and records a verification record. The adapter—not the model—decides whether the result contradicts the claim's premise. Verification is therefore executable and falsifiable, but still domain-defined.

### Statistics

`evidence.Stats` reports node counts, active/invalid claims, unsupported active claims, contradictions, retractions, verification records, unsupported claim rate, and incorrect retractions.

A claim is supported only if it is active, cites at least one observation or claim, all cited evidence is recursively supported, and all cited assumptions are verified. A model assertion without evidence is active but unsupported; this is deliberate so it can be measured rather than hidden.

## Modes

- `baseline`: tools and claims, no dependency-driven consistency or active verification.
- `evidence`: dependency tracking and consistency checks; no generated falsification plans.
- `paradox`: evidence tracking plus active verification, contradiction handling, retraction, and replanning.

Modes use the same provider, task, and tools, with mode-specific system instructions describing the enabled harness mechanisms. The provider/model itself is unchanged.

## Domain adapter contract

```go
type DomainAdapter interface {
    Name() string
    Init(ctx context.Context, task Task) (*DomainContext, error)
    Tools() []Tool
    VerificationPlans(ctx context.Context, claim evidence.Node, snapshot evidence.Snapshot, policy VerificationPolicy) ([]VerificationPlan, error)
    EvaluateVerification(ctx context.Context, plan VerificationPlan, result *ToolResult, snapshot evidence.Snapshot) (*VerificationOutcome, error)
    Evaluate(ctx context.Context, in EvaluationInput) (*Evaluation, error)
}
```

A software-engineering adapter can implement the same contract with tests, type checks, linters, reproductions, and failing-test evidence. The harness imports no domain concepts.

`TaskToolAdapter` optionally scopes tools to a task. This lets science expose predefined experiments and ScienceWorld expose its environment-action tool without keeping task configuration in the harness. The ScienceWorld bridge starts the official package, loads the requested task and variation, resets it, forwards actions to `step`, and returns official observations, rewards, scores, and completion state. Model-visible tool output contains the environment response and valid actions; official scoring is evaluated by the adapter. The integration limits actions, steps, output, process resources, and episode-artifact size. Its smoke test runs only when the official Python package and Java runtime are installed.

## Persistence

Each run directory contains:

```text
manifest.json      immutable run identity, task, model, budget, commit, eval metadata
events.jsonl       append-only runtime events, flushed after each event
result.json        final result, usage, evaluation, evidence snapshot
evidence.json      stable evidence snapshot
artifacts/         experiment outputs referenced by SHA-256
work/              per-run working directory
```

The store is local filesystem JSON/JSONL. It is intentionally inspectable and dependency-free. It is not a distributed database and does not provide multi-writer coordination.

## HTTP server

The server has three responsibilities:

1. Persist and serve run artifacts.
2. Start and cancel runs through the same `app.Run` composition root as the CLI.
3. Fan out live events to SSE subscribers while keeping the JSONL file as the source of truth.

The run manager holds in-memory cancellation functions and terminal status. On startup, historical runs are discovered from the store; a run with a manifest but no result is reported as `interrupted`.

Provider keys are read from environment variables only. The browser receives model IDs, never credentials.

The Meta adapter chains Responses API IDs across tool turns. The opaque provider state is held only by the active runner and resets for each run. PARADOX records evidence and explicit decisions; it does not expose or verify private model reasoning.

## Failure behavior

- Provider errors are retried up to `MaxRetries` with bounded backoff, then fail the run.
- Tool errors are returned to the model as structured error results; unknown tools and invalid arguments do not crash the runner.
- Budget exhaustion is recorded as an event and a result error.
- Evaluation failure is distinct from execution failure: a run can complete and still fail the domain evaluation.
- Persistence errors during event recording are surfaced as events; final result/evidence persistence is best-effort after completion.

## Deliberate limits

- The evidence kernel is monotonic in its handling of explicit observations; contradiction is expressed by retracting a guard or premise, not by mutating observations.
- The science subprocess applies CPU, memory, file-descriptor, wall-clock, and output bounds and accepts only a fixed expression grammar. It is still not a hardened security sandbox.
- ScienceWorld is not marked operational unless the official environment smoke test executes. Its evaluation requires an explicit official task and variation and does not add reference answers to the agent prompt.
- Internal statistical, optimization, and measurement tasks are development fixtures, not held-out benchmark results.
- OpenRouter tool support is selected per model. CLI users must explicitly confirm tool support after checking the provider catalog; the adapter's generic tool schema does not imply model capability.
- The mock provider exists to test the harness and is not a model-quality benchmark.
- The dashboard displays persisted data; it does not infer missing values.
