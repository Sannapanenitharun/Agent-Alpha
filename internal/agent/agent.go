package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	logsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	_ "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type Config struct {
	ListenAddress     string
	GRPCListenAddress string
	IntakeURL         string
	TenantID          string
	Token             string
	BatchSize         int
	FlushInterval     time.Duration
	QueueSize         int
}

type Event struct {
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type Envelope struct {
	TenantID string  `json:"tenant_id"`
	Events   []Event `json:"events"`
}

type Collector struct {
	config    Config
	log       *slog.Logger
	queue     chan Event
	client    *http.Client
	dropped   atomic.Uint64
	delivered atomic.Uint64
	cancel    context.CancelFunc
	done      chan struct{}
}

// errPermanent marks a batch that can never be delivered no matter how often it
// is retried. Retrying such a batch forever blocks every later event behind it,
// so the pipeline drops it instead of stalling.
var errPermanent = errors.New("permanently undeliverable")

func New(config Config, logger *slog.Logger) (*Collector, error) {
	if config.TenantID == "" || config.Token == "" {
		return nil, errors.New("tenant ID and ingest token are required")
	}
	if config.IntakeURL == "" {
		return nil, errors.New("intake URL is required")
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 100
	}
	if config.FlushInterval <= 0 {
		config.FlushInterval = 2 * time.Second
	}
	if config.QueueSize <= 0 {
		config.QueueSize = 10_000
	}
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	collector := &Collector{
		config: config,
		log:    logger,
		queue:  make(chan Event, config.QueueSize),
		client: &http.Client{Timeout: 10 * time.Second},
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go collector.run(ctx)
	return collector, nil
}

func (c *Collector) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", c.health)
	mux.HandleFunc("/readyz", c.ready)
	mux.HandleFunc("/v1/stats", c.stats)
	mux.HandleFunc("/v1/logs", func(w http.ResponseWriter, r *http.Request) { c.receive(w, r, "logs") })
	mux.HandleFunc("/v1/metrics", func(w http.ResponseWriter, r *http.Request) { c.receive(w, r, "metrics") })
	mux.HandleFunc("/v1/traces", func(w http.ResponseWriter, r *http.Request) { c.receive(w, r, "traces") })
	return mux
}

func (c *Collector) GRPCServer() *grpc.Server {
	server := grpc.NewServer()
	logsv1.RegisterLogsServiceServer(server, logsService{collector: c})
	metricsv1.RegisterMetricsServiceServer(server, metricsService{collector: c})
	tracev1.RegisterTraceServiceServer(server, traceService{collector: c})
	return server
}

func (c *Collector) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
func (c *Collector) ready(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// stats reports the collector's own pipeline health. An observability agent
// that cannot be observed is a blind spot during exactly the incidents it
// exists to explain.
func (c *Collector) stats(w http.ResponseWriter, r *http.Request) {
	if !c.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid ingest credentials"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"queued":         len(c.queue),
		"queue_capacity": cap(c.queue),
		"delivered":      c.delivered.Load(),
		"dropped":        c.dropped.Load(),
	})
}

