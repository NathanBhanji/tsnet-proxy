package health

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/NathanBhanji/tsnet-proxy/internal/manager"
)

// Checker performs periodic health checks on services
type Checker struct {
	manager *manager.Manager
	client  *http.Client
	mu      sync.RWMutex
	stopCh  chan struct{}
}

// NewChecker creates a new health checker instance
func NewChecker(mgr *manager.Manager) *Checker {
	return &Checker{
		manager: mgr,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				IdleConnTimeout:     90 * time.Second,
				DisableCompression:  true,
				DisableKeepAlives:   false,
				MaxIdleConnsPerHost: 10,
			},
		},
		stopCh: make(chan struct{}),
	}
}

// Start begins health checking for all services
func (c *Checker) Start(ctx context.Context) {
	services := c.manager.GetAllServices()

	for _, svc := range services {
		if svc.Config.HealthCheck.Enabled {
			go c.runHealthCheck(ctx, svc)
		}
	}

	log.Printf("Health checker started for %d services", len(services))
}

// Stop stops all health checking
func (c *Checker) Stop() {
	close(c.stopCh)
	log.Printf("Health checker stopped")
}

// runHealthCheck performs periodic health checks for a single service
func (c *Checker) runHealthCheck(ctx context.Context, svc *manager.Service) {
	cfg := svc.Config.HealthCheck
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	failureCount := 0

	log.Printf("Starting health checks for service %s (interval: %s, path: %s)",
		svc.Config.Name, cfg.Interval, cfg.Path)

	for {
		select {
		case <-ctx.Done():
			log.Printf("Health checker for service %s stopped (context done)", svc.Config.Name)
			return
		case <-c.stopCh:
			log.Printf("Health checker for service %s stopped", svc.Config.Name)
			return
		case <-ticker.C:
			healthy := c.performCheck(svc)

			if !healthy {
				failureCount++
				log.Printf("Health check failed for service %s (failures: %d/%d)",
					svc.Config.Name, failureCount, cfg.UnhealthyThreshold)

				if failureCount >= cfg.UnhealthyThreshold {
					if svc.IsHealthy() {
						log.Printf("Service %s marked UNHEALTHY after %d consecutive failures",
							svc.Config.Name, failureCount)
						svc.SetHealthy(false)
					}
				}
			} else {
				if failureCount > 0 || !svc.IsHealthy() {
					log.Printf("Service %s marked HEALTHY", svc.Config.Name)
				}
				failureCount = 0
				svc.SetHealthy(true)
			}
		}
	}
}

// performCheck executes a single health check
func (c *Checker) performCheck(svc *manager.Service) bool {
	cfg := svc.Config.HealthCheck
	healthURL := svc.Config.Backend + cfg.Path

	checkCtx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(checkCtx, "GET", healthURL, nil)
	if err != nil {
		log.Printf("Failed to create health check request for %s: %v", svc.Config.Name, err)
		return false
	}

	resp, err := c.client.Do(req)
	if err != nil {
		log.Printf("Health check error for %s: %v", svc.Config.Name, err)
		return false
	}
	defer resp.Body.Close()

	// Consider 2xx and 3xx as healthy
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return true
	}

	log.Printf("Health check for %s returned status %d", svc.Config.Name, resp.StatusCode)
	return false
}

// GetServiceStatus returns the health status of a specific service
func (c *Checker) GetServiceStatus(name string) (healthy bool, exists bool) {
	svc, exists := c.manager.GetService(name)
	if !exists {
		return false, false
	}
	return svc.IsHealthy(), true
}

// GetAllStatuses returns health status for all services
func (c *Checker) GetAllStatuses() map[string]bool {
	services := c.manager.GetAllServices()
	statuses := make(map[string]bool, len(services))

	for name, svc := range services {
		statuses[name] = svc.IsHealthy()
	}

	return statuses
}
