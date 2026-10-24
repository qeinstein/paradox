# Evaluation Methodology

PARADOX evaluates the harness, not a model's hidden reasoning. Comparisons reuse the same task, provider/model, tool set, reasoning parameters, retry policy, resource limits, and evaluator across modes. PARADOX may use more tools and tokens for verification; reports expose that overhead.

## Modes

| Mode | Evidence dependencies | Active falsification | Retraction and recovery |
|---|---|---|---|
| `baseline` | passively recorded for scoring; no consistency or invalidation logic is run | off | off |
| `evidence` | on | off | dependency/consistency invalidation only |
| `paradox` | on | on | executable verification, retraction, and replanning |

The same underlying provider, task, tool definitions, and external budgets are used in every mode. Mode-specific system instructions describe which harness mechanism is active; the selected model is unchanged.

## Internal development tasks

The `science` suite currently includes:

- `science-primality-v1`: internal regression demonstration. A partial scan supports an overbroad claim; a full scan finds `f(40)=1681=41×41`, after which the assumption and dependent conclusion are retracted and the agent replans.
- `science-permutation-test-v1`: exact two-sided permutation test over fixed samples. The evaluator checks the measured permutation count and p-value against an independent reference calculation, and rejects a false but well-formed claim.
- `science-optimization-v1`: exhaustive integer optimization over a bounded domain, scored against an analytic reference minimizer.
- `science-measurement-revision-v1`: pilot and independent replication measurements; a failed stability assumption invalidates its conclusion and triggers a follow-up.

These deterministic fixtures test runtime behavior. They are **development tasks**, not held-out tasks or a scientific benchmark. The evaluator's references are stored in task metadata and are not serialized into the model request. The mock provider is scripted to exercise action sequences and cannot support claims about real-model performance.

The `domain/software` package has a narrow regression-test integration demonstrating the same evidence update and recovery path outside the science adapter. It is not a coding benchmark.

## Running an evaluation

```bash
go run ./cmd/paradox eval --config eval/configs/internal-science-v0.2.json
```

The checked-in JSON config defines the development tasks, mock provider/model, modes, deterministic trial count, retry settings, verification policy, and budgets. It is credential-free. Pass `--config` to `paradox doctor` as `--eval-config` to validate a config offline. When supplied, config run settings take precedence over matching CLI flags. Without a config, command-line evaluation settings are reported with config ID `ad-hoc`.

The official ScienceWorld adapter can be invoked with a selected official task and variation:

```bash
go run ./cmd/paradox doctor --provider meta --model muse-spark-1.3 --dry-run
go run ./cmd/paradox eval --suite scienceworld --benchmark-task boil \
  --variation 0 --simplifications "" --step-limit 100 \
  --provider meta --model muse-spark-1.3 \
  --modes baseline,evidence,paradox --trials 1
```

The ScienceWorld README currently requires Java 1.8+ and Python 3.8+ and documents installation with `pip install scienceworld`. The adapter loads/resets the real environment and uses official action execution and score/reward fields. `variation` is explicit and appears in the manifest/report; the optional `seed` metadata is recorded if a task definition supplies one. The official environment score is normalized to `[0,1]` for the shared report, with the raw score and reward kept in the episode evidence/artifact. An absent dependency is a blocker, not a simulated benchmark pass. See the [official ScienceWorld repository and API](https://github.com/allenai/ScienceWorld).

DiscoveryWorld has been reviewed as a possible later discovery benchmark, but no adapter or performance claim is implemented. See its [official repository](https://github.com/allenai/discoveryworld).

## Matching and reports

The JSON report records evaluator version, Git commit, task IDs and public benchmark metadata, model/provider, reasoning parameters, selected tools, mode, trials, verification policy, budget, retry policy, inference/tool usage, tokens, cost, wall time, and per-run evaluation details. Each run persists a manifest with the same task/model/budget context.

Reports include:

- success and domain score, both derived from the domain evaluator rather than model self-report;
- unsupported active claims, contradictions, verification records, and incorrect retractions where the evaluator can assess them;
- per-mode and per-run inference calls, tool calls, tokens, monetary cost when reported/configured, and elapsed time;
- reproducibility signatures for repeated runs;
- paired baseline-to-evidence and baseline-to-PARADOX score, success, token, inference-call, and tool-call deltas, matched by task ID and trial number;
- paired score-delta standard errors and two-sided 95% Student t intervals only when at least two complete pairs exist.

Execution-error pairs are omitted from paired score deltas and remain visible in run details. A one-pair delta is descriptive and has no uncertainty interval. Intervals from small internal samples are not evidence of generalization. Reports never substitute mock measurements for live model results or estimate missing costs.

The local mock provider uses deterministic protocol fixtures with synthetic token counters. Mock token totals and wall-clock execution time are diagnostics for offline harness execution, not real inference usage or latency; mock inference cost is reported as unavailable. Do not interpret mock reports as provider cost or benchmark-quality measurements.

Provider request fixtures were checked against the [Meta Responses API](https://dev.meta.ai/docs/protocols/responses) and [OpenRouter Chat Completions API](https://openrouter.ai/docs/api/api-reference/chat/send-chat-completion-request). Meta documents server-managed multi-turn continuation with `previous_response_id`; OpenRouter documents completion token limits and model-dependent features. Offline protocol tests do not prove live endpoint compatibility.

Output files:

```text
report.json    reproducible run records and aggregate metrics
report.md      human-readable methods, summaries, paired comparisons, and limitations
```

## Live model comparisons

No live inference is run automatically. For later experiments, select one model version and use it unchanged across the modes. Keep prompts, task variations, tool access, evaluation version, and externally defined budgets identical. Record the additional PARADOX verification cost rather than matching realized token usage by hiding calls. OpenRouter tool support is model-specific; verify `tools` in that model's `supported_parameters` before enabling it with `--tool-calls-supported`.

Repeated trials should use task-level pairing. For external benchmarks, report official scores under the same environment, task variations, seeds, step limits, and scoring convention. OpenRouter may select among routed providers for a model ID; the current adapter records the requested model ID but does not pin or report the selected upstream endpoint. Treat such runs as model-ID matched, not necessarily upstream-endpoint matched. Do not compare external published results unless model versions, prompts, tools, budgets, and evaluation conditions are sufficiently matched.

## Limitations

- Internal tasks do not constitute held-out evaluation.
- ScienceWorld is operational only after the official environment smoke test actually runs; the offline adapter test skips when its package is absent.
- DiscoveryWorld and other scientific benchmarks are not integrated.
- Verification metrics depend on domain-specific checks and reference implementations.
- Reproducibility describes evidence records, not scientific truth.
- Cost accounting depends on provider usage fields or explicitly configured prices.
- No benchmark improvement has been established; the research hypothesis remains untested with real-model trials.
