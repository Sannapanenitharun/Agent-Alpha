package intake

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/signal-observability/collector/internal/telemetry"
)

func storedEvent(tenant, kind string, at time.Time) StoredEvent {
	return StoredEvent{
		TenantID: tenant,
		Received: at,
		Event:    telemetry.Event{Type: kind, Timestamp: at, Payload: json.RawMessage(`{"ok":true}`)},
	}
}

func TestStoreKeepsTenantsSeparate(t *testing.T) {
	store, err := NewJSONLStore(filepath.Join(t.TempDir(), "telemetry.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Now().UTC()
	if err = store.Append([]StoredEvent{
		storedEvent("tenant-a", "logs", now),
		storedEvent("tenant-a", "metrics", now),
		storedEvent("tenant-b", "traces", now),
	}); err != nil {
		t.Fatal(err)
	}

	summaryA, err := store.Summary("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if summaryA.Events != 2 || summaryA.Logs != 1 || summaryA.Metrics != 1 || summaryA.Traces != 0 {
		t.Fatalf("tenant-a summary leaked or miscounted: %+v", summaryA)
	}
	summaryB, err := store.Summary("tenant-b")
	if err != nil {
		t.Fatal(err)
	}
	if summaryB.Events != 1 || summaryB.Traces != 1 {
		t.Fatalf("tenant-b summary wrong: %+v", summaryB)
	}

	recentB, err := store.Recent("tenant-b", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range recentB {
		if event.TenantID != "tenant-b" {
			t.Fatalf("tenant-b query returned %s data", event.TenantID)
		}
	}
	unknown, err := store.Summary("tenant-does-not-exist")
	if err != nil || unknown.Events != 0 {
		t.Fatalf("unknown tenant should see nothing: %+v %v", unknown, err)
	}
}

func TestStoreRebuildsIndexOnRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	store, err := NewJSONLStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = store.Append([]StoredEvent{storedEvent("tenant-a", "logs", now), storedEvent("tenant-a", "traces", now)}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	reopened, err := NewJSONLStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	summary, err := reopened.Summary("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Events != 2 || summary.Logs != 1 || summary.Traces != 1 {
		t.Fatalf("index was not rebuilt from disk: %+v", summary)
	}
}

// A crash can leave a truncated final line; the service must still start.
func TestStoreToleratesTruncatedTrailingLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	good, err := json.Marshal(storedEvent("tenant-a", "logs", time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(append(good, '\n'), []byte(`{"tenant_id":"tenant-a","rec`)...), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewJSONLStore(path)
	if err != nil {
		t.Fatalf("store refused to open after a partial write: %v", err)
	}
	defer store.Close()
	summary, err := store.Summary("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Events != 1 {
		t.Fatalf("expected the intact event to survive, got %+v", summary)
	}
}

func TestRecentIsBoundedAndOrdered(t *testing.T) {
	store, err := NewJSONLStore(filepath.Join(t.TempDir(), "telemetry.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	base := time.Now().UTC()
	batch := make([]StoredEvent, 0, recentCapacity+50)
	for i := 0; i < recentCapacity+50; i++ {
		batch = append(batch, storedEvent("tenant-a", "logs", base.Add(time.Duration(i)*time.Second)))
	}
	if err = store.Append(batch); err != nil {
		t.Fatal(err)
	}

	recent, err := store.Recent("tenant-a", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != recentCapacity {
		t.Fatalf("recent buffer should cap at %d, got %d", recentCapacity, len(recent))
	}
	for i := 1; i < len(recent); i++ {
		if recent[i].Received.Before(recent[i-1].Received) {
			t.Fatal("recent events are not in chronological order")
		}
	}
	// The newest event must be present and the oldest evicted.
	if !recent[len(recent)-1].Received.Equal(base.Add(time.Duration(recentCapacity+49) * time.Second)) {
		t.Fatal("newest event missing from the recent buffer")
	}
	summary, err := store.Summary("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Events != recentCapacity+50 {
		t.Fatalf("counters must track every event, not just buffered ones: %+v", summary)
	}
}
