# Evaluation Methodology

PARADOX evaluates the harness, not a model's latent knowledge. The same task, provider, model, budget, retry policy, and tool timeout are used across modes so that differences come from the orchestration mode.

## Running an evaluation

```bash
go run ./cmd/paradox eval --trials 3
```

Useful flags:

```text
--suite science             internal deterministic suite
--modes baseline,evidence,paradox
--trials N                  repeated trials per mode
--provider mock|meta|openrouter|openai-compatible
--model <id>
--runs-dir <dir>            persisted run artifacts
--output <dir>              report directory
--json                      machine-readable report on stdout
--markdown <path|->         additional Markdown report
--allow-errors              exit 0 even if individual runs fail
```

Each run manifest records `eval_id`, `eval_suite`, and `eval_trial`. The report records the Git commit, provider, model, budget, verification policy, task set, run IDs, and all metrics.

## Matched comparison rules

For every task and mode:

- one provider instance is reused for the evaluation;
- the same budget and retry policy are passed to every run;
- the same domain adapter configuration is used;
- only the mode's orchestration behavior differs;
- trials are repeated sequentially to keep the comparison controlled.

This is a within-suite comparison. It does not establish that one mode is universally better for every domain or model.

## Metrics

### Success rate

`successes / runs`. Success comes from the domain adapter's `Evaluate` result, not from the model's final text.

### Mean score

Mean of the domain adapter's score in `[0,1]`. For the science suite, partial credit is awarded for observations, counterexample discovery, retraction of the universal claim, a bounded replacement claim, and a counterexample claim. A complete recovery sets the score to `1.0`.

### Unsupported claim rate

```text
active claims without a live chain to direct observations and verified assumptions
────────────────────────────────────────────────────────────────────────────────────
                              active claims
```

A claim is supported only when:

1. it is active;
2. it cites at least one observation or claim;
3. every cited evidence node is recursively supported;
4. every cited assumption is verified.

A bare model assertion is active but unsupported. This is intentional: the metric exposes assertions that were recorded but not grounded.

### Incorrect retraction rate

```text
verification/contradiction retractions without a matching counterexample observation
───────────────────────────────────────────────────────────────────────────────────────
                 assessable verification/contradiction retractions
```

Manual and consistency retractions are excluded from the denominator. When there are no assessable retractions, the report prints `n/a` rather than zero. In the science suite, a counterexample observation is an observation with `claim_value = "composite"`.

### Verification accuracy

```text
verification records whose referenced evidence agrees with the outcome
──────────────────────────────────────────────────────────────────────
                  assessable verification records
```

- A contradicted verification is correct when its referenced evidence is an observation with a composite value or a positive composite count.
- A non-contradicted verification is correct when its referenced evidence is a primality scan with zero composites.
- Records without usable evidence are counted as unknown and excluded from the denominator.

### Reproducibility

Reproducibility is computed only when a mode has more than one trial. Each run gets a canonical evidence signature covering:

- node kind, text, claim key/value, verification status, and active state (internal guards excluded);
- contradiction reasons;
- retraction node/reason/cause;
- verification plan, method, outcome, and reason.

The metric is the share of trials matching the most common signature in that mode. With one trial it is `n/a`. This measures determinism of the recorded evidence state; it does not measure scientific truth.

### Cost, tokens, and time

- Token counts are summed from provider usage.
- Cost is summed only when every run reports cost, or when configured per-million-token prices are present.
- If any cost is unavailable, the report marks cost as partial or `n/a`; it never estimates a price.
- Duration is wall-clock time recorded by the runner. Mean time is reported per mode.

### Calls

Inference calls and tool calls are aggregated per mode. Verification tool calls are included in tool-call usage and budgets.

## The internal science suite

The suite currently contains one task, `science-primality-v1`:

> Investigate `f(n)=n²+n+41` for every integer `n` from 0 through 100. Determine whether `f(n)` is prime for all `n` in that interval. Run computational experiments, record a precise claim with explicit evidence and assumptions, and if a claim is invalidated, revise it and run a follow-up experiment. Report any counterexample.

The deterministic mock provider deliberately overgeneralizes after a partial scan. It does not decide whether the claim is valid; the harness and the science adapter do.

Expected outcomes with the mock provider:

| Mode | Expected success | Why |
|---|---:|---|
| `baseline` | false | no dependency invalidation or active verification; universal claim remains active |
| `evidence` | false | dependencies are tracked, but no falsification plan is generated |
| `paradox` | true | full-range scan finds `f(40)=1681=41×41`; assumption and claim retracted; bounded and counterexample claims submitted |

These outcomes are verified by tests. They are properties of the harness and mock, not evidence about a real model.

## Running with real models

Set the provider key and pass an explicit model:

```bash
export META_API_KEY=...
go run ./cmd/paradox eval \
  --provider meta --model muse-spark-1.3 \
  --trials 3 --json
```

```bash
export OPENROUTER_API_KEY=...
go run ./cmd/paradox eval \
  --provider openrouter --model <model-id> \
  --trials 3
```

Real models may fail the task, call tools incorrectly, exceed budgets, or produce different claims. Reports should be interpreted with the model, prompt, task, and budget attached. Do not compare reports with different budgets or task sets as if they were matched.

## Report files

Each evaluation directory contains:

```text
report.json    complete machine-readable report, including all runs
report.md      human-readable summary, metric definitions, and run table
```

The JSON report is the source of truth for downstream tooling. The dashboard reads the same file through `GET /api/evals`.

## Limitations

- One hand-built scientific task is not a broad benchmark.
- The mock provider is deterministic by design and should not be presented as a model result.
- Verification accuracy depends on the domain adapter's interpretation of tool evidence.
- Reproducibility signatures compare recorded evidence, not external ground truth.
- Token and cost accounting depend on the provider's usage fields.
- The science subprocess is resource-limited, not a hostile-code sandbox.
