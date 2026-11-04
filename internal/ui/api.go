package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NathanBhanji/tsnet-proxy/internal/activity"
	"github.com/NathanBhanji/tsnet-proxy/internal/config"
	"github.com/NathanBhanji/tsnet-proxy/internal/health"
	"github.com/NathanBhanji/tsnet-proxy/internal/manager"
)

// maxConfigBytes bounds the size of a config uploaded via PUT /api/config.
const maxConfigBytes = 1 << 20

// APIHandler handles REST API requests
type APIHandler struct {
	manager       *manager.Manager
	healthChecker *health.Checker
	activity      *activity.Log
	configPath    string
	config        *config.Config

	// mu serialises mutations of config (in memory and on disk) and the
	// reconcile performed by PUT /api/config.
	mu sync.Mutex
}

// NewAPIHandler creates a new API handler
func NewAPIHandler(mgr *manager.Manager, checker *health.Checker, cfg *config.Config, configPath string, actLog *activity.Log) *APIHandler {
	return &APIHandler{
		manager:       mgr,
		healthChecker: checker,
		activity:      actLog,
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

func serviceResponse(svc *manager.Service) ServiceResponse {
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
	return svcResp
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
		response = append(response, serviceResponse(svc))
	}

	// GetAllServices returns a map, so iteration order is random; sort by name
	// so the API (and the UI cards built from it) have a stable order.
	sort.Slice(response, func(i, j int) bool { return response[i].Name < response[j].Name })

	writeJSON(w, http.StatusOK, response)
}

// GetService returns a single service
func (h *APIHandler) GetService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name, ok := serviceNameFromPath(r.URL.Path)
	if !ok {
		http.Error(w, "Service name required", http.StatusBadRequest)
		return
	}

	svc, exists := h.manager.GetService(name)
	if !exists {
		http.Error(w, "Service not found", http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, serviceResponse(svc))
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

	h.mu.Lock()
	defer h.mu.Unlock()

	// Add service to manager
	if err := h.manager.AddService(svcCfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to add service: %v", err), http.StatusInternalServerError)
		return
	}
	if svc, ok := h.manager.GetService(svcCfg.Name); ok {
		h.healthChecker.Watch(svc)
	}

	// Add to config and save
	h.config.Services = append(h.config.Services, svcCfg)
	if err := config.SaveService(h.configPath, svcCfg.Name, &svcCfg); err != nil {
		log.Printf("Warning: Failed to save config after adding service: %v", err)
	}

	log.Printf("Service %s added via API", svcCfg.Name)

	writeJSON(w, http.StatusCreated, map[string]string{
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

	name, ok := serviceNameFromPath(r.URL.Path)
	if !ok {
		http.Error(w, "Service name required", http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Stop health checks, then remove from manager
	h.healthChecker.Unwatch(name)
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

	if err := config.SaveService(h.configPath, name, nil); err != nil {
		log.Printf("Warning: Failed to save config after removing service: %v", err)
	}

	log.Printf("Service %s removed via API", name)

	writeJSON(w, http.StatusOK, map[string]string{
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

	writeJSON(w, http.StatusOK, h.healthChecker.GetAllStatuses())
}

// Activity returns the most recent proxied requests, newest first.
// GET /api/activity?limit=N (default 50, max 200)
func (h *APIHandler) Activity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.activity == nil {
		http.Error(w, "Activity log not enabled", http.StatusNotFound)
		return
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
			return
		}
		limit = n
	}
	if limit > 200 {
		limit = 200
	}

	entries := h.activity.Recent(limit)
	if entries == nil {
		entries = []activity.Entry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// Config serves the raw configuration file.
//
//	GET /api/config  -> the YAML file as stored on disk (env placeholders intact)
//	PUT /api/config  -> validate the body, write it to disk and hot-reload services
func (h *APIHandler) Config(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.getConfig(w, r)
	case http.MethodPut:
		h.putConfig(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *APIHandler) getConfig(w http.ResponseWriter, _ *http.Request) {
	h.mu.Lock()
	data, err := os.ReadFile(h.configPath)
	h.mu.Unlock()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to read config file: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

// ApplyResult describes what a PUT /api/config did.
type ApplyResult struct {
	Status    string   `json:"status"` // "success" or "partial"
	Added     []string `json:"added"`
	Removed   []string `json:"removed"`
	Updated   []string `json:"updated"`
	Unchanged int      `json:"unchanged"`
	// RestartRequired lists top-level settings that changed in the file but
	// cannot be applied without restarting the process.
	RestartRequired []string `json:"restartRequired"`
	Errors          []string `json:"errors"`
}

func (h *APIHandler) putConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxConfigBytes+1))
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to read request body: %v", err), http.StatusBadRequest)
		return
	}
	if len(body) > maxConfigBytes {
		http.Error(w, "Config too large", http.StatusRequestEntityTooLarge)
		return
	}

	newCfg, err := config.Parse(body)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid config: %v", err), http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if err := writeFileAtomic(h.configPath, body, 0600); err != nil {
		http.Error(w, fmt.Sprintf("Failed to write config file: %v", err), http.StatusInternalServerError)
		return
	}
	log.Printf("Config file %s rewritten via API (%d bytes)", h.configPath, len(body))

	result := h.reconcile(newCfg)

	// Services are now whatever the file says; other top-level settings keep
	// their running values until restart (reported in RestartRequired).
	h.config.Services = newCfg.Services

	status := http.StatusOK
	if len(result.Errors) > 0 {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, result)
}

// reconcile brings the running services in line with cfg: services missing
// from cfg are removed (and their devices deleted), new ones are started, and
// ones whose configuration changed are restarted in place. h.mu must be held.
func (h *APIHandler) reconcile(cfg *config.Config) ApplyResult {
	res := ApplyResult{
		Status:          "success",
		Added:           []string{},
		Removed:         []string{},
		Updated:         []string{},
		RestartRequired: restartRequiredFields(h.config, cfg),
		Errors:          []string{},
	}

	current := h.manager.GetAllServices()
	desired := make(map[string]struct{}, len(cfg.Services))
	for _, s := range cfg.Services {
		desired[s.Name] = struct{}{}
	}

	// Removals first so a rename frees its hostname before the new one starts.
	names := make([]string, 0, len(current))
	for name := range current {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, keep := desired[name]; keep {
			continue
		}
		h.healthChecker.Unwatch(name)
		if err := h.manager.RemoveService(name); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("remove %s: %v", name, err))
			continue
		}
		res.Removed = append(res.Removed, name)
	}

	for _, svcCfg := range cfg.Services {
		if running, ok := current[svcCfg.Name]; ok {
			if sameServiceConfig(running.Config, svcCfg) {
				res.Unchanged++
				continue
			}
			h.healthChecker.Unwatch(svcCfg.Name)
			if err := h.manager.ReplaceService(svcCfg); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("update %s: %v", svcCfg.Name, err))
				continue
			}
			res.Updated = append(res.Updated, svcCfg.Name)
		} else {
			if err := h.manager.AddService(svcCfg); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("add %s: %v", svcCfg.Name, err))
				continue
			}
			res.Added = append(res.Added, svcCfg.Name)
		}
		if svc, ok := h.manager.GetService(svcCfg.Name); ok {
			h.healthChecker.Watch(svc)
		}
	}

	if len(res.Errors) > 0 {
		res.Status = "partial"
	}
	log.Printf("Config applied: %d added, %d removed, %d updated, %d unchanged, %d errors",
		len(res.Added), len(res.Removed), len(res.Updated), res.Unchanged, len(res.Errors))
	return res
}

