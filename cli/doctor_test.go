package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDoctorDryRunSerializesWithoutNetworkOrCredentialLeak(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	const secret = "doctor-sensitive-key"
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"doctor", "--provider", "openai-compatible", "--model", "local-fixture",
		"--base-url", server.URL, "--api-key", secret,
		"--runs-dir", t.TempDir(), "--dry-run", "--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doctor failed: code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if requests.Load() != 0 {
		t.Fatalf("offline doctor contacted the configured provider %d times", requests.Load())
	}
	if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
		t.Fatal("doctor output leaked configured credential")
	}
	var report struct {
		CredentialConfigured bool   `json:"credential_configured"`
		Overall              string `json:"overall"`
		DryRun               struct {
			BodySHA256 string `json:"body_sha256"`
			Network    bool   `json:"network_contacted"`
		} `json:"dry_run"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("parse doctor output: %v", err)
	}
	if !report.CredentialConfigured || report.Overall != "PASS" || report.DryRun.Network || len(report.DryRun.BodySHA256) != 64 {
		t.Fatalf("unexpected offline doctor result: %+v", report)
	}
	blockedScienceWorld := false
	for _, check := range report.Checks {
		if check.Name == "scienceworld_benchmark" && check.Status == "BLOCKED" {
			blockedScienceWorld = true
		}
	}
	if !blockedScienceWorld {
		t.Fatal("missing official ScienceWorld dependency status")
	}
}

func TestDoctorOpenRouterModelSyntaxFailsWithoutNetwork(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"doctor", "--provider", "openrouter", "--model", "model-without-provider",
		"--runs-dir", t.TempDir(), "--json",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("invalid OpenRouter model ID passed preflight: %s", stdout.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "OPENROUTER_API_KEY") {
		t.Fatal("doctor should not request credentials")
	}
}

func TestDoctorOpenRouterCapabilitiesAreExplicitOfflineConfiguration(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"doctor", "--provider", "openrouter", "--model", "vendor/model",
		"--tool-calls-supported", "--reasoning-effort", "medium", "--reasoning-supported",
		"--runs-dir", t.TempDir(), "--dry-run", "--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("offline doctor failed: code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	var report struct {
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, check := range report.Checks {
		statuses[check.Name] = check.Status
	}
	if statuses["model_tool_capability"] != "PASS" || statuses["reasoning_capability"] != "PASS" {
		t.Fatalf("operator-confirmed OpenRouter capabilities not reflected: %+v", statuses)
	}
}
