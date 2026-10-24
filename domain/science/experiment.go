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
	"strconv"
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
var exponentLiteral = regexp.MustCompile(`\^\s*([0-9]+)`)

func primalityScanTool() harness.Tool {
	return primalityScanToolWithDefault("n*n+n+41")
}

func primalityScanToolWithDefault(defaultExpression string) harness.Tool {
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
		Handler: func(ctx context.Context, tc *harness.ToolContext, raw json.RawMessage) (*harness.ToolResult, error) {
			return runExperimentWithDefault(ctx, tc, raw, defaultExpression)
		},
	}
}

func runExperiment(ctx context.Context, tc *harness.ToolContext, raw json.RawMessage) (*harness.ToolResult, error) {
	return runExperimentWithDefault(ctx, tc, raw, "n*n+n+41")
}

func runExperimentWithDefault(ctx context.Context, tc *harness.ToolContext, raw json.RawMessage, defaultExpression string) (*harness.ToolResult, error) {
	var args scanArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("invalid experiment arguments: %w", err)
	}
	if args.Experiment != "primality_scan" {
		return nil, fmt.Errorf("unsupported experiment %q", args.Experiment)
	}
	if strings.TrimSpace(args.Expression) == "" {
		args.Expression = defaultExpression
	}
	if len(args.Expression) > 120 || !safeExpression.MatchString(args.Expression) {
		return nil, errors.New("expression must contain only n, digits, whitespace, and + - * ^ ( )")
	}
	for _, match := range exponentLiteral.FindAllStringSubmatch(args.Expression, -1) {
		exponent, err := strconv.Atoi(match[1])
		if err != nil || exponent > 12 {
			return nil, errors.New("exponents must be between 0 and 12")
		}
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
	if tc == nil || strings.TrimSpace(tc.WorkDir) == "" || strings.TrimSpace(tc.ArtifactsDir) == "" {
		return nil, evidence.ArtifactRef{}, errors.New("experiment requires a run work directory and artifacts directory")
	}
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
	if stdout.truncated {
		return nil, evidence.ArtifactRef{}, errors.New("experiment output exceeded the 1 MiB limit")
	}
	var result scanResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, evidence.ArtifactRef{}, fmt.Errorf("could not parse experiment output: %w", err)
	}
	if result.Tested != args.End-args.Start+1 {
		return nil, evidence.ArtifactRef{}, fmt.Errorf("experiment returned %d tested values, expected %d", result.Tested, args.End-args.Start+1)
	}
	if result.Experiment != args.Experiment || result.Expression != args.Expression || result.Start != args.Start || result.End != args.End {
		return nil, evidence.ArtifactRef{}, errors.New("experiment output did not match the requested experiment")
	}
	if result.PrimeCount < 0 || result.CompositeCount < 0 || result.PrimeCount+result.CompositeCount != result.Tested {
		return nil, evidence.ArtifactRef{}, errors.New("experiment returned inconsistent primality counts")
	}

	artifactData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, evidence.ArtifactRef{}, err
	}
	sum := sha256.Sum256(artifactData)
	hash := hex.EncodeToString(sum[:])
	result.ArtifactSHA256 = hash
	name := fmt.Sprintf("%s-%s.json", "primality", hash[:16])
	if err := os.MkdirAll(tc.ArtifactsDir, 0o700); err != nil {
		return nil, evidence.ArtifactRef{}, fmt.Errorf("create experiment artifacts directory: %w", err)
	}
	artifactPath := filepath.Join(tc.ArtifactsDir, name)
	if err := writeArtifact(artifactPath, artifactData); err != nil {
		return nil, evidence.ArtifactRef{}, fmt.Errorf("persist experiment artifact: %w", err)
	}
	artifact := evidence.ArtifactRef{
		Name: name, SHA256: hash, MediaType: "application/json",
		Path: artifactPath,
	}
	return &result, artifact, nil
}

func writeArtifact(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".artifact-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
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
const pythonScanScript = `import ast, json, sys

import resource

def set_limit(name, requested):
    _resource = getattr(resource, name, None)
    if _resource is None:
        return
    _soft, _hard = resource.getrlimit(_resource)
    if _soft != resource.RLIM_INFINITY:
        requested = min(requested, _soft)
    if _hard != resource.RLIM_INFINITY:
        requested = min(requested, _hard)
    resource.setrlimit(_resource, (requested, requested))

set_limit("RLIMIT_CPU", 10)
for _name, _limit in (("RLIMIT_AS", 512 * 1024 * 1024), ("RLIMIT_NOFILE", 128), ("RLIMIT_FSIZE", 1024 * 1024)):
    set_limit(_name, _limit)

args = json.loads(sys.argv[1])
start = int(args["start"])
end = int(args["end"])
tree = ast.parse(args["expression"].replace("^", "**"), mode="eval")

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

def polynomial_value(node, n):
    if isinstance(node, ast.Expression):
        return polynomial_value(node.body, n)
    if isinstance(node, ast.Constant) and type(node.value) is int:
        value = node.value
    elif isinstance(node, ast.Name) and node.id == "n":
        value = n
    elif isinstance(node, ast.UnaryOp) and isinstance(node.op, (ast.UAdd, ast.USub)):
        value = polynomial_value(node.operand, n)
        if isinstance(node.op, ast.USub):
            value = -value
    elif isinstance(node, ast.BinOp):
        left = polynomial_value(node.left, n)
        if isinstance(node.op, ast.Pow):
            if not isinstance(node.right, ast.Constant) or type(node.right.value) is not int:
                raise ValueError("exponents must be integer literals")
            exponent = node.right.value
            if exponent < 0 or exponent > 12:
                raise ValueError("exponents must be between 0 and 12")
            value = left ** exponent
        else:
            right = polynomial_value(node.right, n)
            if isinstance(node.op, ast.Add):
                value = left + right
            elif isinstance(node.op, ast.Sub):
                value = left - right
            elif isinstance(node.op, ast.Mult):
                value = left * right
            else:
                raise ValueError("only integer polynomial operations are supported")
    else:
        raise ValueError("expression contains an unsupported syntax node")
    if abs(value) > 10**12:
        raise ValueError("polynomial intermediate exceeds the experiment's safe bound")
    return value

def polynomial(n):
    return polynomial_value(tree, n)

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
print(json.dumps(result))
`