func (c *Collector) receive(w http.ResponseWriter, r *http.Request, eventType string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !c.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid ingest credentials"})
		return
	}
	body, err := readRequestBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(bytes.TrimSpace(body)) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payload is empty"})
		return
	}
	payload, err := decodePayload(eventType, body, r.Header.Get("Content-Type"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := c.enqueue(eventType, payload); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

// maxPayloadBytes bounds both the compressed request body and the decompressed
// result so a small gzip payload cannot expand into an unbounded allocation.
const maxPayloadBytes = 10 << 20

// readRequestBody reads an OTLP request body, transparently decompressing it
// when the client sent Content-Encoding: gzip. Standard OTLP exporters enable
// gzip by default, so this path is the common one in production.
func readRequestBody(r *http.Request) ([]byte, error) {
	var reader io.Reader = io.LimitReader(r.Body, maxPayloadBytes)
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") {
		decompressor, err := gzip.NewReader(reader)
		if err != nil {
			return nil, errors.New("could not read gzip payload")
		}
		defer decompressor.Close()
		reader = io.LimitReader(decompressor, maxPayloadBytes)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, errors.New("could not read payload")
	}
	return body, nil
}

func (c *Collector) enqueue(eventType string, payload []byte) error {
	event := Event{Type: eventType, Timestamp: time.Now().UTC(), Payload: json.RawMessage(payload)}
	select {
	case c.queue <- event:
		return nil
	default:
		c.dropped.Add(1)
		return errors.New("collector queue is full")
	}
}

func (c *Collector) authorizedContext(ctx context.Context) bool {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	provided := ""
	if len(values) > 0 {
		provided = strings.TrimSpace(strings.TrimPrefix(values[0], "Bearer "))
	}
	if provided == "" {
		values = metadata.ValueFromIncomingContext(ctx, "x-signal-ingest-token")
		if len(values) > 0 {
			provided = values[0]
		}
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(c.config.Token)) == 1
}

// mediaType strips parameters such as "; charset=utf-8" and matches the type
// exactly. Substring matching would treat "application/json-lines" as OTLP JSON.
func mediaType(contentType string) string {
	parsed, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(contentType))
	}
	return parsed
}

func decodePayload(eventType string, body []byte, contentType string) ([]byte, error) {
	switch mediaType(contentType) {
	case "application/x-protobuf", "application/json":
	default:
		// Legacy passthrough for untyped JSON. The payload still has to be
		// valid JSON: an unparseable payload would otherwise be accepted here
		// and then fail to encode at delivery time, stalling the whole batch.
		if !json.Valid(body) {
			return nil, errors.New("payload must be OTLP or valid JSON")
		}
		return body, nil
	}
	var message proto.Message
	switch eventType {
	case "logs":
		message = &logsv1.ExportLogsServiceRequest{}
	case "metrics":
		message = &metricsv1.ExportMetricsServiceRequest{}
	case "traces":
		message = &tracev1.ExportTraceServiceRequest{}
	default:
		return nil, fmt.Errorf("unsupported signal type %q", eventType)
	}
	if mediaType(contentType) == "application/json" {
		if err := protojson.Unmarshal(body, message); err != nil {
			return nil, fmt.Errorf("invalid OTLP JSON payload: %w", err)
		}
	} else if err := proto.Unmarshal(body, message); err != nil {
		return nil, fmt.Errorf("invalid OTLP protobuf payload: %w", err)
	}
	return normalizeMessage(message)
}

func normalizeMessage(message proto.Message) ([]byte, error) {
	normalized, err := protojson.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("normalize OTLP payload: %w", err)
	}
	return normalized, nil
}

type logsService struct {
	logsv1.UnimplementedLogsServiceServer
	collector *Collector
}

func (s logsService) Export(ctx context.Context, request *logsv1.ExportLogsServiceRequest) (*logsv1.ExportLogsServiceResponse, error) {
	return s.collectLogs(ctx, request)
}

func (s logsService) collectLogs(ctx context.Context, request proto.Message) (*logsv1.ExportLogsServiceResponse, error) {
	if !s.collector.authorizedContext(ctx) {
		return nil, status.Error(codes.Unauthenticated, "invalid ingest credentials")
	}
	payload, err := normalizeMessage(request)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.collector.enqueue("logs", payload); err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &logsv1.ExportLogsServiceResponse{}, nil
}

type metricsService struct {
	metricsv1.UnimplementedMetricsServiceServer
	collector *Collector
}

func (s metricsService) Export(ctx context.Context, request *metricsv1.ExportMetricsServiceRequest) (*metricsv1.ExportMetricsServiceResponse, error) {
	if !s.collector.authorizedContext(ctx) {
		return nil, status.Error(codes.Unauthenticated, "invalid ingest credentials")
	}
	payload, err := normalizeMessage(request)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.collector.enqueue("metrics", payload); err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &metricsv1.ExportMetricsServiceResponse{}, nil
}

