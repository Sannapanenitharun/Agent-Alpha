package intake

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/signal-observability/collector/internal/telemetry"
)

type memoryStore struct {
	events []StoredEvent
}

func (store *memoryStore) Append(events []StoredEvent) error {
	store.events = append(store.events, events...)
	return nil
}

// Summary and Recent filter by tenant, mirroring the production store so the
// isolation behaviour is exercised rather than assumed.
func (store *memoryStore) Summary(tenantID string) (Summary, error) {
	summary := Summary{}
	for _, event := range store.events {
		if event.TenantID != tenantID {
			continue
		}
		summary.Events++
		switch event.Event.Type {
		case "logs":
			summary.Logs++
		case "metrics":
			summary.Metrics++
		case "traces":
			summary.Traces++
		}
		if summary.LastReceived == nil || event.Received.After(*summary.LastReceived) {
			received := event.Received
			summary.LastReceived = &received
		}
	}
	return summary, nil
}

func (store *memoryStore) Recent(tenantID string, limit int) ([]StoredEvent, error) {
	matching := make([]StoredEvent, 0, len(store.events))
	for _, event := range store.events {
		if event.TenantID == tenantID {
			matching = append(matching, event)
		}
	}
	if len(matching) > limit {
		matching = matching[len(matching)-limit:]
	}
	return matching, nil
}

func TestServiceRequiresTenantAndToken(t *testing.T) {
	_, err := New(Config{}, &memoryStore{}, slog.Default())
	if err == nil {
		t.Fatal("expected missing tenant configuration to fail")
	}
}

func TestServiceValidatesAndPersistsEnvelope(t *testing.T) {
	store := &memoryStore{}
	service, err := New(Config{Tenants: []Tenant{{ID: "tenant-a", Token: "secret"}}}, store, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.Handler())
	defer server.Close()

	envelope := telemetry.Envelope{
		TenantID: "tenant-a",
		Events:   []telemetry.Event{{Type: "logs", Timestamp: time.Now().UTC(), Payload: json.RawMessage(`{"message":"hello"}`)}},
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
	service, err := New(Config{Tenants: []Tenant{{ID: "tenant-a", Token: "secret"}}}, &memoryStore{}, slog.Default())
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
		{TenantID: "tenant-a", Received: time.Now().UTC().Add(-time.Minute), Event: telemetry.Event{Type: "logs", Payload: json.RawMessage(`{"message":"first"}`)}},
		{TenantID: "tenant-a", Received: time.Now().UTC(), Event: telemetry.Event{Type: "traces", Payload: json.RawMessage(`{"resourceSpans":[]}`)}},
	}}
	service, err := New(Config{Tenants: []Tenant{{ID: "tenant-a", Token: "secret"}}}, store, slog.Default())
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

func TestAWSIngestionRoutes(t *testing.T) {
	store := &memoryStore{}
	service, err := New(Config{Tenants: []Tenant{{ID: "tenant-a", Token: "secret"}}}, store, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.Handler())
	defer server.Close()

	logsJSON := []byte(`{"logEvents":[{"id":"log-1","timestamp":123,"message":"hello"}]}`)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(logsJSON); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(compressed.Bytes())
	for _, request := range []struct{ path, contentType, body string }{
		{"/v1/aws/cloudwatch-logs", "application/json", `{"awslogs":{"data":"` + encoded + `"}}`},
		{"/v1/aws/eventbridge", "application/json", `{"detail-type":"EC2 State Change"}`},
		{"/v1/aws/s3", "application/json", `{"Records":[{"eventName":"ObjectCreated:Put"}]}`},
	} {
		req, _ := http.NewRequest(http.MethodPost, server.URL+request.path, strings.NewReader(request.body))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("Content-Type", request.contentType)
		response, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusAccepted {
			t.Fatalf("%s returned %d", request.path, response.StatusCode)
		}
	}
	if len(store.events) != 3 {
		t.Fatalf("expected 3 AWS events, got %d", len(store.events))
	}
}

// Two tenants sharing one intake must never see each other's telemetry, and a
// forged tenant claim in the body must not override the credential.
func TestTenantsAreIsolated(t *testing.T) {
	store := &memoryStore{}
	service, err := New(Config{Tenants: []Tenant{
		{ID: "tenant-a", Token: "token-a"},
		{ID: "tenant-b", Token: "token-b"},
	}}, store, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.Handler())
	defer server.Close()

	send := func(token, envelopeTenant string) int {
		envelope := telemetry.Envelope{TenantID: envelopeTenant, Events: []telemetry.Event{{
			Type: "logs", Timestamp: time.Now().UTC(), Payload: json.RawMessage(`{"message":"hello"}`),
		}}}
		body, marshalErr := json.Marshal(envelope)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/intake", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		response, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		response.Body.Close()
		return response.StatusCode
	}

	if status := send("token-a", "tenant-a"); status != http.StatusAccepted {
		t.Fatalf("tenant-a write rejected: %d", status)
	}
	// Claiming another tenant with your own credential must be refused.
	if status := send("token-a", "tenant-b"); status != http.StatusBadRequest {
		t.Fatalf("cross-tenant write should be rejected, got %d", status)
	}
	// An omitted tenant is attributed to the authenticated one.
	if status := send("token-b", ""); status != http.StatusAccepted {
		t.Fatalf("tenant-b write rejected: %d", status)
	}

	summaryFor := func(token string) map[string]any {
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/summary", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		defer response.Body.Close()
		var payload map[string]any
		if decodeErr := json.NewDecoder(response.Body).Decode(&payload); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		return payload
	}

	if events := summaryFor("token-a")["events"]; events != float64(1) {
		t.Fatalf("tenant-a should see exactly its own event, saw %v", events)
	}
	if events := summaryFor("token-b")["events"]; events != float64(1) {
		t.Fatalf("tenant-b should see exactly its own event, saw %v", events)
	}

	for _, event := range store.events {
		if event.TenantID != "tenant-a" && event.TenantID != "tenant-b" {
			t.Fatalf("event stored under unexpected tenant %q", event.TenantID)
		}
	}
}

func TestConfigRejectsSharedTokens(t *testing.T) {
	_, err := New(Config{Tenants: []Tenant{
		{ID: "tenant-a", Token: "same"},
		{ID: "tenant-b", Token: "same"},
	}}, &memoryStore{}, slog.Default())
	if err == nil {
		t.Fatal("two tenants sharing a token must be rejected: the token cannot identify a tenant")
	}
}
