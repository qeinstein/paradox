package eval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qeinstein/paradox/domain/science"
)

func TestDefaultEvaluationConfigIsCompleteAndResolvesInternalTasks(t *testing.T) {
	config := DefaultConfig()
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	tasks, domain, err := config.TasksAndDomain()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 4 || domain.Name() != science.DomainName {
		t.Fatalf("unexpected development config tasks/domain: %d %q", len(tasks), domain.Name())
	}
}

func TestEvaluationConfigRejectsUnknownCredentialFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"id":"unsafe","suite":"science","provider":"mock","model":"mock","modes":["baseline"],"trials":1,"task_ids":["science-primality-v1"],"api_key":"never-accept-secrets","budget":{"max_steps":1,"max_tool_calls":1,"max_tokens":1,"max_cost_usd":1,"timeout_ms":1,"max_retries":0,"tool_timeout_ms":1},"verification":{"enabled":false,"max_plans_per_claim":0,"min_assumption_confidence":0,"timeout_ms":0}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("unknown credential field should be rejected by strict config decoder")
	}
}

func TestEvaluationConfigRequiresOpenRouterToolCapabilityConfirmation(t *testing.T) {
	config := DefaultConfig()
	config.Provider, config.Model = "openrouter", "vendor/model"
	if err := config.Validate(); err == nil {
		t.Fatal("OpenRouter evaluation without tool support confirmation should fail validation")
	}
	config.OpenRouterToolCallsSupported = true
	if err := config.Validate(); err != nil {
		t.Fatalf("explicitly confirmed OpenRouter tool support should validate: %v", err)
	}
}
