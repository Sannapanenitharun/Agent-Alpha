package main

import (
	"log/slog"
	"net/http"
	"os"

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
	logger.Info("signal intake listening", "address", config.ListenAddress, "tenant", config.TenantID)
	if err := http.ListenAndServe(config.ListenAddress, service.Handler()); err != nil {
		logger.Error("intake stopped", "error", err)
		os.Exit(1)
	}
}
