package intake

import (
	"compress/gzip"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/signal-observability/collector/internal/agent"
)

type Config struct {
	ListenAddress string
	Token         string
	TenantID      string
	StoragePath   string
}

type StoredEvent struct {
	TenantID string      `json:"tenant_id"`
	Received time.Time   `json:"received_at"`
	Event    agent.Event `json:"event"`
}

type Store interface {
	Append([]StoredEvent) error
}

type QueryStore interface {
	Store
	List() ([]StoredEvent, error)
}

type JSONLStore struct {
	file *os.File
	path string
	mu   sync.Mutex
}

func NewJSONLStore(path string) (*JSONLStore, error) {
	if path == "" {
		return nil, errors.New("storage path is required")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("open intake storage: %w", err)
	}
	return &JSONLStore{file: file, path: path}, nil
}

func (s *JSONLStore) Append(events []StoredEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	encoder := json.NewEncoder(s.file)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			return fmt.Errorf("write intake event: %w", err)
		}
	}
	return nil
}

func (s *JSONLStore) Close() error {
	return s.file.Close()
}

func (s *JSONLStore) List() ([]StoredEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	contents, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("read intake storage: %w", err)
	}
	var events []StoredEvent
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	for {
		var event StoredEvent
		err := decoder.Decode(&event)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode intake event: %w", err)
		}
		events = append(events, event)
	}
	return events, nil
}

type Service struct {
	config Config
	store  Store
	log    *slog.Logger
}

func New(config Config, store Store, logger *slog.Logger) (*Service, error) {
	if config.Token == "" || config.TenantID == "" {
		return nil, errors.New("intake token and tenant ID are required")
	}
	if store == nil {
		return nil, errors.New("intake store is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{config: config, store: store, log: logger}, nil
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/readyz", s.ready)
	mux.HandleFunc("/v1/intake", s.receive)
	mux.HandleFunc("/v1/summary", s.summary)
	mux.HandleFunc("/v1/telemetry", s.telemetry)
	mux.HandleFunc("/v1/aws/cloudwatch-logs", s.awsCloudWatchLogs)
	mux.HandleFunc("/v1/aws/eventbridge", s.awsEventBridge)
	mux.HandleFunc("/v1/aws/s3", s.awsS3)
	return mux
}

func (s *Service) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Service) ready(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Service) receive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid intake credentials"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 20<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read intake payload"})
		return
	}
	var envelope agent.Envelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid intake envelope"})
		return
	}
	if err := s.validate(envelope); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	received := time.Now().UTC()
	stored := make([]StoredEvent, 0, len(envelope.Events))
	for _, event := range envelope.Events {
		stored = append(stored, StoredEvent{TenantID: envelope.TenantID, Received: received, Event: event})
	}
	if err := s.store.Append(stored); err != nil {
		s.log.Error("intake persistence failed", "error", err, "tenant", envelope.TenantID, "events", len(stored))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "telemetry persistence unavailable"})
		return
	}
	s.log.Info("telemetry persisted", "tenant", envelope.TenantID, "events", len(stored))
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "events": len(stored)})
}

func (s *Service) summary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid intake credentials"})
		return
	}
	store, ok := s.store.(QueryStore)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "query storage is not configured"})
		return
	}
	events, err := store.List()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "telemetry query unavailable"})
		return
	}
	response := map[string]any{"events": len(events), "logs": 0, "metrics": 0, "traces": 0, "last_received": nil}
	for _, event := range events {
		switch event.Event.Type {
		case "logs":
			response["logs"] = response["logs"].(int) + 1
		case "metrics":
			response["metrics"] = response["metrics"].(int) + 1
		case "traces":
			response["traces"] = response["traces"].(int) + 1
		}
		if latest, ok := response["last_received"].(time.Time); !ok || event.Received.After(latest) {
			response["last_received"] = event.Received
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Service) telemetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid intake credentials"})
		return
	}
	store, ok := s.store.(QueryStore)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "query storage is not configured"})
		return
	}
	events, err := store.List()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "telemetry query unavailable"})
		return
	}
	limit := 100
	if requested := r.URL.Query().Get("limit"); requested != "" {
		if _, err := fmt.Sscanf(requested, "%d", &limit); err != nil || limit < 1 || limit > 500 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be between 1 and 500"})
			return
		}
	}
	total := len(events)
	if total > limit {
		events = events[len(events)-limit:]
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "total": total})
}

