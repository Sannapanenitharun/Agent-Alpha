package intake

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/signal-observability/collector/internal/telemetry"
)

// recentCapacity is how many recent events are kept in memory per tenant to
// answer dashboard queries. Older events remain durable on disk; they are just
// not served by the recent-events endpoint.
const recentCapacity = 500

type StoredEvent struct {
	TenantID string          `json:"tenant_id"`
	Received time.Time       `json:"received_at"`
	Event    telemetry.Event `json:"event"`
}

// Summary is the per-tenant rollup backing the dashboard's counters.
type Summary struct {
	Events       int        `json:"events"`
	Logs         int        `json:"logs"`
	Metrics      int        `json:"metrics"`
	Traces       int        `json:"traces"`
	LastReceived *time.Time `json:"last_received"`
}

type Store interface {
	Append([]StoredEvent) error
}

// QueryStore serves reads. Every method takes a tenant ID and must apply it as
// a filter: callers pass the tenant resolved from the credential, and the store
// is the last place that separation can be enforced.
type QueryStore interface {
	Store
	Summary(tenantID string) (Summary, error)
	Recent(tenantID string, limit int) ([]StoredEvent, error)
}

// ring is a fixed-size buffer of the most recent events for one tenant.
type ring struct {
	items []StoredEvent
	next  int
	full  bool
}

func (r *ring) add(event StoredEvent) {
	if len(r.items) < recentCapacity {
		r.items = append(r.items, event)
		return
	}
	r.items[r.next] = event
	r.next = (r.next + 1) % recentCapacity
	r.full = true
}

// list returns up to limit events, oldest first.
func (r *ring) list(limit int) []StoredEvent {
	ordered := make([]StoredEvent, 0, len(r.items))
	if r.full {
		ordered = append(ordered, r.items[r.next:]...)
		ordered = append(ordered, r.items[:r.next]...)
	} else {
		ordered = append(ordered, r.items...)
	}
	if len(ordered) > limit {
		ordered = ordered[len(ordered)-limit:]
	}
	return ordered
}

type tenantIndex struct {
	summary Summary
	recent  ring
}

// JSONLStore appends telemetry to a JSON Lines file and keeps a per-tenant
// index in memory.
//
// Queries are served from that index. Reading the whole file on every request
// made dashboard polling cost grow with total retained volume, and it held the
// write lock while doing so, stalling ingestion.
type JSONLStore struct {
	file    *os.File
	path    string
	mu      sync.Mutex
	tenants map[string]*tenantIndex
}

func NewJSONLStore(path string) (*JSONLStore, error) {
	if path == "" {
		return nil, errors.New("storage path is required")
	}
	store := &JSONLStore{path: path, tenants: map[string]*tenantIndex{}}
	// Rebuild the index once at startup so a restart does not lose the
	// counters or the recent-events view.
	if err := store.rebuild(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("open intake storage: %w", err)
	}
	store.file = file
	return store, nil
}

func (s *JSONLStore) rebuild() error {
	file, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read intake storage: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 32<<20)
	line := 0
	for scanner.Scan() {
		line++
		var event StoredEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			// A partially written trailing line should not stop the service
			// from starting; skip it and keep the rest of the history.
			continue
		}
		s.index(event)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("scan intake storage at line %d: %w", line, err)
	}
	return nil
}

// index updates the in-memory view. The caller holds the lock, except during
// rebuild where the store is not yet shared.
func (s *JSONLStore) index(event StoredEvent) {
	entry, ok := s.tenants[event.TenantID]
	if !ok {
		entry = &tenantIndex{}
		s.tenants[event.TenantID] = entry
	}
	entry.summary.Events++
	switch event.Event.Type {
	case "logs":
		entry.summary.Logs++
	case "metrics":
		entry.summary.Metrics++
	case "traces":
		entry.summary.Traces++
	}
	if entry.summary.LastReceived == nil || event.Received.After(*entry.summary.LastReceived) {
		received := event.Received
		entry.summary.LastReceived = &received
	}
	entry.recent.add(event)
}

func (s *JSONLStore) Append(events []StoredEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	writer := bufio.NewWriter(s.file)
	encoder := json.NewEncoder(writer)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			return fmt.Errorf("write intake event: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush intake storage: %w", err)
	}
	// Index only after the durable write succeeds, so the in-memory view never
	// reports events that were not persisted.
	for _, event := range events {
		s.index(event)
	}
	return nil
}

func (s *JSONLStore) Summary(tenantID string) (Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.tenants[tenantID]
	if !ok {
		return Summary{}, nil
	}
	return entry.summary, nil
}

func (s *JSONLStore) Recent(tenantID string, limit int) ([]StoredEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.tenants[tenantID]
	if !ok {
		return []StoredEvent{}, nil
	}
	return entry.recent.list(limit), nil
}

func (s *JSONLStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file.Close()
}