type traceService struct {
	tracev1.UnimplementedTraceServiceServer
	collector *Collector
}

func (s traceService) Export(ctx context.Context, request *tracev1.ExportTraceServiceRequest) (*tracev1.ExportTraceServiceResponse, error) {
	if !s.collector.authorizedContext(ctx) {
		return nil, status.Error(codes.Unauthenticated, "invalid ingest credentials")
	}
	payload, err := normalizeMessage(request)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.collector.enqueue("traces", payload); err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &tracev1.ExportTraceServiceResponse{}, nil
}

func (c *Collector) authorized(r *http.Request) bool {
	provided := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if provided == "" {
		provided = r.Header.Get("X-Signal-Ingest-Token")
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(c.config.Token)) == 1
}

func (c *Collector) run(ctx context.Context) {
	defer close(c.done)
	batch := make([]Event, 0, c.config.BatchSize)
	ticker := time.NewTicker(c.config.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case event := <-c.queue:
			batch = append(batch, event)
			if len(batch) >= c.config.BatchSize {
				batch = c.flush(ctx, batch)
			}
		case <-ticker.C:
			batch = c.flush(ctx, batch)
		case <-ctx.Done():
			batch = c.drain(batch)
			// The parent context is already cancelled, so give the final
			// delivery its own deadline instead of failing immediately.
			final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if remaining := c.flush(final, batch); len(remaining) > 0 {
				c.dropped.Add(uint64(len(remaining)))
				c.log.Error("events lost at shutdown", "events", len(remaining))
			}
			cancel()
			return
		}
	}
}

// drain moves everything still queued into the batch so a shutdown delivers it
// rather than discarding it.
func (c *Collector) drain(batch []Event) []Event {
	for {
		select {
		case event := <-c.queue:
			batch = append(batch, event)
		default:
			return batch
		}
	}
}

// maxRetained bounds how many events a failing batch may hold. Without it, a
// prolonged intake outage grows the batch until the process is killed.
func (c *Collector) maxRetained() int {
	return c.config.QueueSize
}

// flush attempts delivery and returns the events still awaiting it. A returned
// empty slice means the batch is fully accounted for, whether delivered or
// deliberately dropped.
func (c *Collector) flush(ctx context.Context, batch []Event) []Event {
	if len(batch) == 0 {
		return batch
	}
	err := c.send(ctx, batch)
	if err == nil {
		c.delivered.Add(uint64(len(batch)))
		return batch[:0]
	}
	if errors.Is(err, errPermanent) {
		c.dropped.Add(uint64(len(batch)))
		c.log.Error("dropping undeliverable batch", "error", err, "events", len(batch))
		return batch[:0]
	}
	c.log.Error("intake delivery failed", "error", err, "events", len(batch))
	if overflow := len(batch) - c.maxRetained(); overflow > 0 {
		c.dropped.Add(uint64(overflow))
		c.log.Warn("dropping oldest events awaiting retry", "events", overflow)
		batch = append(batch[:0], batch[overflow:]...)
	}
	return batch
}

// Shutdown stops accepting new work, drains what is queued, and waits for the
// final delivery attempt to finish or ctx to expire.
func (c *Collector) Shutdown(ctx context.Context) error {
	c.cancel()
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Collector) send(ctx context.Context, events []Event) error {
	payload, err := json.Marshal(Envelope{TenantID: c.config.TenantID, Events: events})
	if err != nil {
		return fmt.Errorf("encode envelope: %w: %w", errPermanent, err)
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.IntakeURL, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("create intake request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.config.Token)
		response, err := c.client.Do(req)
		if err == nil {
			response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("intake returned HTTP %d", response.StatusCode)
		} else {
			lastErr = err
		}
		if attempt < 2 {
			time.Sleep(time.Duration(1<<attempt) * 100 * time.Millisecond)
		}
	}
	return lastErr
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
