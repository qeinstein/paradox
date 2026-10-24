# PARADOX v0.2 Offline Readiness

This checklist records offline verification performed for the v0.2 benchmark-readiness milestone. No Meta or OpenRouter credentials were requested or used, and no live inference requests were made. **PASS** means the listed command ran successfully. **BLOCKED** means the relevant behavior requires an unavailable live provider or a broader external evaluation. Mock results are protocol and harness checks only.

## Readiness gate

| Status | Area | Command and outcome |
|---|---|---|
| PASS | Go formatting | `files="$(gofmt -s -l .)"; test -z "$files"` — no files reported. |
| PASS | Toolchain and module integrity | `GOTOOLCHAIN=go1.26.10 go version && GOTOOLCHAIN=go1.26.10 go mod verify` — `go1.26.10 linux/amd64`; all modules verified. `go.mod` requires Go 1.26.10 or later. |
| PASS | Static analysis | `GOTOOLCHAIN=go1.26.10 go vet ./...` — exit 0. |
| PASS | Full Go tests | `GOTOOLCHAIN=go1.26.10 go test ./... -count=1 -timeout 180s` — all packages passed. The default Python environment did not have ScienceWorld installed, so its environment-dependent test skipped; the real-environment check below ran in a temporary venv. |
| PASS | Race detector | `GOTOOLCHAIN=go1.26.10 go test -race ./... -count=1 -timeout 240s` — all packages passed. |
| PASS | CLI build and version | `build_dir="$(mktemp -d /tmp/paradox-build-XXXXXX)"; GOTOOLCHAIN=go1.26.10 go build -o "$build_dir/paradox" ./cmd/paradox && "$build_dir/paradox" version` — built successfully and reported PARADOX `0.2.0` on Go 1.26.10. |
| PASS | Vulnerability scan | `GOTOOLCHAIN=go1.26.10 GOBIN=/tmp/paradox-tools-lP8o37 go install golang.org/x/vuln/cmd/govulncheck@latest && GOTOOLCHAIN=go1.26.10 /tmp/paradox-tools-lP8o37/govulncheck ./...` — `No vulnerabilities found.` A preliminary scan on Go 1.26.0 identified vulnerable standard-library code; the project now requires Go 1.26.10, and the final scan is clean. |
| PASS | Offline preflight | `go run ./cmd/paradox doctor --provider mock --eval-config eval/configs/internal-science-v0.2.json --dry-run --json` — overall `PASS`; request serialization reported `network_contacted: false`. Optional checks explicitly reported ScienceWorld unavailable to the default Python and mock-model tool capability as `BLOCKED`. |
| PASS | Deterministic science regression | `go run ./cmd/paradox run --provider mock --mode paradox --runs-dir "$scratch_dir/runs" --json` with a fresh `scratch_dir` — exit 0; `science-primality-v1` completed successfully, score 1.0, with six tool calls and evidence retraction. The report records mock cost as unavailable. |
| PASS | Matched evaluation output | `go run ./cmd/paradox eval --config eval/configs/internal-science-v0.2.json --runs-dir "$scratch_dir/runs" --output "$scratch_dir/evals" --markdown "$scratch_dir/report.md" --json` with a fresh `scratch_dir` — exit 0; 12 matched runs across four development tasks and three modes, with no execution errors. The one-trial fixture report scored 8/12 overall and all four `paradox` runs passed; baseline and evidence modes failed the primality and measurement-revision fixtures. It recorded two correct verifications, zero incorrect retractions, extra verification tool calls, and unavailable mock cost. These are internal mock diagnostics, not real-model results or evidence of general improvement. |
| PASS | Meta and OpenRouter offline workflow fixtures | Included in `go test ./...`: local HTTP mock servers ran complete multi-turn agent workflows through the actual runner, including OpenRouter tool calls and Meta `previous_response_id` chaining. No provider was contacted. |
| PASS | Adversarial evidence checks | Included in `go test ./...`: alternative justifications, expired assumptions, unsupported/nonexistent verifier evidence, inconsistent observations, resolved-assumption handling, exhausted verification budget, and a three-step invalidation/replanning sequence passed. |
| PASS | Cross-domain transfer | Included in `go test ./...`: `domain/software` executed a failing candidate, revised it after regression evidence, and verified the corrected candidate. |
| PASS | Secret handling | Included in `go test ./...`: provider credential-echo redaction and restricted run/artifact permissions passed. |
| PASS | ScienceWorld real-environment smoke | Installed the official `scienceworld` 1.2.3 package and `py4j` into a temporary Python 3.11 venv, then reran `PATH="/tmp/paradox-scienceworld-CFOGHu/venv/bin:$PATH" GOTOOLCHAIN=go1.26.10 go test -v ./benchmark/scienceworld -run TestOfficialScienceWorldEnvironmentSmokeWhenInstalled -count=1` — PASS in 3.55s. The test loaded official task `boil`, variation `0`, ran a real `look around` action through the PARADOX harness, recorded the returned official score and observation as evidence, and correctly left the incomplete episode unsuccessful at score 0. Java 17 was present. |
| BLOCKED | Live Meta inference | Not attempted by design. No credentials were available or requested. Offline fixtures do not establish live API compatibility. |
| BLOCKED | Live OpenRouter inference and current model capability discovery | Not attempted by design. Offline fixtures do not prove a routed model's live tool, reasoning, or structured-output support. Operator confirmation remains model-specific. |
| BLOCKED | General benchmark performance claims | No real-model trials or held-out benchmark comparison were run. The internal evaluation is one trial per mode on development tasks only. |
| BLOCKED | DiscoveryWorld integration | Reviewed as a future scientific-discovery benchmark; no adapter is implemented. |

