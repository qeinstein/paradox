package science

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/qeinstein/paradox/evidence"
	"github.com/qeinstein/paradox/harness"
)

type scanArgs struct {
	Experiment string `json:"experiment"`
	Expression string `json:"expression"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
}

type scanResult struct {
	Experiment     string         `json:"experiment"`
	Expression     string         `json:"expression"`
	Start          int            `json:"start"`
	End            int            `json:"end"`
	Tested         int            `json:"tested"`
	PrimeCount     int            `json:"prime_count"`
	CompositeCount int            `json:"composite_count"`
	Counterexample map[string]any `json:"counterexample,omitempty"`
	ArtifactSHA256 string         `json:"artifact_sha256,omitempty"`
}

var safeExpression = regexp.MustCompile(`^[0-9nN+\-*^() \t]+$`)

func primalityScanTool() harness.Tool {
	return harness.Tool{
		Name:        "run_experiment",
		Description: "Execute a deterministic computational experiment in an isolated Python subprocess. Supported experiment: primality_scan, which evaluates an integer polynomial over an inclusive integer range and returns primes, composites, and the first counterexample.",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "experiment": {"type": "string", "enum": ["primality_scan"]},
    "expression": {"type": "string", "description": "Integer polynomial in n, using +, -, *, ^, and parentheses, e.g. n*n+n+41."},
    "start": {"type": "integer", "minimum": 0},
    "end": {"type": "integer", "minimum": 0}
  },
  "required": ["experiment", "start", "end"]
}`),
		Handler: runExperiment,
	}
}

func runExperiment(ctx context.Context, tc *harness.ToolContext, raw json.RawMessage) (*harness.ToolResult, error) {
	var args scanArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("invalid experiment arguments: %w", err)
	}
	if args.Experiment != "primality_scan" {
		return nil, fmt.Errorf("unsupported experiment %q", args.Experiment)
	}
	if strings.TrimSpace(args.Expression) == "" {
		args.Expression = "n*n+n+41"
	}
	if len(args.Expression) > 120 || !safeExpression.MatchString(args.Expression) {
		return nil, errors.New("expression must contain only n, digits, whitespace, and + - * ^ ( )")
	}
	if args.Start < 0 || args.End < args.Start {
		return nil, errors.New("range must satisfy 0 <= start <= end")
	}
	if args.End-args.Start > 100000 {
		return nil, errors.New("range is too large (maximum 100001 values)")
	}
	if args.End > 1000000 {
		return nil, errors.New("end must be <= 1000000")
	}

	result, artifact, err := executePrimalityScan(ctx, tc, args)
	if err != nil {
		return nil, err
	}
	rangeKey := fmt.Sprintf("primality:poly:%s:range:%d-%d", normalizeExpression(args.Expression), args.Start, args.End)
	rangeValue := "all_prime"
	rangeText := fmt.Sprintf("primality_scan f(n)=%s over n=[%d,%d]: %d/%d prime, %d composite", args.Expression, args.Start, args.End, result.PrimeCount, result.Tested, result.CompositeCount)
	if result.CompositeCount > 0 {
		rangeValue = "has_composite"
	}
	rangeObservation := evidence.ObservationInput{
		Text: rangeText, ClaimKey: rangeKey, ClaimValue: rangeValue,
		Subject: args.Expression, Predicate: "all_prime_over_range",
		Object: fmt.Sprintf("%d-%d", args.Start, args.End),
		Source: evidence.SourceExperiment,
		Provenance: map[string]any{
			"source_type": "experiment", "tool": "run_experiment",
			"experiment": "primality_scan", "artifact_sha256": result.ArtifactSHA256,
		},
		Metadata: map[string]any{
			"experiment": "primality_scan", "expression": args.Expression,
			"start": args.Start, "end": args.End, "tested": result.Tested,
			"prime_count": result.PrimeCount, "composite_count": result.CompositeCount,
			"counterexample": result.Counterexample, "artifact_sha256": result.ArtifactSHA256,
		},
		Artifacts: []evidence.ArtifactRef{artifact},
	}
	observations := []evidence.ObservationInput{rangeObservation}
	if result.Counterexample != nil {
		n, _ := number(result.Counterexample["n"])
		value, _ := number(result.Counterexample["value"])
		factors, _ := result.Counterexample["factors"].([]any)
		observations = append(observations, evidence.ObservationInput{
			Text:       fmt.Sprintf("f(%d)=%d is composite (factors %v)", n, value, factors),
			ClaimKey:   fmt.Sprintf("primality:n=%d:%s", n, normalizeExpression(args.Expression)),
			ClaimValue: "composite", Subject: fmt.Sprintf("n=%d", n),
			Predicate: "is_prime", Object: "false", Source: evidence.SourceExperiment,
			Provenance: map[string]any{
				"source_type": "experiment", "tool": "run_experiment",
				"experiment": "primality_scan", "artifact_sha256": result.ArtifactSHA256,
			},
			Metadata: map[string]any{
				"experiment": "primality_scan", "expression": args.Expression,
				"n": n, "value": value, "factors": factors,
				"artifact_sha256": result.ArtifactSHA256,
			},
			Artifacts: []evidence.ArtifactRef{artifact},
		})
	}
	return &harness.ToolResult{
		Content: rangeText,
		Data: map[string]any{
			"experiment": "primality_scan", "expression": args.Expression,
			"start": args.Start, "end": args.End, "tested": result.Tested,
			"prime_count": result.PrimeCount, "composite_count": result.CompositeCount,
			"counterexample": result.Counterexample, "artifact_sha256": result.ArtifactSHA256,
			"reproducible": true,
		},
		Evidence:  observations,
		Artifacts: []evidence.ArtifactRef{artifact},
	}, nil
}

