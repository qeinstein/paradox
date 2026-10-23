package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qeinstein/paradox/cli"
	"github.com/qeinstein/paradox/eval"
	"github.com/qeinstein/paradox/harness"
)

func TestRunTraceEvidenceAndEval(t *testing.T) {
	root := t.TempDir()
	runsDir := filepath.Join(root, "runs")
	evalsDir := filepath.Join(root, "evals")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	code := cli.Run(ctx, []string{
		"run", "--provider", "mock", "--mode", "paradox",
		"--runs-dir", runsDir, "--json",
		"--max-steps", "12", "--max-tool-calls", "30", "--max-tokens", "30000",
		"--timeout", "30s", "--tool-timeout", "30s", "--max-retries", "0",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run exit=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	var payload struct {
		Result   *harness.RunResult  `json:"result"`
		Manifest harness.RunManifest `json:"manifest"`
		RunDir   string              `json:"run_dir"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("decode run JSON: %v\n%s", err, stdout.String())
	}
	if payload.Result == nil || !payload.Result.Success {
		t.Fatalf("expected successful run: %+v", payload.Result)
	}
	runID := payload.Result.RunID
	if runID == "" || payload.Result.Snapshot.Stats.Contradictions == 0 {
		t.Fatalf("unexpected run result: %+v", payload.Result)
	}

	stdout.Reset()
	stderr.Reset()
	code = cli.Run(context.Background(), []string{"trace", "--runs-dir", runsDir, runID}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("trace exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "claim_submitted") || !strings.Contains(stdout.String(), "claim_invalidated") {
		t.Fatalf("trace missing key events:\n%s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = cli.Run(context.Background(), []string{"evidence", "--runs-dir", runsDir, runID}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("evidence exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Claims:") || !strings.Contains(stdout.String(), "Retractions:") {
		t.Fatalf("evidence output incomplete:\n%s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = cli.Run(ctx, []string{
		"eval", "--provider", "mock", "--modes", "paradox", "--trials", "1",
		"--runs-dir", runsDir, "--output", evalsDir, "--json",
		"--max-steps", "12", "--max-tool-calls", "30", "--max-tokens", "30000",
		"--timeout", "30s", "--tool-timeout", "30s", "--max-retries", "0",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("eval exit=%d stderr=%s", code, stderr.String())
	}
	var report eval.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode eval JSON: %v\n%s", err, stdout.String())
	}
	if report.Overall.Runs != 1 || report.ByMode["paradox"].SuccessRate != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if _, err := os.Stat(report.Files["md"]); err != nil {
		t.Fatalf("missing markdown report: %v", err)
	}
}

func TestModelsAndVersionAndServeGuards(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), []string{"models", "--provider", "mock", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("models exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "mock-science-v1") {
		t.Fatalf("models output missing mock: %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = cli.Run(context.Background(), []string{"version", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("version exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"version"`) {
		t.Fatalf("version output incomplete: %s", stdout.String())
	}

	t.Setenv("PARADOX_API_KEY", "")
	stdout.Reset()
	stderr.Reset()
	code = cli.Run(context.Background(), []string{"serve", "--addr", "0.0.0.0:8080"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected non-loopback serve guard to exit 2, got %d: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "refusing") {
		t.Fatalf("expected refusal message: %s", stderr.String())
	}
}
