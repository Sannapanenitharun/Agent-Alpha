package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/signal-observability/collector/internal/agent"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	config := agent.Config{
		ListenAddress:     os.Getenv("SIGNAL_AGENT_LISTEN_ADDRESS"),
		GRPCListenAddress: os.Getenv("SIGNAL_AGENT_GRPC_LISTEN_ADDRESS"),
		IntakeURL:         os.Getenv("SIGNAL_INTAKE_URL"),
		TenantID:          os.Getenv("SIGNAL_TENANT_ID"),
		Token:             os.Getenv("SIGNAL_INGEST_TOKEN"),
		BatchSize:         100,
		FlushInterval:     2 * time.Second,
		QueueSize:         10_000,
	}
	if config.ListenAddress == "" {
		config.ListenAddress = ":4318"
	}
	if config.GRPCListenAddress == "" {
		config.GRPCListenAddress = ":4317"
	}
	if config.FlushInterval <= 0 {
		config.FlushInterval = 2 * time.Second
	}
	collector, err := agent.New(config, logger)
	if err != nil {
		logger.Error("collector configuration failed", "error", err)
		os.Exit(1)
	}
	logger.Info("signal collector listening", "address", config.ListenAddress, "tenant", config.TenantID)
	grpcListener, err := net.Listen("tcp", config.GRPCListenAddress)
	if err != nil {
		logger.Error("gRPC listener failed", "address", config.GRPCListenAddress, "error", err)
		os.Exit(1)
	}
	go func() {
		logger.Info("signal collector gRPC listening", "address", config.GRPCListenAddress, "tenant", config.TenantID)
		if err := collector.GRPCServer().Serve(grpcListener); err != nil {
			logger.Error("collector gRPC stopped", "error", err)
		}
	}()
	if err := http.ListenAndServe(config.ListenAddress, collector.Handler()); err != nil {
		logger.Error("collector stopped", "error", err)
		os.Exit(1)
	}
}
