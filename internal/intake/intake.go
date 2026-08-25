package intake

import (
	"bytes"
	"compress/gzip"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/signal-observability/collector/internal/telemetry"
)

// Tenant is one isolated customer of the platform.
type Tenant struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

type Config struct {
	ListenAddress string
	StoragePath   string
	// Tenants is the registry of accepted credentials. A request's tenant is
	// resolved from the token it presents, never from anything in the payload.
	Tenants []Tenant
}

type Service struct {
	config Config
	store  Store
	log    *slog.Logger
}

func New(config Config, store Store, logger *slog.Logger) (*Service, error) {
	if len(config.Tenants) == 0 {
		return nil, errors.New("at least one tenant must be configured")
	}
	seen := map[string]struct{}{}
	for _, tenant := range config.Tenants {
		if tenant.ID == "" || tenant.Token == "" {
			return nil, errors.New("every tenant requires an ID and a token")
		}
		if _, duplicate := seen[tenant.Token]; duplicate {
			return nil, errors.New("tenants must not share an ingest token")
		}
		seen[tenant.Token] = struct{}{}
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
	tenant, ok := s.authenticate(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid intake credentials"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 20<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read intake payload"})
		return
	}
	var envelope telemetry.Envelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid intake envelope"})
		return
	}
	if err := s.validate(envelope, tenant); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	received := time.Now().UTC()
	stored := make([]StoredEvent, 0, len(envelope.Events))
	for _, event := range envelope.Events {
		// Attribute to the authenticated tenant, not to the claim in the body.
		stored = append(stored, StoredEvent{TenantID: tenant.ID, Received: received, Event: event})
	}
	if err := s.store.Append(stored); err != nil {
		s.log.Error("intake persistence failed", "error", err, "tenant", tenant.ID, "events", len(stored))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "telemetry persistence unavailable"})
		return
	}
	s.log.Info("telemetry persisted", "tenant", tenant.ID, "events", len(stored))
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "events": len(stored)})
}

func (s *Service) summary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	tenant, ok := s.authenticate(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid intake credentials"})
		return
	}
	store, ok := s.store.(QueryStore)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "query storage is not configured"})
		return
	}
	// The tenant comes from the credential, so a caller cannot widen the query
	// to another tenant's data.
	summary, err := store.Summary(tenant.ID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "telemetry query unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Service) telemetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	tenant, ok := s.authenticate(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid intake credentials"})
		return
	}
	limit := 100
	if requested := r.URL.Query().Get("limit"); requested != "" {
		parsed, err := strconv.Atoi(requested)
		if err != nil || parsed < 1 || parsed > 500 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be between 1 and 500"})
			return
		}
		limit = parsed
	}
	store, ok := s.store.(QueryStore)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "query storage is not configured"})
		return
	}
	events, err := store.Recent(tenant.ID, limit)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "telemetry query unavailable"})
		return
	}
	summary, err := store.Summary(tenant.ID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "telemetry query unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "total": summary.Events})
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
	tenant, ok := s.authenticate(r)
	if !ok {
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
		stored = append(stored, StoredEvent{TenantID: tenant.ID, Received: time.Now().UTC(), Event: telemetry.Event{Type: "logs", Timestamp: time.Now().UTC(), Payload: payload}})
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
	if reader, err := gzip.NewReader(bytes.NewReader(decoded)); err == nil {
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

func (s *Service) validate(envelope telemetry.Envelope, tenant Tenant) error {
	// An envelope may omit the tenant, but if it names one it must be the
	// tenant that authenticated. Anything else is an attempt to write into
	// another customer's data.
	if envelope.TenantID != "" && envelope.TenantID != tenant.ID {
		return errors.New("envelope tenant does not match the authenticated tenant")
	}
	if len(envelope.Events) == 0 {
		return errors.New("intake envelope contains no events")
	}
	if len(envelope.Events) > 10_000 {
		return errors.New("intake envelope contains too many events")
	}
	for _, event := range envelope.Events {
		if !telemetry.ValidSignal(event.Type) {
			return fmt.Errorf("unsupported event type %q", event.Type)
		}
		if len(event.Payload) == 0 || !json.Valid(event.Payload) {
			return fmt.Errorf("event %q contains invalid payload", event.Type)
		}
	}
	return nil
}

// authenticate resolves the caller's tenant from the presented token. Every
// candidate is compared even after a match so the work does not depend on which
// tenant is calling.
func (s *Service) authenticate(r *http.Request) (Tenant, bool) {
	provided := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if provided == "" {
		provided = r.Header.Get("X-Signal-Ingest-Token")
	}
	if provided == "" {
		return Tenant{}, false
	}
	var matched Tenant
	found := false
	for _, tenant := range s.config.Tenants {
		if subtle.ConstantTimeCompare([]byte(provided), []byte(tenant.Token)) == 1 {
			matched = tenant
			found = true
		}
	}
	return matched, found
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