ScienceWorld installation requirements and APIs were checked against its [official repository](https://github.com/allenai/ScienceWorld), which documents Java 1.8+, Python 3.8+, and `pip install scienceworld`. Provider schemas were checked against the [Meta Responses API](https://dev.meta.ai/docs/protocols/responses) and [OpenRouter Chat Completions API](https://openrouter.ai/docs/api/api-reference/chat/send-chat-completion-request). The official Meta documentation describes multi-turn `previous_response_id` chaining; OpenRouter documents `max_completion_tokens` and notes that `max_tokens` is deprecated. OpenRouter capabilities remain model-dependent. The [DiscoveryWorld repository](https://github.com/allenai/discoveryworld) was reviewed but is not integrated.

## Scope limits

- ScienceWorld is operational for the tested task/action path, but only `boil`, variation 0, received a real-environment smoke run. A full task/variation sweep remains outstanding.
- Standard CI does not install ScienceWorld. Its adapter tests skip the official-environment smoke when the package is absent; the temporary-venv command above verified the integration without adding a CI dependency.
- The statistical permutation test, integer optimization task, and measurement-revision task use fixed, predefined operations. Model-generated arbitrary code is not exposed.
- The internal matched report has only one trial per task and mode. Per-task uncertainty estimates are therefore unavailable; aggregate paired summaries are descriptive and do not support general performance claims.
- The software-engineering transfer test is a narrow deterministic fixture, not a general coding agent.
- The OpenRouter adapter records the requested model ID but does not pin the routed upstream provider. Comparisons are not necessarily provider-endpoint matched.
- Verification accuracy is based on domain-executable checks; it does not verify private model reasoning.
- The harness enforces reported cost after each inference response. A first response can exceed a configured cap before its returned usage is known; use the provider's account-level spend controls too.

## First live smoke recommendation

When credentials are available, first verify the selected OpenRouter model's current tool support in its live catalog. Then run one `science-primality-v1` task in `paradox` mode with `--max-steps 12 --max-tool-calls 8 --max-tokens 4096 --max-cost 0.50 --require-cost --timeout 5m --tool-timeout 30s --max-retries 0`. Use a provider/account spend cap of at most USD 1 for the initial run. `--require-cost` stops later calls when the provider omits cost, while the token and step budgets bound the first request. Record the exact model ID, capability confirmation, returned cost, evidence trace, and commit; do not treat one run as a performance result.
