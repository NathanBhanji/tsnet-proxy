package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/NathanBhanji/tsnet-proxy/internal/config"
	"github.com/NathanBhanji/tsnet-proxy/internal/health"
	"github.com/NathanBhanji/tsnet-proxy/internal/manager"
	"github.com/NathanBhanji/tsnet-proxy/internal/metrics"
	"github.com/NathanBhanji/tsnet-proxy/internal/ui"
)

var (
	configPath = flag.String("config", "configs/services.yaml", "Path to configuration file")
)

func main() {
	flag.Parse()

	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.Printf("Starting tsnet-proxy...")

	// Load configuration
	log.Printf("Loading configuration from: %s", *configPath)
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	log.Printf("Loaded configuration with %d services", len(cfg.Services))

	// Create manager
	mgr := manager.NewManager(cfg.AuthKey, cfg.StateDir, cfg.APIKey, cfg.Tailnet)

	// Add all configured services
	for _, svcCfg := range cfg.Services {
		if err := mgr.AddService(svcCfg); err != nil {
			log.Printf("Failed to add service %s: %v", svcCfg.Name, err)
			continue
		}
	}

	// Start health checker
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	healthChecker := health.NewChecker(mgr)
	healthChecker.Start(ctx)

	// Start management UI
	uiServer := ui.NewUIServer(cfg, *configPath, mgr, healthChecker, cfg.AuthKey, cfg.StateDir)
	if err := uiServer.Start(); err != nil {
		log.Fatalf("Failed to start management UI: %v", err)
	}

	// Start metrics server
	metricsServer := metrics.NewMetricsServer(cfg, mgr)
	if err := metricsServer.Start(); err != nil {
		log.Fatalf("Failed to start metrics server: %v", err)
	}

	log.Printf("tsnet-proxy started successfully")

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	<-sigChan
	log.Printf("Received shutdown signal")

	// Graceful shutdown
	cancel() // Stop health checker
	healthChecker.Stop()
	uiServer.Stop()
	metricsServer.Stop()
	mgr.Shutdown()
	log.Printf("tsnet-proxy stopped")
}
