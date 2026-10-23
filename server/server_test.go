package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qeinstein/paradox/app"
	"github.com/qeinstein/paradox/domain/science"
	"github.com/qeinstein/paradox/evidence"
	"github.com/qeinstein/paradox/harness"
	"github.com/qeinstein/paradox/server"
)

func newTestServer(t *testing.T, apiKey string) *httptest.Server {
	t.Helper()
	srv, err := server.New(server.Config{
		RunsDir: filepath.Join(t.TempDir(), "runs"),
		EvalDir: filepath.Join(t.TempDir(), "evals"),
		APIKey:  apiKey,
		Version: "test",
		ProviderFactory: func(cfg app.ProviderConfig) (harness.ModelProvider, error) {
			return science.NewMockProvider(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		httpServer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return httpServer
}

func TestRunLifecycleAndEvidenceAPI(t *testing.T) {
	httpServer := newTestServer(t, "")
	body := []byte(`{"task_id":"science-primality-v1","mode":"paradox","provider":"mock","budget":{"max_steps":12,"max_tool_calls":30,"max_tokens":30000,"max_cost_usd":1,"timeout_ms":30000,"max_retries":0,"tool_timeout_ms":30000}}`)
	response, err := http.Post(httpServer.URL+"/api/runs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("start run status=%d body=%s", response.StatusCode, data)
	}
	var started struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&started); err != nil {
		t.Fatal(err)
	}
	if started.RunID == "" {
		t.Fatal("missing run id")
	}

	deadline := time.Now().Add(30 * time.Second)
	var detail struct {
		Status   string              `json:"status"`
		Manifest harness.RunManifest `json:"manifest"`
		Result   *harness.RunResult  `json:"result"`
		Evidence *evidence.Snapshot  `json:"evidence"`
	}
	for time.Now().Before(deadline) {
		response, err := http.Get(httpServer.URL + "/api/runs/" + started.RunID)
		if err != nil {
			t.Fatal(err)
		}
		err = json.NewDecoder(response.Body).Decode(&detail)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if detail.Status == "completed" || detail.Status == "failed" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if detail.Status != "completed" {
		t.Fatalf("run did not complete: %+v", detail)
	}
	if detail.Result == nil || !detail.Result.Success {
		t.Fatalf("expected successful paradox run: %+v", detail.Result)
	}
	if detail.Evidence == nil || detail.Evidence.Stats.Contradictions == 0 || detail.Evidence.Stats.Retractions == 0 {
		t.Fatalf("expected contradiction and retractions: %+v", detail.Evidence)
	}

	response, err = http.Get(httpServer.URL + "/api/runs/" + started.RunID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	var events []harness.Event
	if err := json.NewDecoder(response.Body).Decode(&events); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(events) == 0 {
		t.Fatal("expected persisted events")
	}

	var artifact string
	for _, node := range detail.Evidence.Nodes {
		if len(node.Artifacts) > 0 {
			artifact = node.Artifacts[0].Name
			break
		}
	}
	if artifact == "" {
		t.Fatal("expected an experiment artifact")
	}
	response, err = http.Get(httpServer.URL + "/api/runs/" + started.RunID + "/artifacts/" + artifact)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("artifact status=%d", response.StatusCode)
	}
	artifactData, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !bytes.Contains(artifactData, []byte("primality_scan")) {
		t.Fatalf("unexpected artifact: %s", artifactData)
	}
}

func TestEventStreamReplaysAndCloses(t *testing.T) {
	httpServer := newTestServer(t, "")
	body := []byte(`{"task_id":"science-primality-v1","mode":"paradox","provider":"mock","budget":{"max_steps":12,"max_tool_calls":30,"max_tokens":30000,"max_cost_usd":1,"timeout_ms":30000,"max_retries":0,"tool_timeout_ms":30000}}`)
	response, err := http.Post(httpServer.URL+"/api/runs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		RunID string `json:"run_id"`
	}
	_ = json.NewDecoder(response.Body).Decode(&started)
	response.Body.Close()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(httpServer.URL + "/api/runs/" + started.RunID)
		if err != nil {
			t.Fatal(err)
		}
		var detail struct {
			Status string `json:"status"`
		}
		_ = json.NewDecoder(response.Body).Decode(&detail)
		response.Body.Close()
		if detail.Status == "completed" || detail.Status == "failed" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	request, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/api/runs/"+started.RunID+"/events/stream", nil)
	stream, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("stream status=%d", stream.StatusCode)
	}
	if !strings.HasPrefix(stream.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("unexpected content type: %s", stream.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(stream.Body)
	found := false
	for index := 0; index < 200; index++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.Contains(line, "run_finished") || strings.Contains(line, "server_run_finished") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected a terminal event in the SSE replay")
	}
}

func TestAPIKeyProtectsRoutes(t *testing.T) {
	httpServer := newTestServer(t, "secret-key")
	response, err := http.Get(httpServer.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health should be public: %d", response.StatusCode)
	}
	response.Body.Close()

	response, err = http.Get(httpServer.URL + "/api/runs")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.StatusCode)
	}
	response.Body.Close()

	request, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/api/runs", nil)
	request.Header.Set("X-API-Key", "secret-key")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected authenticated 200, got %d", response.StatusCode)
	}
	response.Body.Close()
}