// restartRequiredFields lists top-level settings that differ between the
// running config and the newly written one.
func restartRequiredFields(running, next *config.Config) []string {
	fields := []string{}
	if running.AuthKey != next.AuthKey {
		fields = append(fields, "authKey")
	}
	if running.APIKey != next.APIKey {
		fields = append(fields, "apiKey")
	}
	if running.Tailnet != next.Tailnet {
		fields = append(fields, "tailnet")
	}
	if running.StateDir != next.StateDir {
		fields = append(fields, "stateDir")
	}
	if running.ManagementUI != next.ManagementUI {
		fields = append(fields, "managementUI")
	}
	if running.Metrics != next.Metrics {
		fields = append(fields, "metrics")
	}
	return fields
}

// sameServiceConfig compares two service configs ignoring representation
// differences that don't change behaviour (nil vs empty paths, health check
// fields when health checks are disabled).
func sameServiceConfig(a, b config.ServiceConfig) bool {
	return reflect.DeepEqual(normalizeServiceConfig(a), normalizeServiceConfig(b))
}

func normalizeServiceConfig(c config.ServiceConfig) config.ServiceConfig {
	if len(c.Paths) == 0 {
		c.Paths = nil
	}
	if !c.HealthCheck.Enabled {
		c.HealthCheck = config.HealthCheckConfig{}
	}
	if !c.TLS.Enabled {
		c.TLS = config.TLSConfig{}
	}
	return c
}

// writeFileAtomic writes data to a temp file in the same directory and renames
// it over path, so a crash mid-write never leaves a truncated config.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// serviceNameFromPath extracts {name} from /api/services/{name}.
func serviceNameFromPath(p string) (string, bool) {
	parts := strings.Split(p, "/")
	if len(parts) < 4 || parts[3] == "" {
		return "", false
	}
	return parts[3], true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("Failed to encode JSON response: %v", err)
	}
}
