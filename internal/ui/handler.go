package ui

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"time"

	"github.com/NathanBhanji/tsnet-proxy/internal/config"
	"github.com/NathanBhanji/tsnet-proxy/internal/health"
	"github.com/NathanBhanji/tsnet-proxy/internal/manager"
	"tailscale.com/tsnet"
)

//go:embed static/*
var staticFiles embed.FS

// UIServer represents the management UI server
type UIServer struct {
	tsnetServer   *tsnet.Server
	apiHandler    *APIHandler
	config        *config.Config
	configPath    string
	manager       *manager.Manager
	healthChecker *health.Checker
}

// NewUIServer creates a new UI server instance
func NewUIServer(cfg *config.Config, configPath string, mgr *manager.Manager, checker *health.Checker, authKey, stateDir string) *UIServer {
	return &UIServer{
		config:        cfg,
		configPath:    configPath,
		manager:       mgr,
		healthChecker: checker,
		apiHandler:    NewAPIHandler(mgr, checker, cfg, configPath),
		tsnetServer: &tsnet.Server{
			Hostname:  cfg.ManagementUI.Hostname,
			Dir:       stateDir + "/" + cfg.ManagementUI.Hostname,
			AuthKey:   authKey,
			Ephemeral: false,
		},
	}
}

// Start starts the UI server
func (s *UIServer) Start() error {
	if !s.config.ManagementUI.Enabled {
		log.Printf("Management UI is disabled")
		return nil
	}

	log.Printf("Starting management UI server...")
	log.Printf("Auth key (first 20 chars): %s...", s.tsnetServer.AuthKey[:20])
	log.Printf("Hostname: %s", s.tsnetServer.Hostname)
	log.Printf("State dir: %s", s.tsnetServer.Dir)

	// Enable verbose logging from tsnet
	s.tsnetServer.Logf = log.Printf

	// Use Up() to start and wait for authentication with timeout
	log.Printf("Connecting to Tailscale network...")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	status, err := s.tsnetServer.Up(ctx)
	if err != nil {
		return fmt.Errorf("failed to connect to Tailscale: %w", err)
	}
	log.Printf("Connected to Tailscale! Status: %v", status.BackendState)

	// Create listener
	ln, err := s.tsnetServer.Listen("tcp", ":80")
	if err != nil {
		s.tsnetServer.Close()
		return err
	}

	// Create HTTP mux
	mux := http.NewServeMux()

	// API endpoints
	mux.HandleFunc("/api/services", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.apiHandler.ListServices(w, r)
		case http.MethodPost:
			s.apiHandler.AddService(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/api/services/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.apiHandler.GetService(w, r)
		case http.MethodDelete:
			s.apiHandler.DeleteService(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/api/health", s.apiHandler.HealthStatus)

	// Serve static files
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return err
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	// Start HTTP server in goroutine
	go func() {
		log.Printf("Management UI listening on http://%s.your-tailnet.ts.net", s.config.ManagementUI.Hostname)
		if err := http.Serve(ln, mux); err != nil {
			log.Printf("Management UI server error: %v", err)
		}
	}()

	return nil
}

// Stop stops the UI server
func (s *UIServer) Stop() {
	if s.tsnetServer != nil {
		log.Printf("Stopping management UI server...")
		s.tsnetServer.Close()
	}
}
