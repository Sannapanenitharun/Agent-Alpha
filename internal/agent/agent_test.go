package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	logsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	logsdatav1 "go.opentelemetry.io/proto/otlp/logs/v1"
	metricsdatav1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracedatav1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

func TestCollectorRequiresCredentials(t *testing.T) {
	_, err := New(Config{IntakeURL: "http://intake", BatchSize: 1}, slog.Default())
	if err == nil {
		t.Fatal("expected missing credentials to fail")
	}
}

func TestCollectorAuthenticatesAndBatchesAllSignals(t *testing.T) {
	received := make(chan Envelope, 1)
	intake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var envelope Envelope
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Errorf("invalid envelope: %v", err)
		}
		received <- envelope
		w.WriteHeader(http.StatusAccepted)
	}))
	defer intake.Close()

	collector, err := New(Config{IntakeURL: intake.URL, TenantID: "tenant-a", Token: "secret", BatchSize: 3, FlushInterval: time.Hour, QueueSize: 3}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(collector.Handler())
	defer server.Close()

	for _, signal := range []string{"logs", "metrics", "traces"} {
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/"+signal, strings.NewReader(`{"service":"checkout"}`))
		req.Header.Set("Authorization", "Bearer secret")
		response, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil || response.StatusCode != http.StatusAccepted {
			t.Fatalf("signal %s was not accepted: %v", signal, requestErr)
		}
		response.Body.Close()
	}
	select {
	case envelope := <-received:
		if envelope.TenantID != "tenant-a" || len(envelope.Events) != 3 {
			t.Fatalf("unexpected envelope: %+v", envelope)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for intake delivery")
	}
}

func TestCollectorAcceptsOTLPProtobuf(t *testing.T) {
	received := make(chan Envelope, 1)
	intake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var envelope Envelope
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Errorf("invalid envelope: %v", err)
		}
		received <- envelope
		w.WriteHeader(http.StatusAccepted)
	}))
	defer intake.Close()

	collector, err := New(Config{IntakeURL: intake.URL, TenantID: "tenant-otel", Token: "secret", BatchSize: 3, FlushInterval: time.Hour}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(collector.Handler())
	defer server.Close()

	payloads := map[string]proto.Message{
		"logs":    &logsv1.ExportLogsServiceRequest{ResourceLogs: []*logsdatav1.ResourceLogs{{}}},
		"metrics": &metricsv1.ExportMetricsServiceRequest{ResourceMetrics: []*metricsdatav1.ResourceMetrics{{}}},
		"traces":  &tracev1.ExportTraceServiceRequest{ResourceSpans: []*tracedatav1.ResourceSpans{{}}},
	}
	for signal, payload := range payloads {
		body, marshalErr := proto.Marshal(payload)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		req, requestErr := http.NewRequest(http.MethodPost, server.URL+"/v1/"+signal, bytes.NewReader(body))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("Content-Type", "application/x-protobuf")
		response, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil || response.StatusCode != http.StatusAccepted {
			t.Fatalf("OTLP signal %s was not accepted: %v", signal, requestErr)
		}
		response.Body.Close()
	}

	select {
	case envelope := <-received:
		if envelope.TenantID != "tenant-otel" || len(envelope.Events) != 3 {
			t.Fatalf("unexpected OTLP envelope: %+v", envelope)
		}
		for _, event := range envelope.Events {
			if !json.Valid(event.Payload) {
				t.Fatalf("event %s was not normalized to JSON: %s", event.Type, event.Payload)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OTLP intake delivery")
	}
}

func TestCollectorRejectsMalformedOTLP(t *testing.T) {
	collector, err := New(Config{IntakeURL: "http://intake", TenantID: "tenant-a", Token: "secret"}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader("not-protobuf"))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/x-protobuf")
	recorder := httptest.NewRecorder()
	collector.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected malformed OTLP to return 400, got %d", recorder.Code)
	}
}

func TestCollectorAcceptsAuthenticatedOTLPGRPC(t *testing.T) {
	received := make(chan Envelope, 1)
	intake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var envelope Envelope
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Errorf("invalid envelope: %v", err)
		}
		received <- envelope
		w.WriteHeader(http.StatusAccepted)
	}))
	defer intake.Close()

	collector, err := New(Config{IntakeURL: intake.URL, TenantID: "tenant-grpc", Token: "secret", BatchSize: 1}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer secret"))
	_, err = (metricsService{collector: collector}).Export(ctx, &metricsv1.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricsdatav1.ResourceMetrics{{}},
	})
	if err != nil {
		t.Fatalf("gRPC export failed: %v", err)
	}
	select {
	case envelope := <-received:
		if envelope.TenantID != "tenant-grpc" || len(envelope.Events) != 1 || envelope.Events[0].Type != "metrics" {
			t.Fatalf("unexpected gRPC envelope: %+v", envelope)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for gRPC intake delivery")
	}
}
