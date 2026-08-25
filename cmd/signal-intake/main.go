package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	tenants, err := loadTenants()
	if err != nil {
		logger.Error("tenant configuration failed", "error", err)
		os.Exit(1)
	}
	config := intake.Config{
		ListenAddress: os.Getenv("SIGNAL_INTAKE_LISTEN_ADDRESS"),
		StoragePath:   os.Getenv("SIGNAL_INTAKE_STORAGE_PATH"),
		Tenants:       tenants,
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
		logger.Info("signal intake listening", "address", config.ListenAddress, "tenants", len(config.Tenants))
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

// loadTenants reads the tenant registry from SIGNAL_INTAKE_TENANTS_FILE, a JSON
// array of {"id","token"} objects. The single-tenant environment variables
// remain supported for local development and existing deployments.
func loadTenants() ([]intake.Tenant, error) {
	path := os.Getenv("SIGNAL_INTAKE_TENANTS_FILE")
	if path == "" {
		token := os.Getenv("SIGNAL_INTAKE_TOKEN")
		id := os.Getenv("SIGNAL_INTAKE_TENANT_ID")
		if token == "" || id == "" {
			return nil, errors.New("set SIGNAL_INTAKE_TENANTS_FILE, or both SIGNAL_INTAKE_TOKEN and SIGNAL_INTAKE_TENANT_ID")
		}
		return []intake.Tenant{{ID: id, Token: token}}, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tenant registry: %w", err)
	}
	var tenants []intake.Tenant
	if err := json.Unmarshal(body, &tenants); err != nil {
		return nil, fmt.Errorf("decode tenant registry: %w", err)
	}
	return tenants, nil
}
