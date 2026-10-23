# PARADOX

**A model-agnostic, evidence-grounded, self-correcting agent harness.**

PARADOX runs bounded agent loops against real tools, records typed evidence and explicit assumptions, actively verifies consequential claims with executable checks, and retracts conclusions when their premises fail. It does not change model weights, and it does not treat a model's assertion as ground truth.

The first vertical slice is a computational science task: an agent tests the polynomial `f(n) = n² + n + 41`, overgeneralizes from a partial range, and recovers after an exhaustive scan finds `f(40) = 1681 = 41 × 41`.

```text
model/provider ──▶ bounded runner ──▶ domain tools
                         │                  │
                         │                  ▼
                         │          direct observations
                         │                  │
                         ▼                  ▼
                  verification ◀── symbolic evidence kernel
                         │          claims + assumptions + guards
                         ▼                  │
                 executable checks          │ contradiction
                         │                  ▼
                         └──────────▶ retraction + replan
```

## What PARADOX does

- **Tracks evidence dependencies.** Observations are roots. Assumptions are explicit roots. Claims are derived facts justified by evidence IDs, assumptions, and a contradiction guard.
- **Makes verification executable.** In `paradox` mode, a domain adapter generates verification plans that run tools. The science adapter scans the full claimed range rather than asking a critic model for an opinion.
- **Revises beliefs non-monotonically.** When a verification produces a counterexample, the supporting assumption is rejected, the claim's guard is retracted, dependent claims become invalid, and the runner sends a replan message back to the agent.
- **Keeps provenance and explanations.** Every run persists a manifest, append-only event stream, result, evidence snapshot, and experiment artifacts. The original symbolic kernel provides justification graphs and explanations.
- **Compares operating modes under matched conditions.** `baseline`, `evidence`, and `paradox` runs use the same task, model, budget, retry policy, and tool timeout.
- **Exposes actual execution, not a scripted demo.** The local dashboard reads the persisted event stream and the symbolic evidence snapshot.

## What PARADOX is not

- It is not a new foundation model or a fine-tuning method.
- It is not an unconditional "critic LLM" wrapper. Verification is a tool plan supplied by the domain adapter and evaluated against tool output.
- It is not a general-purpose code sandbox. The included science tool executes a fixed, validated Python script under resource limits. Do not run untrusted models as an OS-level adversary.
- It is not a claim of real-world model benchmark results. The repository contains deterministic mock-provider tests; real model runs require your credentials and are not included in CI.
- It is not the old Velarix SaaS product. Billing, tenancy, signup/login, enterprise provisioning, legacy dashboards, SDKs, and marketing surfaces have been removed.

## Quickstart

Requirements:

- Go 1.26+
- Python 3.10+ for the science experiment tool
- Optional: `prlimit` (Linux `util-linux`) for subprocess memory/CPU/file-descriptor limits

Run the complete scientific slice with the deterministic mock provider:

```bash
go run ./cmd/paradox run --provider mock --mode paradox
```

Inspect the persisted trace and evidence graph:

```bash
go run ./cmd/paradox trace <run-id>
go run ./cmd/paradox evidence <run-id>
```

Run a matched comparison:

```bash
go run ./cmd/paradox eval --trials 3
```

Start the local dashboard and API:

```bash
go run ./cmd/paradox serve
# open http://127.0.0.1:8080
```

All run artifacts default to `.paradox/runs`; evaluation reports default to `.paradox/evals`.

## The science vertical slice

Task `science-primality-v1` asks whether `f(n) = n² + n + 41` is prime for every integer `n` in `[0,100]`.

1. The agent runs a primality scan over `[0,39]`. All 40 values are prime.
2. It submits a universal claim over `[0,100]` and records an explicit assumption: the tested range is representative of the claimed range.
3. In `paradox` mode, the science adapter turns that assumption into an executable verification plan: scan the full claimed range.
4. The scan finds 14 composites. The first is `f(40) = 1681 = 41 × 41`.
5. The assumption and the universal claim are retracted. The guard on the claim is invalidated, so dependent conclusions are invalid too.
6. The runner sends a replan message. The agent revises to a bounded claim over `[0,39]` and a counterexample claim about `n=40`.

