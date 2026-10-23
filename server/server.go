// Package server exposes persisted runs, the live event stream, and the
// evidence graph over a small local HTTP API. The dashboard is static and is
// served from the embedded web package; no external services are required.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/qeinstein/paradox/app"
	"github.com/qeinstein/paradox/harness"
	"github.com/qeinstein/paradox/runstore"
	"github.com/qeinstein/paradox/web"
)

type Config struct {
	RunsDir         string
	EvalDir         string
	APIKey          string
	Version         string
	ProviderFactory func(app.ProviderConfig) (harness.ModelProvider, error)
}

type Server struct {
	cfg     Config
	store   *runstore.Store
	hub     *Hub
	handler http.Handler

	mu   sync.Mutex
	runs map[string]*runState
	wg   sync.WaitGroup
}

type runState struct {
	id       string
	status   string
	err      string
	cancel   context.CancelFunc
	started  time.Time
	finished time.Time
}

func New(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.RunsDir) == "" {
		cfg.RunsDir = ".paradox/runs"
	}
	if strings.TrimSpace(cfg.EvalDir) == "" {
		cfg.EvalDir = ".paradox/evals"
	}
	if cfg.ProviderFactory == nil {
		cfg.ProviderFactory = app.BuildProvider
	}
	store, err := runstore.New(cfg.RunsDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.EvalDir, 0o755); err != nil {
		return nil, err
	}
	server := &Server{
		cfg: cfg, store: store, hub: newHub(),
		runs: map[string]*runState{},
	}
	server.handler = server.routes()
	return server, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	for _, state := range s.runs {
		if state.cancel != nil {
			state.cancel()
		}
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/models", s.handleModels)
	mux.HandleFunc("GET /api/runs", s.handleListRuns)
	mux.HandleFunc("POST /api/runs", s.handleStartRun)
	mux.HandleFunc("GET /api/runs/{id}", s.handleGetRun)
	mux.HandleFunc("POST /api/runs/{id}/cancel", s.handleCancelRun)
	mux.HandleFunc("GET /api/runs/{id}/events", s.handleEvents)
	mux.HandleFunc("GET /api/runs/{id}/events/stream", s.handleEventStream)
	mux.HandleFunc("GET /api/runs/{id}/evidence", s.handleEvidence)
	mux.HandleFunc("GET /api/runs/{id}/artifacts/{name}", s.handleArtifact)
	mux.HandleFunc("GET /api/evals", s.handleListEvals)
	mux.HandleFunc("GET /api/evals/{id}", s.handleGetEval)
	mux.Handle("/", http.FileServer(http.FS(web.FS)))
	return s.securityHeaders(s.auth(s.recover(mux)))
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) auth(next http.Handler) http.Handler {
	if strings.TrimSpace(s.cfg.APIKey) == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		provided := strings.TrimSpace(r.Header.Get("X-API-Key"))
		if provided == "" {
			provided = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		if provided == "" {
			provided = r.URL.Query().Get("api_key")
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(s.cfg.APIKey)) != 1 {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("paradox server panic on %s %s: %v", r.Method, r.URL.Path, recovered)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func (s *Server) runStatus(runID string) (string, string) {
	s.mu.Lock()
	state, ok := s.runs[runID]
	s.mu.Unlock()
	if ok {
		return state.status, state.err
	}
	result, err := s.store.ReadResult(runID)
	if err == nil {
		if result.Error != "" {
			return "failed", result.Error
		}
		return "completed", ""
	}
	if _, err := os.Stat(filepath.Join(s.store.Dir(runID), "manifest.json")); err == nil {
		return "interrupted", ""
	}
	return "unknown", ""
}

func (s *Server) setRunState(runID, status, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.runs[runID]
	if !ok {
		state = &runState{id: runID}
		s.runs[runID] = state
	}
	state.status = status
	state.err = message
	if status != "running" {
		state.finished = time.Now().UTC()
	}
}

func (s *Server) cancelRun(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.runs[runID]
	if !ok || state.status != "running" || state.cancel == nil {
		return false
	}
	state.cancel()
	return true
}

func (s *Server) checkRunID(runID string) error {
	if runID == "" || strings.ContainsAny(runID, `/\`) || runID == "." || runID == ".." {
		return fmt.Errorf("invalid run id")
	}
	return nil
}
