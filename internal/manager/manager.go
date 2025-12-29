package manager

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"sync"

	"github.com/NathanBhanji/tsnet-proxy/internal/config"
	"tailscale.com/client/tailscale"
	"tailscale.com/tsnet"
)

// Manager manages multiple tsnet services
type Manager struct {
	services  map[string]*Service
	authKey   string
	stateDir  string
	apiClient *tailscale.Client
	tailnet   string
	mu        sync.RWMutex
}

// NewManager creates a new Manager instance
func NewManager(authKey, stateDir, apiKey, tailnet string) *Manager {
	var apiClient *tailscale.Client
	if apiKey != "" && tailnet != "" {
		apiClient = tailscale.NewClient(tailnet, nil)
		apiClient.APIKey = apiKey
	}

	return &Manager{
		services:  make(map[string]*Service),
		authKey:   authKey,
		stateDir:  stateDir,
		apiClient: apiClient,
		tailnet:   tailnet,
	}
}

// AddService adds and starts a new service
func (m *Manager) AddService(cfg config.ServiceConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.services[cfg.Name]; exists {
		return fmt.Errorf("service %s already exists", cfg.Name)
	}

	log.Printf("Adding service: %s -> %s", cfg.Name, cfg.Backend)

	// Create service instance
	svc := NewService(cfg)

	// Create tsnet.Server with unique hostname
	ts := &tsnet.Server{
		Hostname:  cfg.Name,
		Dir:       filepath.Join(m.stateDir, cfg.Name),
		AuthKey:   m.authKey,
		Ephemeral: false, // Persist identity across restarts
	}

	// Start the tsnet server
	if err := ts.Start(); err != nil {
		return fmt.Errorf("failed to start tsnet server: %w", err)
	}

	// Create HTTPS listener with automatic Tailscale certificates
	httpsLn, err := ts.ListenTLS("tcp", ":443")
	if err != nil {
		ts.Close()
		return fmt.Errorf("failed to create HTTPS listener: %w", err)
	}

	// Create HTTP listener on port 80
	httpLn, err := ts.Listen("tcp", ":80")
	if err != nil {
		ts.Close()
		return fmt.Errorf("failed to create HTTP listener: %w", err)
	}

	// Parse backend URL
	target, err := url.Parse(cfg.Backend)
	if err != nil {
		ts.Close()
		return fmt.Errorf("invalid backend URL: %w", err)
	}

	// Create reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(target)

	// Customize transport for TLS backends
	if cfg.TLS.Enabled {
		proxy.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: cfg.TLS.SkipVerify,
			},
		}
	}

	// Set up error handler
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("Proxy error for service %s: %v", cfg.Name, err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}

	// Create HTTP handler with path routing and health checking
	handler := m.createHandler(svc, proxy)

	// Start HTTPS server in goroutine
	go func() {
		log.Printf("Service %s listening on HTTPS (port 443) with Tailscale certificates", cfg.Name)
		if err := http.Serve(httpsLn, handler); err != nil {
			log.Printf("Service %s HTTPS stopped: %v", cfg.Name, err)
		}
	}()

	// Start HTTP server in goroutine
	go func() {
		log.Printf("Service %s listening on HTTP (port 80)", cfg.Name)
		if err := http.Serve(httpLn, handler); err != nil {
			log.Printf("Service %s HTTP stopped: %v", cfg.Name, err)
		}
	}()

	svc.tsnetServer = ts
	svc.reverseProxy = proxy
	m.services[cfg.Name] = svc

	log.Printf("Service %s started successfully", cfg.Name)
	return nil
}

// RemoveService stops and removes a service
func (m *Manager) RemoveService(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	svc, exists := m.services[name]
	if !exists {
		return fmt.Errorf("service %s not found", name)
	}

	log.Printf("Removing service: %s", name)

	// Delete device from Tailscale and close the tsnet server
	if svc.tsnetServer != nil {
		m.deleteDevice(svc.tsnetServer, name)
		if err := svc.tsnetServer.Close(); err != nil {
			log.Printf("Error closing tsnet server for %s: %v", name, err)
		}
	}

	delete(m.services, name)
	log.Printf("Service %s removed successfully", name)
	return nil
}

// GetService returns a service by name
func (m *Manager) GetService(name string) (*Service, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	svc, exists := m.services[name]
	return svc, exists
}

// GetAllServices returns all services
func (m *Manager) GetAllServices() map[string]*Service {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Return a copy to prevent external modification
	services := make(map[string]*Service, len(m.services))
	for k, v := range m.services {
		services[k] = v
	}
	return services
}

// createHandler creates an HTTP handler with path routing and health checking
func (m *Manager) createHandler(svc *Service, proxy *httputil.ReverseProxy) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if service is healthy
		if !svc.IsHealthy() {
			log.Printf("Service %s is unhealthy, returning 503", svc.Config.Name)
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			return
		}

		// Path-based routing
		if len(svc.Config.Paths) > 0 {
			matched := false
			originalPath := r.URL.Path

			for _, path := range svc.Config.Paths {
				if strings.HasPrefix(r.URL.Path, path) {
					matched = true
					if svc.Config.StripPrefix {
						// Strip the prefix before forwarding
						r.URL.Path = strings.TrimPrefix(r.URL.Path, path)
						if r.URL.Path == "" {
							r.URL.Path = "/"
						}
					}
					break
				}
			}

			if !matched {
				log.Printf("Service %s: path %s did not match any configured paths", svc.Config.Name, originalPath)
				http.NotFound(w, r)
				return
			}
		}

		// Forward to backend
		log.Printf("Service %s: proxying %s %s to %s", svc.Config.Name, r.Method, r.URL.Path, svc.Config.Backend)
		proxy.ServeHTTP(w, r)
	})
}

// Shutdown gracefully shuts down all services
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()

	log.Printf("Shutting down all services...")
	for name, svc := range m.services {
		log.Printf("Stopping service: %s", name)
		if svc.tsnetServer != nil {
			// Delete device from Tailscale before closing
			m.deleteDevice(svc.tsnetServer, name)
			svc.tsnetServer.Close()
		}
	}
	m.services = make(map[string]*Service)
	log.Printf("All services stopped")
}

// deleteDevice removes the device from Tailscale control plane
func (m *Manager) deleteDevice(ts *tsnet.Server, name string) {
	if m.apiClient == nil {
		log.Printf("API client not configured, skipping device deletion for %s", name)
		return
	}

	// Get local client to get device status
	lc, err := ts.LocalClient()
	if err != nil {
		log.Printf("Failed to get LocalClient for %s: %v", name, err)
		return
	}

	status, err := lc.Status(context.Background())
	if err != nil {
		log.Printf("Failed to get status for %s: %v", name, err)
		return
	}

	if status.Self == nil {
		log.Printf("No self node found for %s", name)
		return
	}

	deviceID := fmt.Sprintf("%d", status.Self.ID)
	log.Printf("Deleting device %s (ID: %s) from Tailscale...", name, deviceID)

	err = m.apiClient.DeleteDevice(context.Background(), deviceID)
	if err != nil {
		log.Printf("Failed to delete device %s: %v", name, err)
	} else {
		log.Printf("Successfully deleted device %s from Tailscale", name)
	}
}
