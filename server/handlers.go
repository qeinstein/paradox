package server

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/qeinstein/paradox/app"
	"github.com/qeinstein/paradox/domain/science"
	"github.com/qeinstein/paradox/eval"
	"github.com/qeinstein/paradox/evidence"
	"github.com/qeinstein/paradox/harness"
	"github.com/qeinstein/paradox/provider"
)

type startRunRequest struct {
	Task            *harness.Task               `json:"task,omitempty"`
	TaskID          string                      `json:"task_id,omitempty"`
	Mode            string                      `json:"mode,omitempty"`
	Provider        string                      `json:"provider,omitempty"`
	Model           string                      `json:"model,omitempty"`
	Budget          *harness.Budget             `json:"budget,omitempty"`
	Verification    *harness.VerificationPolicy `json:"verification,omitempty"`
	ReasoningEffort string                      `json:"reasoning_effort,omitempty"`
	Temperature     *float64                    `json:"temperature,omitempty"`
	RunID           string                      `json:"run_id,omitempty"`
}

type runListItem struct {
	Manifest harness.RunManifest `json:"manifest"`
	Status   string              `json:"status"`
	Success  *bool               `json:"success,omitempty"`
	Score    *float64            `json:"score,omitempty"`
	Error    string              `json:"error,omitempty"`
}