func executePrimalityScan(ctx context.Context, tc *harness.ToolContext, args scanArgs) (*scanResult, evidence.ArtifactRef, error) {
	dir, err := os.MkdirTemp(tc.WorkDir, "science-scan-")
	if err != nil {
		return nil, evidence.ArtifactRef{}, err
	}
	defer os.RemoveAll(dir)
	scriptPath := filepath.Join(dir, "primality_scan.py")
	if err := os.WriteFile(scriptPath, []byte(pythonScanScript), 0o600); err != nil {
		return nil, evidence.ArtifactRef{}, err
	}
	payload, err := json.Marshal(args)
	if err != nil {
		return nil, evidence.ArtifactRef{}, err
	}

	stdout := &limitedBuffer{limit: 1 << 20}
	stderr := &limitedBuffer{limit: 64 << 10}
	var cmd *exec.Cmd
	if prlimitPath, lookErr := exec.LookPath("prlimit"); lookErr == nil {
		cmd = exec.CommandContext(ctx, prlimitPath,
			"--as=536870912", "--cpu=10", "--nofile=128", "--nproc=64", "--",
			"python3", "-I", scriptPath, string(payload))
	} else {
		cmd = exec.CommandContext(ctx, "python3", "-I", scriptPath, string(payload))
	}
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + dir, "TMPDIR=" + dir,
		"LANG=C", "LC_ALL=C",
		"PYTHONNOUSERSITE=1", "PYTHONDONTWRITEBYTECODE=1",
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, evidence.ArtifactRef{}, fmt.Errorf("experiment cancelled or timed out: %w", ctx.Err())
	}
	if runErr != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = runErr.Error()
		}
		return nil, evidence.ArtifactRef{}, fmt.Errorf("experiment subprocess failed: %s", message)
	}
	var result scanResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, evidence.ArtifactRef{}, fmt.Errorf("could not parse experiment output: %w", err)
	}
	if result.Tested != args.End-args.Start+1 {
		return nil, evidence.ArtifactRef{}, fmt.Errorf("experiment returned %d tested values, expected %d", result.Tested, args.End-args.Start+1)
	}

	artifactData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, evidence.ArtifactRef{}, err
	}
	sum := sha256.Sum256(artifactData)
	hash := hex.EncodeToString(sum[:])
	name := fmt.Sprintf("%s-%s.json", "primality", hash[:16])
	if tc.ArtifactsDir != "" {
		if err := os.MkdirAll(tc.ArtifactsDir, 0o755); err == nil {
			_ = os.WriteFile(filepath.Join(tc.ArtifactsDir, name), artifactData, 0o644)
		}
	}
	artifact := evidence.ArtifactRef{
		Name: name, SHA256: hash, MediaType: "application/json",
		Path: filepath.Join(tc.ArtifactsDir, name),
	}
	return &result, artifact, nil
}

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	if w.buf.Len() >= w.limit {
		w.truncated = true
		return len(p), nil
	}
	remaining := w.limit - w.buf.Len()
	if len(p) > remaining {
		w.buf.Write(p[:remaining])
		w.truncated = true
		return len(p), nil
	}
	return w.buf.Write(p)
}

func (w *limitedBuffer) Bytes() []byte  { return w.buf.Bytes() }
func (w *limitedBuffer) String() string { return w.buf.String() }

func normalizeExpression(expression string) string {
	replacer := strings.NewReplacer(" ", "", "\t", "", "^", "*")
	return strings.ToLower(replacer.Replace(expression))
}

// pythonScanScript is fixed and receives only validated JSON arguments. The
// agent cannot supply arbitrary Python source.
const pythonScanScript = `import json, sys, hashlib

args = json.loads(sys.argv[1])
start = int(args["start"])
end = int(args["end"])
expr = args["expression"].replace("^", "**")

def is_prime(value):
    if value < 2:
        return False
    if value % 2 == 0:
        return value == 2
    factor = 3
    while factor * factor <= value:
        if value % factor == 0:
            return False
        factor += 2
    return True

def first_factors(value):
    if value < 2:
        return []
    factor = 2
    while factor * factor <= value:
        if value % factor == 0:
            return [factor, value // factor]
        factor += 1
    return [value]

def polynomial(n):
    return eval(expr, {"__builtins__": {}}, {"n": n})

primes = 0
composites = 0
counterexample = None
tested = 0
for n in range(start, end + 1):
    value = polynomial(n)
    if abs(value) > 10**12:
        raise ValueError("polynomial value exceeds the experiment's safe bound")
    tested += 1
    if is_prime(value):
        primes += 1
    else:
        composites += 1
        if counterexample is None:
            counterexample = {"n": n, "value": value, "factors": first_factors(value)}

result = {
    "experiment": "primality_scan",
    "expression": args["expression"],
    "start": start,
    "end": end,
    "tested": tested,
    "prime_count": primes,
    "composite_count": composites,
    "counterexample": counterexample,
}
result["artifact_sha256"] = hashlib.sha256(json.dumps(result, sort_keys=True).encode()).hexdigest()
print(json.dumps(result))
`
