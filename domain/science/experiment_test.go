package science_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qeinstein/paradox/domain/science"
	"github.com/qeinstein/paradox/harness"
)

func runScan(t *testing.T, expression string, start, end int, artifactsDir string) (*harness.ToolResult, error) {
	t.Helper()
	adapter := science.New()
	tool := adapter.Tools()[0]
	workDir := t.TempDir()
	args, err := json.Marshal(map[string]any{
		"experiment": "primality_scan", "expression": expression,
		"start": start, "end": end,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return tool.Handler(ctx, &harness.ToolContext{
		WorkDir: workDir, ArtifactsDir: artifactsDir,
	}, args)
}

func TestPrimalityScanPersistsReproducibleEvidenceArtifact(t *testing.T) {
	artifactsDir := t.TempDir()
	result, err := runScan(t, "n*n+n+41", 0, 100, artifactsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != 2 {
		t.Fatalf("expected range and counterexample observations, got %d", len(result.Evidence))
	}
	if len(result.Artifacts) != 1 {
		t.Fatalf("expected one artifact reference, got %+v", result.Artifacts)
	}
	artifact := result.Artifacts[0]
	data, err := os.ReadFile(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	checksum := sha256.Sum256(data)
	if hex.EncodeToString(checksum[:]) != artifact.SHA256 {
		t.Fatalf("artifact checksum does not match persisted bytes: %+v", artifact)
	}
	if artifact.Path == "" || filepath.Dir(artifact.Path) != artifactsDir {
		t.Fatalf("unexpected artifact path: %+v", artifact)
	}
	for _, observation := range result.Evidence {
		if observation.Provenance["artifact_sha256"] != artifact.SHA256 {
			t.Fatalf("evidence provenance checksum differs from artifact: %+v", observation.Provenance)
		}
		if len(observation.Artifacts) != 1 || observation.Artifacts[0].SHA256 != artifact.SHA256 {
			t.Fatalf("observation has no matching artifact reference: %+v", observation.Artifacts)
		}
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record["tested"] != float64(101) || record["composite_count"] != float64(14) {
		t.Fatalf("unexpected experiment artifact: %+v", record)
	}
}

func TestPrimalityScanRejectsUnboundedExponent(t *testing.T) {
	started := time.Now()
	_, err := runScan(t, "n^999999999", 0, 1, t.TempDir())
	if err == nil {
		t.Fatal("expected exponent bound to reject the expression")
	}
	if !strings.Contains(err.Error(), "exponents must be between") {
		t.Fatalf("expected expression safety error, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("bounded expression took too long to reject: %s", elapsed)
	}
}

func TestPrimalityScanFailsWhenArtifactCannotBePersisted(t *testing.T) {
	root := t.TempDir()
	blockedPath := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blockedPath, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runScan(t, "n*n+n+41", 0, 2, blockedPath)
	if err == nil {
		t.Fatal("expected artifact persistence failure")
	}
}
