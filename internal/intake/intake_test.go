package intake

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/signal-observability/collector/internal/agent"
)

type memoryStore struct {
	events []StoredEvent
}

func (store *memoryStore) Append(events []StoredEvent) error {
	store.events = append(store.events, events...)
	return nil
}

func (store *memoryStore) List() ([]StoredEvent, error) {
	return append([]StoredEvent(nil), store.events...), nil
}

func TestServiceRequiresTenantAndToken(t *testing.T) {
	_, err := New(Config{}, &memoryStore{}, slog.Default())
	if err == nil {
		t.Fatal("expected missing tenant configuration to fail")
	}
}

func TestServiceValidatesAndPersistsEnvelope(t *testing.T) {
	store := &memoryStore{}
	service, err := New(Config{TenantID: "tenant-a", Token: "secret"}, store, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.Handler())
	defer server.Close()

	envelope := agent.Envelope{
		TenantID: "tenant-a",
		Events:   []agent.Event{{Type: "logs", Timestamp: time.Now().UTC(), Payload: json.RawMessage(`{"message":"hello"}`)}},
	}
	body, _ := json.Marshal(envelope)
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/intake", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	response, requestErr := http.DefaultClient.Do(req)
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted || len(store.events) != 1 {
		t.Fatalf("expected accepted event, status=%d events=%d", response.StatusCode, len(store.events))
	}
}

func TestServiceRejectsWrongTenantAndCredentials(t *testing.T) {
	service, err := New(Config{TenantID: "tenant-a", Token: "secret"}, &memoryStore{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	body := `{"tenant_id":"tenant-b","events":[{"type":"logs","payload":{"message":"nope"}}]}`
	for _, token := range []string{"wrong", "secret"} {
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/intake", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", "Bearer "+token)
		response, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		response.Body.Close()
		expected := http.StatusUnauthorized
		if token == "secret" {
			expected = http.StatusBadRequest
		}
		if response.StatusCode != expected {
			t.Fatalf("token %q: expected %d, got %d", token, expected, response.StatusCode)
		}
	}
}

func TestServiceQueriesSummaryAndRecentTelemetry(t *testing.T) {
	store := &memoryStore{events: []StoredEvent{
		{TenantID: "tenant-a", Received: time.Now().UTC().Add(-time.Minute), Event: agent.Event{Type: "logs", Payload: json.RawMessage(`{"message":"first"}`)}},
		{TenantID: "tenant-a", Received: time.Now().UTC(), Event: agent.Event{Type: "traces", Payload: json.RawMessage(`{"resourceSpans":[]}`)}},
	}}
	service, err := New(Config{TenantID: "tenant-a", Token: "secret"}, store, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	for _, endpoint := range []string{"/v1/summary", "/v1/telemetry?limit=1"} {
		req, _ := http.NewRequest(http.MethodGet, server.URL+endpoint, nil)
		req.Header.Set("Authorization", "Bearer secret")
		response, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			t.Fatalf("query %s returned %d", endpoint, response.StatusCode)
		}
		var payload map[string]any
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		response.Body.Close()
		if endpoint == "/v1/summary" && payload["events"] != float64(2) {
			t.Fatalf("unexpected summary: %v", payload)
		}
		if endpoint != "/v1/summary" && payload["total"] != float64(2) {
			t.Fatalf("unexpected telemetry total: %v", payload)
		}
	}
}
