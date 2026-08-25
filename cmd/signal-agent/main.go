package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/signal-observability/collector/internal/agent"
	"google.golang.org/grpc"
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
	collector, err := agent.New(config, logger)
	if err != nil {
		logger.Error("collector configuration failed", "error", err)
		os.Exit(1)
	}

	grpcListener, err := net.Listen("tcp", config.GRPCListenAddress)
	if err != nil {
		logger.Error("gRPC listener failed", "address", config.GRPCListenAddress, "error", err)
		os.Exit(1)
	}
	grpcServer := collector.GRPCServer()
	go func() {
		logger.Info("signal collector gRPC listening", "address", config.GRPCListenAddress, "tenant", config.TenantID)
		if err := grpcServer.Serve(grpcListener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			logger.Error("collector gRPC stopped", "error", err)
		}
	}()

	// Timeouts are required: without them a single slow client can hold a
	// connection open indefinitely.
	server := &http.Server{
		Addr:              config.ListenAddress,
		Handler:           collector.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		logger.Info("signal collector listening", "address", config.ListenAddress, "tenant", config.TenantID)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("collector stopped", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	logger.Info("shutdown requested, draining telemetry")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("http shutdown failed", "error", err)
	}
	grpcServer.GracefulStop()
	// Drain last: the listeners must be closed first so nothing new arrives
	// while the queue is being flushed.
	if err := collector.Shutdown(ctx); err != nil {
		logger.Error("collector drain incomplete", "error", err)
	}
	logger.Info("shutdown complete")
}
