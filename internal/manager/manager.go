package manager

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/NathanBhanji/tsnet-proxy/internal/config"
	"tailscale.com/client/tailscale"
	"tailscale.com/tsnet"
)

// RequestObserver is notified after every request a service handles,
// including requests rejected by the proxy itself (503 unhealthy, 404 path
// mismatch). path is the request path as received, before any prefix
// stripping. Observers must be fast and must not block.
type RequestObserver func(service, method, path string, status int, duration time.Duration)

// Manager manages multiple tsnet services
type Manager struct {
	services  map[string]*Service
	authKey   string
	stateDir  string
	apiClient *tailscale.Client
	tailnet   string
	mu        sync.RWMutex

	obsMu     sync.RWMutex
	observers []RequestObserver
}

// NewManager creates a new Manager instance
func NewManager(authKey, stateDir, apiKey, tailnet string) *Manager {
	var apiClient *tailscale.Client
	if apiKey != "" && tailnet != "" {
		apiClient = tailscale.NewClient(tailnet, tailscale.APIKey(apiKey))
	}

	return &Manager{
		services:  make(map[string]*Service),
		authKey:   authKey,
		stateDir:  stateDir,
		apiClient: apiClient,
		tailnet:   tailnet,
	}
}

// AddRequestObserver registers a callback invoked for every proxied request
// across all services (metrics, activity log, ...).
func (m *Manager) AddRequestObserver(fn RequestObserver) {
	if fn == nil {
		return
	}
	m.obsMu.Lock()
	m.observers = append(m.observers, fn)
	m.obsMu.Unlock()
}

func (m *Manager) notify(service, method, path string, status int, d time.Duration) {
	m.obsMu.RLock()
	observers := m.observers
	m.obsMu.RUnlock()
	for _, fn := range observers {
		fn(service, method, path, status, d)
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
	return m.startServiceLocked(cfg)
}

// ReplaceService restarts an existing service with a new configuration. Unlike
// RemoveService+AddService it keeps the service's Tailscale device: the tsnet
// server is closed and restarted from the same state directory, so the node
// identity (and therefore its IP and DNS name) is preserved. The service is
// briefly unavailable while it reconnects.
func (m *Manager) ReplaceService(cfg config.ServiceConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	old, exists := m.services[cfg.Name]
	if !exists {
		return fmt.Errorf("service %s not found", cfg.Name)
	}

	log.Printf("Restarting service %s with updated configuration", cfg.Name)
	if old.tsnetServer != nil {
		if err := old.tsnetServer.Close(); err != nil {
			log.Printf("Error closing tsnet server for %s: %v", cfg.Name, err)
		}
	}
	delete(m.services, cfg.Name)

	return m.startServiceLocked(cfg)
}

// startServiceLocked creates and starts a service. m.mu must be held.
func (m *Manager) startServiceLocked(cfg config.ServiceConfig) error {
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
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	// Observe every request (metrics, activity feed) with its final status.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		path := r.URL.Path
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		inner.ServeHTTP(rec, r)
		m.notify(svc.Config.Name, r.Method, path, rec.status, time.Since(start))
	})
}

// statusRecorder captures the response status code. It implements Unwrap so
// http.ResponseController (used by httputil.ReverseProxy for flushing and
// WebSocket hijacking) can reach the underlying writer, and Flush/Hijack
// directly for handlers that type-assert instead.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (rw *statusRecorder) WriteHeader(code int) {
	if !rw.written {
		rw.status = code
		rw.written = true
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *statusRecorder) Write(b []byte) (int, error) {
	if !rw.written {
		rw.status = http.StatusOK
		rw.written = true
	}
	return rw.ResponseWriter.Write(b)
}

func (rw *statusRecorder) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

func (rw *statusRecorder) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (rw *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := rw.ResponseWriter.(http.Hijacker); ok {
		if !rw.written {
			rw.status = http.StatusSwitchingProtocols
			rw.written = true
		}
		return h.Hijack()
	}
	return nil, nil, errors.New("response writer does not support hijacking")
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

	deviceID := string(status.Self.ID)
	log.Printf("Deleting device %s (ID: %s) from Tailscale...", name, deviceID)

	err = m.apiClient.DeleteDevice(context.Background(), deviceID)
	if err != nil {
		log.Printf("Failed to delete device %s: %v", name, err)
	} else {
		log.Printf("Successfully deleted device %s from Tailscale", name)
	}
}
