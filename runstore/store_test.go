package runstore_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qeinstein/paradox/harness"
	"github.com/qeinstein/paradox/runstore"
)

func TestValidateRunID(t *testing.T) {
	valid := []string{"run-20261023-120000-abcdef", "manual_run.1", "ABC-123"}
	for _, runID := range valid {
		if err := runstore.ValidateRunID(runID); err != nil {
			t.Fatalf("expected %q to be valid: %v", runID, err)
		}
	}
	invalid := []string{"", " ", "../escape", "a/b", `a\b`, ".", "..", "with space"}
	for _, runID := range invalid {
		if err := runstore.ValidateRunID(runID); err == nil {
			t.Fatalf("expected %q to be rejected", runID)
		}
	}
}

func TestStoreRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	store, err := runstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest := harness.RunManifest{
		RunID: "../outside", CreatedAt: time.Now().UTC(),
		TaskID: "task", TaskPrompt: "prompt", Domain: "test",
		Mode: harness.Paradox, Provider: "mock", Model: "mock",
	}
	if _, err := store.Create(manifest); err == nil {
		t.Fatal("expected Create to reject path traversal")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "outside")); !os.IsNotExist(err) {
		t.Fatal("path traversal may have created a directory")
	}
	if _, err := store.ReadEvents("../outside"); err == nil || !strings.Contains(err.Error(), "run id") {
		t.Fatalf("expected ReadEvents to reject path traversal, got %v", err)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	store, err := runstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manifest := harness.RunManifest{
		RunID: "run-test-1", CreatedAt: time.Now().UTC(),
		TaskID: "task", TaskPrompt: "prompt", Domain: "test",
		Mode: harness.Paradox, Provider: "mock", Model: "mock",
	}
	writer, err := store.Create(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Record(harness.Event{Seq: 1, RunID: manifest.RunID, Type: "run_started", Time: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(manifest.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Manifest.RunID != manifest.RunID || len(record.Events) != 1 {
		t.Fatalf("unexpected round trip: %+v", record)
	}
}