`baseline` and `evidence` modes do not run the falsification plan, so the universal claim remains active and the domain evaluation fails. This is measured in the evaluation report rather than asserted by the UI.

## Evidence model

| Kind | Meaning |
|---|---|
| `observation` | Direct tool or experiment output. Treated as verified empirical evidence. |
| `assumption` | An explicit premise introduced by the model or task. Unverified until a verification plan supports it. |
| `claim` | A model interpretation or conclusion. It must cite observations/claims and may name assumptions. |
| `contradiction` | A system or verification fact that records a failed premise or conflicting conclusion. |
| `decision` | A typed decision node reserved for domain adapters. |
| `artifact` | Referenced experiment output with a path and SHA-256 hash. |

Each claim is derived from:

```text
evidence IDs + assumption IDs + per-claim contradiction guard
```

The guard is a positive root. Contradiction retracts the guard, which invalidates the claim and every dependent conclusion through the symbolic truth-maintenance engine. Guards are marked internal and excluded from user-facing node statistics.

Claims record an epistemic status (`hypothesis`, `supported_hypothesis`) and verification status (`unverified`, `supported`, `verified`, `rejected`). The harness never silently upgrades a model assertion to an observation.

## Operating modes

| Mode | Dependencies | Consistency checks | Active falsification | Automatic retraction/replan |
|---|---:|---:|---:|---:|
| `baseline` | no | no | no | no |
| `evidence` | yes | yes | no | only for explicit contradictions found during consistency checks |
| `paradox` | yes | yes | yes | yes, when executable verification contradicts a premise |

Live verification is generated only in `paradox` mode and only for assumptions that the domain adapter considers testable and consequential.

## Providers

PARADOX speaks an OpenAI-compatible chat-completions surface and keeps provider logic outside the harness and domains.

| Provider | Use | Credentials |
|---|---|---|
| `mock` | Deterministic science tests, evaluation harness, UI demos | none |
| `meta` | Meta Model API, first-class Muse models | `META_API_KEY` (or `MODEL_API_KEY`) |
| `openrouter` | Any current OpenRouter model; explicit model ID required | `OPENROUTER_API_KEY` |
| `openai-compatible` | Self-hosted or other compatible endpoints | `MODEL_API_KEY` / `OPENAI_API_KEY`, `PARADOX_BASE_URL` |

Meta adapter defaults to `muse-spark-1.3` and uses `https://api.meta.ai/v1`. The documented catalog is available with:

```bash
go run ./cmd/paradox models --provider meta
go run ./cmd/paradox models --provider meta --remote   # live enumeration with a key
```

OpenRouter model IDs change over time:

```bash
export OPENROUTER_API_KEY=...
go run ./cmd/paradox models --provider openrouter
go run ./cmd/paradox run --provider openrouter --model <model-id>
```

Cost accounting is only marked available when the provider reports cost or when per-million-token prices are configured. Missing cost is never estimated.

## CLI

```text
paradox run       Execute a bounded agent run
paradox trace     Inspect a persisted execution trace
paradox evidence  Inspect the evidence graph and retractions
paradox eval      Run matched baseline/evidence/paradox comparisons
paradox serve     Start the local API and dashboard
paradox models    List documented or live provider models
paradox version   Print build identity
```

Examples:

```bash
# Real provider run
go run ./cmd/paradox run \
  --provider meta --model muse-spark-1.3 \
  --mode paradox --max-steps 20 --max-tokens 200000

# Machine-readable output
go run ./cmd/paradox run --provider mock --json

# Trace one run
go run ./cmd/paradox trace --json <run-id>

# Evidence statistics and nodes
go run ./cmd/paradox evidence <run-id> --edges

# Focus on one node
go run ./cmd/paradox evidence <run-id> --node claim-0002

# Evaluation with a Markdown report
go run ./cmd/paradox eval --trials 5 --markdown report.md
```