func (s *Service) awsCloudWatchLogs(w http.ResponseWriter, r *http.Request) {
	s.receiveAWS(w, r, "aws.cloudwatch.logs", decodeCloudWatchLogs)
}
func (s *Service) awsEventBridge(w http.ResponseWriter, r *http.Request) {
	s.receiveAWS(w, r, "aws.eventbridge", identityPayload)
}
func (s *Service) awsS3(w http.ResponseWriter, r *http.Request) {
	s.receiveAWS(w, r, "aws.s3", identityPayload)
}

type awsPayloadDecoder func([]byte) ([]map[string]any, error)

func (s *Service) receiveAWS(w http.ResponseWriter, r *http.Request, source string, decode awsPayloadDecoder) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid intake credentials"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 20<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read AWS payload"})
		return
	}
	items, err := decode(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	stored := make([]StoredEvent, 0, len(items))
	for _, item := range items {
		item["source"] = source
		payload, _ := json.Marshal(item)
		stored = append(stored, StoredEvent{TenantID: s.config.TenantID, Received: time.Now().UTC(), Event: agent.Event{Type: "logs", Timestamp: time.Now().UTC(), Payload: payload}})
	}
	if len(stored) == 0 {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "events": 0})
		return
	}
	if err := s.store.Append(stored); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "telemetry persistence unavailable"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "events": len(stored)})
}

func identityPayload(body []byte) ([]map[string]any, error) {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, fmt.Errorf("invalid AWS JSON payload: %w", err)
	}
	if object, ok := value.(map[string]any); ok {
		if records, ok := object["Records"].([]any); ok {
			items := make([]map[string]any, 0, len(records))
			for _, record := range records {
				items = append(items, map[string]any{"record": record})
			}
			return items, nil
		}
	}
	return []map[string]any{{"payload": value}}, nil
}

func decodeCloudWatchLogs(body []byte) ([]map[string]any, error) {
	decoded := body
	var wrapper struct {
		AWSLogs struct {
			Data string `json:"data"`
		} `json:"awslogs"`
	}
	if err := json.Unmarshal(body, &wrapper); err == nil && wrapper.AWSLogs.Data != "" {
		decoded, _ = base64.StdEncoding.DecodeString(wrapper.AWSLogs.Data)
	} else if value, err := base64.StdEncoding.DecodeString(string(body)); err == nil {
		decoded = value
	}
	if reader, err := gzip.NewReader(strings.NewReader(string(decoded))); err == nil {
		defer reader.Close()
		decoded, err = io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
	}
	var envelope struct {
		LogEvents []struct {
			ID        string `json:"id"`
			Timestamp int64  `json:"timestamp"`
			Message   string `json:"message"`
		} `json:"logEvents"`
	}
	if err := json.Unmarshal(decoded, &envelope); err != nil {
		return nil, fmt.Errorf("invalid CloudWatch Logs payload: %w", err)
	}
	items := make([]map[string]any, 0, len(envelope.LogEvents))
	for _, item := range envelope.LogEvents {
		items = append(items, map[string]any{"event_id": item.ID, "timestamp": item.Timestamp, "message": item.Message})
	}
	return items, nil
}

func (s *Service) validate(envelope agent.Envelope) error {
	if envelope.TenantID == "" || envelope.TenantID != s.config.TenantID {
		return errors.New("invalid tenant ID")
	}
	if len(envelope.Events) == 0 {
		return errors.New("intake envelope contains no events")
	}
	if len(envelope.Events) > 10_000 {
		return errors.New("intake envelope contains too many events")
	}
	for _, event := range envelope.Events {
		if event.Type != "logs" && event.Type != "metrics" && event.Type != "traces" {
			return fmt.Errorf("unsupported event type %q", event.Type)
		}
		if len(event.Payload) == 0 || !json.Valid(event.Payload) {
			return fmt.Errorf("event %q contains invalid payload", event.Type)
		}
	}
	return nil
}

func (s *Service) authorized(r *http.Request) bool {
	provided := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if provided == "" {
		provided = r.Header.Get("X-Signal-Ingest-Token")
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(s.config.Token)) == 1
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
