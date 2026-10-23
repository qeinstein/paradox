package server

import (
	"sync"

	"github.com/qeinstein/paradox/harness"
)

// Hub fans runtime events out to live subscribers. It is deliberately
// non-blocking: a slow browser must never stall an experiment. Clients that
// fall behind can reload the persisted event stream.
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan harness.Event]struct{}
}

func newHub() *Hub {
	return &Hub{subs: map[string]map[chan harness.Event]struct{}{}}
}

func (h *Hub) Subscribe(runID string) (<-chan harness.Event, func()) {
	channel := make(chan harness.Event, 128)
	h.mu.Lock()
	if h.subs[runID] == nil {
		h.subs[runID] = map[chan harness.Event]struct{}{}
	}
	h.subs[runID][channel] = struct{}{}
	h.mu.Unlock()
	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if subscribers, ok := h.subs[runID]; ok {
			delete(subscribers, channel)
			if len(subscribers) == 0 {
				delete(h.subs, runID)
			}
		}
	}
	return channel, cancel
}

func (h *Hub) Publish(runID string, event harness.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for channel := range h.subs[runID] {
		select {
		case channel <- event:
		default:
			// The event remains in the durable JSONL stream.
		}
	}
}

func (h *Hub) CloseRun(runID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for channel := range h.subs[runID] {
		close(channel)
	}
	delete(h.subs, runID)
}
