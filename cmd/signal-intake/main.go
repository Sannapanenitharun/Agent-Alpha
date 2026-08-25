package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/signal-observability/collector/internal/intake"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	config := intake.Config{
		ListenAddress: os.Getenv("SIGNAL_INTAKE_LISTEN_ADDRESS"),
		Token:         os.Getenv("SIGNAL_INTAKE_TOKEN"),
		TenantID:      os.Getenv("SIGNAL_INTAKE_TENANT_ID"),
		StoragePath:   os.Getenv("SIGNAL_INTAKE_STORAGE_PATH"),
	}
	if config.ListenAddress == "" {
		config.ListenAddress = ":8080"
	}
	if config.StoragePath == "" {
		config.StoragePath = "./data/telemetry.jsonl"
	}
	store, err := intake.NewJSONLStore(config.StoragePath)
	if err != nil {
		logger.Error("intake storage configuration failed", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	service, err := intake.New(config, store, logger)
	if err != nil {
		logger.Error("intake configuration failed", "error", err)
		os.Exit(1)
	}
	server := &http.Server{
		Addr:              config.ListenAddress,
		Handler:           service.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		logger.Info("signal intake listening", "address", config.ListenAddress, "tenant", config.TenantID)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("intake stopped", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	logger.Info("shutdown requested")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("intake shutdown failed", "error", err)
	}
	logger.Info("shutdown complete")
}
