package ui

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/NathanBhanji/tsnet-proxy/internal/config"
	"github.com/NathanBhanji/tsnet-proxy/internal/health"
	"github.com/NathanBhanji/tsnet-proxy/internal/manager"
)

// APIHandler handles REST API requests
type APIHandler struct {
	manager       *manager.Manager
	healthChecker *health.Checker
	configPath    string
	config        *config.Config
}

// NewAPIHandler creates a new API handler
func NewAPIHandler(mgr *manager.Manager, checker *health.Checker, cfg *config.Config, configPath string) *APIHandler {
	return &APIHandler{
		manager:       mgr,
		healthChecker: checker,
		configPath:    configPath,
		config:        cfg,
	}
}

// ServiceResponse represents a service in API responses
type ServiceResponse struct {
	Name        string   `json:"name"`
	Backend     string   `json:"backend"`
	Paths       []string `json:"paths"`
	StripPrefix bool     `json:"stripPrefix"`
	Healthy     bool     `json:"healthy"`
	HealthCheck struct {
		Enabled            bool   `json:"enabled"`
		Path               string `json:"path"`
		Interval           string `json:"interval"`
		Timeout            string `json:"timeout"`
		UnhealthyThreshold int    `json:"unhealthyThreshold"`
	} `json:"healthCheck"`
	TLS struct {
		Enabled    bool `json:"enabled"`
		SkipVerify bool `json:"skipVerify"`
	} `json:"tls"`
}

// ListServices returns all services
func (h *APIHandler) ListServices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	services := h.manager.GetAllServices()
	response := make([]ServiceResponse, 0, len(services))

	for _, svc := range services {
		svcResp := ServiceResponse{
			Name:        svc.Config.Name,
			Backend:     svc.Config.Backend,
			Paths:       svc.Config.Paths,
			StripPrefix: svc.Config.StripPrefix,
			Healthy:     svc.IsHealthy(),
		}
		svcResp.HealthCheck.Enabled = svc.Config.HealthCheck.Enabled
		svcResp.HealthCheck.Path = svc.Config.HealthCheck.Path
		svcResp.HealthCheck.Interval = svc.Config.HealthCheck.Interval.String()
		svcResp.HealthCheck.Timeout = svc.Config.HealthCheck.Timeout.String()
		svcResp.HealthCheck.UnhealthyThreshold = svc.Config.HealthCheck.UnhealthyThreshold
		svcResp.TLS.Enabled = svc.Config.TLS.Enabled
		svcResp.TLS.SkipVerify = svc.Config.TLS.SkipVerify

		response = append(response, svcResp)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// GetService returns a single service
func (h *APIHandler) GetService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract service name from path /api/services/{name}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		http.Error(w, "Service name required", http.StatusBadRequest)
		return
	}
	name := parts[3]

	svc, exists := h.manager.GetService(name)
	if !exists {
		http.Error(w, "Service not found", http.StatusNotFound)
		return
	}

	svcResp := ServiceResponse{
		Name:        svc.Config.Name,
		Backend:     svc.Config.Backend,
		Paths:       svc.Config.Paths,
		StripPrefix: svc.Config.StripPrefix,
		Healthy:     svc.IsHealthy(),
	}
	svcResp.HealthCheck.Enabled = svc.Config.HealthCheck.Enabled
	svcResp.HealthCheck.Path = svc.Config.HealthCheck.Path
	svcResp.HealthCheck.Interval = svc.Config.HealthCheck.Interval.String()
	svcResp.HealthCheck.Timeout = svc.Config.HealthCheck.Timeout.String()
	svcResp.HealthCheck.UnhealthyThreshold = svc.Config.HealthCheck.UnhealthyThreshold
	svcResp.TLS.Enabled = svc.Config.TLS.Enabled
	svcResp.TLS.SkipVerify = svc.Config.TLS.SkipVerify

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(svcResp)
}

// ServiceRequest represents the JSON request for adding a service
type ServiceRequest struct {
	Name        string   `json:"name"`
	Backend     string   `json:"backend"`
	Paths       []string `json:"paths"`
	StripPrefix bool     `json:"stripPrefix"`
	HealthCheck struct {
		Enabled            bool   `json:"enabled"`
		Path               string `json:"path"`
		Interval           string `json:"interval"`
		Timeout            string `json:"timeout"`
		UnhealthyThreshold int    `json:"unhealthyThreshold"`
	} `json:"healthCheck"`
	TLS struct {
		Enabled    bool `json:"enabled"`
		SkipVerify bool `json:"skipVerify"`
	} `json:"tls"`
}

// AddService adds a new service
func (h *APIHandler) AddService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	// Convert to config.ServiceConfig
	svcCfg := config.ServiceConfig{
		Name:        req.Name,
		Backend:     req.Backend,
		Paths:       req.Paths,
		StripPrefix: req.StripPrefix,
		TLS: config.TLSConfig{
			Enabled:    req.TLS.Enabled,
			SkipVerify: req.TLS.SkipVerify,
		},
	}

	// Parse duration strings
	if req.HealthCheck.Enabled {
		interval, err := time.ParseDuration(req.HealthCheck.Interval)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid interval duration: %v", err), http.StatusBadRequest)
			return
		}

		timeout, err := time.ParseDuration(req.HealthCheck.Timeout)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid timeout duration: %v", err), http.StatusBadRequest)
			return
		}

		svcCfg.HealthCheck = config.HealthCheckConfig{
			Enabled:            req.HealthCheck.Enabled,
			Path:               req.HealthCheck.Path,
			Interval:           interval,
			Timeout:            timeout,
			UnhealthyThreshold: req.HealthCheck.UnhealthyThreshold,
		}
	}

	// Add service to manager
	if err := h.manager.AddService(svcCfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to add service: %v", err), http.StatusInternalServerError)
		return
	}

	// Add to config and save
	h.config.Services = append(h.config.Services, svcCfg)
	if err := config.Save(h.config, h.configPath); err != nil {
		log.Printf("Warning: Failed to save config after adding service: %v", err)
	}

	log.Printf("Service %s added via API", svcCfg.Name)

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "success",
		"message": fmt.Sprintf("Service %s added successfully", svcCfg.Name),
	})
}

// DeleteService removes a service
func (h *APIHandler) DeleteService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract service name from path
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		http.Error(w, "Service name required", http.StatusBadRequest)
		return
	}
	name := parts[3]

	// Remove from manager
	if err := h.manager.RemoveService(name); err != nil {
		http.Error(w, fmt.Sprintf("Failed to remove service: %v", err), http.StatusInternalServerError)
		return
	}

	// Remove from config and save
	newServices := make([]config.ServiceConfig, 0)
	for _, svc := range h.config.Services {
		if svc.Name != name {
			newServices = append(newServices, svc)
		}
	}
	h.config.Services = newServices

	if err := config.Save(h.config, h.configPath); err != nil {
		log.Printf("Warning: Failed to save config after removing service: %v", err)
	}

	log.Printf("Service %s removed via API", name)

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "success",
		"message": fmt.Sprintf("Service %s removed successfully", name),
	})
}

// HealthStatus returns overall health status
func (h *APIHandler) HealthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	statuses := h.healthChecker.GetAllStatuses()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(statuses)
}