`run` exits non-zero when the domain evaluation does not pass. Use `--allow-failure` when intentionally running a negative-control mode.

## Evaluation

`paradox eval` runs the same task across modes with matched models, budgets, retries, and tool timeouts, then writes `report.json` and `report.md`.

Metrics include:

- success rate and mean domain score
- unsupported claim rate (active claims without a live chain to observations and verified assumptions)
- incorrect retraction rate (verification-driven retractions without a matching counterexample)
- verification accuracy (assessable verification records that agree with their referenced evidence)
- reproducibility (share of repeated trials matching the modal canonical evidence signature)
- prompt/completion/total tokens, cost, wall-clock time, inference calls, and tool calls

See [docs/EVALUATION.md](docs/EVALUATION.md) for precise definitions and interpretation limits.

## HTTP API and dashboard

The default bind is `127.0.0.1:8080`, with no authentication required on loopback. A non-loopback bind is refused unless `PARADOX_API_KEY` (or `--api-key`) is set; API requests then require `X-API-Key` or `Authorization: Bearer`.

| Endpoint | Purpose |
|---|---|
| `GET /api/health` | Health, version, auth requirement, defaults |
| `GET /api/models` | Documented Meta catalog and built-in mock |
| `GET /api/runs` | Persisted run summaries |
| `POST /api/runs` | Start a run asynchronously |
| `GET /api/runs/{id}` | Manifest, status, result, evidence snapshot |
| `POST /api/runs/{id}/cancel` | Cancel an active run |
| `GET /api/runs/{id}/events` | Persisted JSONL event stream |
| `GET /api/runs/{id}/events/stream` | Server-sent events for live runs |
| `GET /api/runs/{id}/evidence` | Evidence snapshot |
| `GET /api/runs/{id}/artifacts/{name}` | Experiment artifact |
| `GET /api/evals` | Evaluation report summaries |
| `GET /api/evals/{id}` | Full evaluation report |

The dashboard reads real status, events, claims, contradictions, retractions, verification records, and artifacts. It does not contain scripted animation data.

## Security and limits

- The HTTP server is local-first. Non-loopback bindings require an API key, and security headers are set on every response.
- Provider keys are read from server environment variables; the browser never receives them.
- The science tool writes a fixed script to a temporary directory, passes validated JSON arguments, uses `python3 -I`, and applies `prlimit` limits when available.
- This is **not** a hardened multi-tenant sandbox. Run untrusted models in an isolated container or VM with network and filesystem controls appropriate to your threat model.
- The symbolic kernel tracks justification and invalidation; it does not prove scientific truth. It records what the tools observed and what the model inferred.
- API keys can be supplied via query parameter only for SSE clients that cannot set headers. Prefer loopback or a reverse proxy that terminates TLS and authentication.

## Repository layout

```text
cmd/paradox/       CLI entry point
cli/               command implementations and renderers
app/               composition root shared by CLI, API, and evaluation
harness/           bounded loop, provider contracts, tools, events, budgets
evidence/          typed evidence layer over the symbolic kernel
core/              preserved truth-maintenance kernel and deterministic tests
domain/science/    science adapter, fixed Python experiment, deterministic mock
provider/          Meta, OpenRouter, and OpenAI-compatible adapters
eval/              matched comparison runner and reports
runstore/          filesystem JSONL run persistence
server/            local HTTP API, event hub, SSE
web/               embedded dependency-free dashboard
extractor/         retained optional fact-extraction package
version/           build identity
```

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the runtime and evidence semantics.

## Development

```bash
gofmt -s -w .
go vet ./...
go test ./... -count=1 -timeout 120s
go build ./cmd/paradox
```

The deterministic suite uses the mock provider and the real science subprocess. It verifies that `paradox` succeeds after a contradiction and retraction while `baseline` and `evidence` fail the same task.

## License and provenance

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE). PARADOX is derived from the Velarix Epistemic Protocol; the symbolic truth-maintenance kernel and its tests are preserved with attribution.
