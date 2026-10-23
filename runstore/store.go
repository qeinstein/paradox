// Package runstore provides durable, human-inspectable local run artifacts.
// Each run is a directory containing a manifest, append-only event stream,
// evidence snapshot, result, and experiment artifacts. No credentials are
// written here.
package runstore

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/qeinstein/paradox/evidence"
	"github.com/qeinstein/paradox/harness"
)

type Store struct {
	root string
}

func New(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		root = ".paradox/runs"
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func (s *Store) Root() string { return s.root }

func (s *Store) Dir(runID string) string { return filepath.Join(s.root, runID) }

func (s *Store) Create(manifest harness.RunManifest) (*Writer, error) {
	if manifest.RunID == "" {
		return nil, errors.New("manifest run id is required")
	}
	dir := s.Dir(manifest.RunID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	if _, err := os.Stat(manifestPath); err == nil {
		return nil, fmt.Errorf("run %s already exists", manifest.RunID)
	}
	if err := writeJSONAtomic(manifestPath, manifest); err != nil {
		return nil, err
	}
	events, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Writer{
		store: s, dir: dir, manifest: manifest, events: events,
	}, nil
}

type Writer struct {
	mu       sync.Mutex
	store    *Store
	dir      string
	manifest harness.RunManifest
	events   *os.File
	closed   bool
}

func (w *Writer) Record(event harness.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.New("run writer is closed")
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := w.events.Write(append(data, '\n')); err != nil {
		return err
	}
	return w.events.Sync()
}

func (w *Writer) SaveResult(result *harness.RunResult) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return writeJSONAtomic(filepath.Join(w.dir, "result.json"), result)
}

func (w *Writer) SaveEvidence(snapshot evidence.Snapshot) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return writeJSONAtomic(filepath.Join(w.dir, "evidence.json"), snapshot)
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return w.events.Close()
}

func (w *Writer) Dir() string { return w.dir }

type RunRecord struct {
	Manifest harness.RunManifest `json:"manifest"`
	Result   *harness.RunResult  `json:"result,omitempty"`
	Evidence *evidence.Snapshot  `json:"evidence,omitempty"`
	Events   []harness.Event     `json:"events,omitempty"`
}

func (s *Store) List() ([]harness.RunManifest, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	manifests := []harness.RunManifest{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := s.readManifest(entry.Name())
		if err != nil {
			continue
		}
		manifests = append(manifests, manifest)
	}
	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].CreatedAt.After(manifests[j].CreatedAt)
	})
	return manifests, nil
}

func (s *Store) readManifest(runID string) (harness.RunManifest, error) {
	var manifest harness.RunManifest
	data, err := os.ReadFile(filepath.Join(s.Dir(runID), "manifest.json"))
	if err != nil {
		return manifest, err
	}
	return manifest, json.Unmarshal(data, &manifest)
}

func (s *Store) Load(runID string) (*RunRecord, error) {
	manifest, err := s.readManifest(runID)
	if err != nil {
		return nil, err
	}
	record := &RunRecord{Manifest: manifest}
	if result, err := s.ReadResult(runID); err == nil {
		record.Result = result
	}
	if snapshot, err := s.ReadEvidence(runID); err == nil {
		record.Evidence = snapshot
	}
	events, err := s.ReadEvents(runID)
	if err == nil {
		record.Events = events
	}
	return record, nil
}

func (s *Store) ReadResult(runID string) (*harness.RunResult, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir(runID), "result.json"))
	if err != nil {
		return nil, err
	}
	var result harness.RunResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Store) ReadEvidence(runID string) (*evidence.Snapshot, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir(runID), "evidence.json"))
	if err != nil {
		return nil, err
	}
	var snapshot evidence.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (s *Store) ReadEvents(runID string) ([]harness.Event, error) {
	file, err := os.Open(filepath.Join(s.Dir(runID), "events.jsonl"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	events := []harness.Event{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event harness.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