type runDetail struct {
	Manifest harness.RunManifest `json:"manifest"`
	Status   string              `json:"status"`
	Error    string              `json:"error,omitempty"`
	Result   *harness.RunResult  `json:"result,omitempty"`
	Evidence *evidence.Snapshot  `json:"evidence,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "version": s.cfg.Version,
		"auth_required":    strings.TrimSpace(s.cfg.APIKey) != "",
		"provider_default": defaultProviderName(),
		"model_default":    defaultModelName(defaultProviderName()),
		"time":             time.Now().UTC(),
	})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	type apiModel struct {
		ID           string               `json:"id"`
		Provider     string               `json:"provider"`
		DisplayName  string               `json:"display_name"`
		Capabilities harness.Capabilities `json:"capabilities"`
		Source       string               `json:"source"`
	}
	models := []apiModel{}
	for _, model := range provider.MetaModels() {
		models = append(models, apiModel{
			ID: model.ID, Provider: model.Provider, DisplayName: model.DisplayName,
			Capabilities: model.Capabilities, Source: "documented_catalog",
		})
	}
	models = append(models, apiModel{
		ID: "mock-science-v1", Provider: "mock", DisplayName: "Deterministic Science Mock",
		Capabilities: harness.Capabilities{ToolCalls: true, StructuredOutput: true, Reasoning: true},
		Source:       "built_in",
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"models": models, "source": "built_in_catalog",
		"note": "Meta entries are the documented catalog. Use `paradox models --provider meta --remote` for live enumeration. OpenRouter accepts any current model id and requires OPENROUTER_API_KEY.",
	})
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	manifests, err := s.store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	limit := 100
	if value := r.URL.Query().Get("limit"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if len(manifests) > limit {
		manifests = manifests[:limit]
	}
	items := make([]runListItem, 0, len(manifests))
	for _, manifest := range manifests {
		item := runListItem{Manifest: manifest}
		item.Status, item.Error = s.runStatus(manifest.RunID)
		if result, err := s.store.ReadResult(manifest.RunID); err == nil {
			success := result.Success
			score := result.Score
			item.Success = &success
			item.Score = &score
			if item.Error == "" {
				item.Error = result.Error
			}
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": items})
}

func (s *Server) handleStartRun(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var request startRunRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	task, err := taskFromRequest(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	mode := harness.Paradox
	if strings.TrimSpace(request.Mode) != "" {
		mode, err = harness.ParseMode(strings.TrimSpace(request.Mode))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	providerName := strings.ToLower(firstNonEmpty(request.Provider, defaultProviderName()))
	modelName := firstNonEmpty(request.Model, defaultModelName(providerName))
	cfg := app.ProviderConfig{
		Name: providerName, Model: modelName, APIKey: providerKey(providerName),
		BaseURL:               os.Getenv("PARADOX_BASE_URL"),
		InputPricePerMillion:  envFloat("PARADOX_INPUT_PRICE_PER_MILLION"),
		OutputPricePerMillion: envFloat("PARADOX_OUTPUT_PRICE_PER_MILLION"),
	}
	modelProvider, err := s.cfg.ProviderFactory(cfg)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	budget := harness.DefaultBudget()
	if request.Budget != nil {
		budget = *request.Budget
	}
	verification := harness.DefaultVerificationPolicy()
	if request.Verification != nil {
		verification = *request.Verification
	}
	runID := strings.TrimSpace(request.RunID)
	if runID == "" {
		runID = harness.NewRunID()
	}
	if err := s.checkRunID(runID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if _, exists := s.runs[runID]; exists {
		s.mu.Unlock()
		cancel()
		writeError(w, http.StatusConflict, "run is already active")
		return
	}
	s.runs[runID] = &runState{
		id: runID, status: "starting", cancel: cancel, started: time.Now().UTC(),
	}
	s.mu.Unlock()
	s.wg.Add(1)
	go s.executeRun(ctx, cancel, runID, app.RunOptions{
		Task: task, Mode: mode, Provider: modelProvider, Domain: science.New(),
		Budget: budget, Verification: verification, RunsDir: s.store.Root(),
		GitCommit: app.GitCommit(), ReasoningEffort: strings.TrimSpace(request.ReasoningEffort),
		Temperature: request.Temperature, RunID: runID,
		ManifestMetadata: map[string]any{"source": "http_api"},
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": runID, "status": "started"})
}

func (s *Server) executeRun(ctx context.Context, cancel context.CancelFunc, runID string, options app.RunOptions) {
	defer s.wg.Done()
	defer cancel()
	s.setRunState(runID, "running", "")
	options.EventSink = func(event harness.Event) { s.hub.Publish(runID, event) }
	output, err := app.Run(ctx, options)
	status := "completed"
	message := ""
	if err != nil {
		status = "failed"
		message = err.Error()
	} else if output.Result != nil && output.Result.Error != "" {
		status = "failed"
		message = output.Result.Error
	}
	if ctx.Err() != nil {
		status = "cancelled"
		if message == "" {
			message = ctx.Err().Error()
		}
	}
	s.setRunState(runID, status, message)
	event := harness.Event{
		Seq: 0, RunID: runID, Time: time.Now().UTC(), Type: "server_run_finished",
		Data: map[string]any{"status": status, "error": message},
	}
	if output != nil && output.Result != nil {
		event.Data["success"] = output.Result.Success
		event.Data["score"] = output.Result.Score
		event.Data["run_dir"] = output.RunDir
	}
	s.hub.Publish(runID, event)
	s.hub.CloseRun(runID)
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if err := s.checkRunID(runID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	record, err := s.store.Load(runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	status, message := s.runStatus(runID)
	detail := runDetail{
		Manifest: record.Manifest, Status: status, Error: message,
		Result: record.Result, Evidence: record.Evidence,
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if err := s.checkRunID(runID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.cancelRun(runID) {
		writeError(w, http.StatusConflict, "run is not active")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": runID, "status": "cancelling"})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if err := s.checkRunID(runID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	events, err := s.store.ReadEvents(runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if err := s.checkRunID(runID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 2000\n\n")
	flusher.Flush()

	channel, unsubscribe := s.hub.Subscribe(runID)
	defer unsubscribe()
	events, err := s.store.ReadEvents(runID)
	if err != nil {
		writeSSE(w, flusher, harness.Event{
			Type: "server_run_finished", RunID: runID, Time: time.Now().UTC(),
			Data: map[string]any{"status": "failed", "error": "run not found"},
		})
		return
	}
	var lastSeq int64
	for _, event := range events {
		if err := writeSSE(w, flusher, event); err != nil {
			return
		}
		if event.Seq > lastSeq {
			lastSeq = event.Seq
		}
	}
	status, message := s.runStatus(runID)
	if status != "running" && status != "starting" {
		_ = writeSSE(w, flusher, harness.Event{
			Type: "server_run_finished", RunID: runID, Time: time.Now().UTC(),
			Data: map[string]any{"status": status, "error": message},
		})
		return
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-channel:
			if !ok {
				status, message := s.runStatus(runID)
				_ = writeSSE(w, flusher, harness.Event{
					Type: "server_run_finished", RunID: runID, Time: time.Now().UTC(),
					Data: map[string]any{"status": status, "error": message},
				})
				return
			}
			if event.Type == "server_run_finished" {
				_ = writeSSE(w, flusher, event)
				return
			}
			if event.Seq <= lastSeq {
				continue
			}
			if err := writeSSE(w, flusher, event); err != nil {
				return
			}
			lastSeq = event.Seq
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
			status, message := s.runStatus(runID)
			if status != "running" && status != "starting" {
				_ = writeSSE(w, flusher, harness.Event{
					Type: "server_run_finished", RunID: runID, Time: time.Now().UTC(),
					Data: map[string]any{"status": status, "error": message},
				})
				return
			}
		}
	}
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, event harness.Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func (s *Server) handleEvidence(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if err := s.checkRunID(runID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snapshot, err := s.store.ReadEvidence(runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "evidence not found")
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	name := r.PathValue("name")
	if err := s.checkRunID(runID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
		writeError(w, http.StatusBadRequest, "invalid artifact name")
		return
	}
	path := filepath.Join(s.store.Dir(runID), "artifacts", name)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		writeError(w, http.StatusNotFound, "artifact not found")
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "inline; filename=\""+name+"\"")
	http.ServeFile(w, r, path)
}

func (s *Server) handleListEvals(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.cfg.EvalDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type evalSummary struct {
		EvalID    string         `json:"eval_id"`
		Suite     string         `json:"suite"`
		CreatedAt time.Time      `json:"created_at"`
		Provider  string         `json:"provider"`
		Model     string         `json:"model"`
		Trials    int            `json:"trials"`
		Overall   eval.Aggregate `json:"overall"`
		Modes     []harness.Mode `json:"modes"`
	}
	items := []evalSummary{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.cfg.EvalDir, entry.Name(), "report.json"))
		if err != nil {
			continue
		}
		var report eval.Report
		if err := json.Unmarshal(data, &report); err != nil {
			continue
		}
		items = append(items, evalSummary{
			EvalID: report.EvalID, Suite: report.Suite, CreatedAt: report.CreatedAt,
			Provider: report.Provider, Model: report.Model, Trials: report.Trials,
			Overall: report.Overall, Modes: report.Modes,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"evals": items})
}

func (s *Server) handleGetEval(w http.ResponseWriter, r *http.Request) {
	evalID := r.PathValue("id")
	if evalID == "" || strings.ContainsAny(evalID, `/\`) || evalID == "." || evalID == ".." {
		writeError(w, http.StatusBadRequest, "invalid evaluation id")
		return
	}
	data, err := os.ReadFile(filepath.Join(s.cfg.EvalDir, evalID, "report.json"))
	if err != nil {
		writeError(w, http.StatusNotFound, "evaluation not found")
		return
	}
	var report eval.Report
	if err := json.Unmarshal(data, &report); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func taskFromRequest(request startRunRequest) (harness.Task, error) {
	if request.Task != nil {
		task := *request.Task
		if task.Domain == "" {
			task.Domain = science.DomainName
		}
		if err := harness.ValidateTask(task); err != nil {
			return harness.Task{}, err
		}
		return task, nil
	}
	if request.TaskID == "" || request.TaskID == science.TaskPrimality {
		return app.DefaultScienceTask(), nil
	}
	return harness.Task{}, fmt.Errorf("unknown task_id %q; supply a task object or use %s", request.TaskID, science.TaskPrimality)
}

func defaultProviderName() string {
	return strings.ToLower(firstNonEmpty(os.Getenv("PARADOX_PROVIDER"), "mock"))
}

func defaultModelName(providerName string) string {
	if value := strings.TrimSpace(os.Getenv("PARADOX_MODEL")); value != "" {
		return value
	}
	if providerName == "mock" {
		return "mock-science-v1"
	}
	return provider.DefaultModel(providerName)
}

func providerKey(name string) string {
	switch name {
	case "meta":
		return firstNonEmpty(os.Getenv("META_API_KEY"), os.Getenv("MODEL_API_KEY"))
	case "openrouter":
		return os.Getenv("OPENROUTER_API_KEY")
	case "openai-compatible":
		return firstNonEmpty(os.Getenv("MODEL_API_KEY"), os.Getenv("OPENAI_API_KEY"))
	default:
		return ""
	}
}

func envFloat(name string) float64 {
	value, _ := strconv.ParseFloat(strings.TrimSpace(os.Getenv(name)), 64)
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
