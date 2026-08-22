package metrics

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/NathanBhanji/tsnet-proxy/internal/config"
	"github.com/NathanBhanji/tsnet-proxy/internal/manager"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// Request metrics
	requestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tsnet_proxy_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"service", "method", "status"},
	)

	requestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "tsnet_proxy_request_duration_seconds",
			Help:    "HTTP request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"service", "method"},
	)

	// Service health metrics
	serviceHealth = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "tsnet_proxy_service_health",
			Help: "Service health status (1 = healthy, 0 = unhealthy)",
		},
		[]string{"service"},
	)
)

// RecordRequest records one proxied request. Its signature matches
// manager.RequestObserver; register it with Manager.AddRequestObserver so
// every service's traffic is counted.
func RecordRequest(service, method, _ string, status int, d time.Duration) {
	requestsTotal.WithLabelValues(service, method, strconv.Itoa(status)).Inc()
	requestDuration.WithLabelValues(service, method).Observe(d.Seconds())
}

// Handler returns the Prometheus exposition handler. It is the same handler
// served on the metrics port, exposed so the management UI can mount it at
// /api/metrics without a second scrape target.
func Handler() http.Handler {
	return promhttp.Handler()
}

// MetricsServer handles Prometheus metrics
type MetricsServer struct {
	config  *config.Config
	manager *manager.Manager
	server  *http.Server
}

// NewMetricsServer creates a new metrics server
func NewMetricsServer(cfg *config.Config, mgr *manager.Manager) *MetricsServer {
	return &MetricsServer{
		config:  cfg,
		manager: mgr,
	}
}

// Start starts the metrics server
func (m *MetricsServer) Start() error {
	if !m.config.Metrics.Enabled {
		log.Printf("Metrics server is disabled")
		return nil
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", Handler())

	m.server = &http.Server{
		Addr:    ":" + strconv.Itoa(m.config.Metrics.Port),
		Handler: mux,
	}

	go func() {
		log.Printf("Metrics server listening on :%d", m.config.Metrics.Port)
		if err := m.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Metrics server error: %v", err)
		}
	}()

	// Start background metrics collector
	go m.collectServiceMetrics()

	return nil
}

// Stop stops the metrics server
func (m *MetricsServer) Stop() {
	if m.server != nil {
		log.Printf("Stopping metrics server...")
		m.server.Close()
	}
}

// collectServiceMetrics periodically collects metrics from services
func (m *MetricsServer) collectServiceMetrics() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		services := m.manager.GetAllServices()
		for name, svc := range services {
			// Update health gauge
			if svc.IsHealthy() {
				serviceHealth.WithLabelValues(name).Set(1)
			} else {
				serviceHealth.WithLabelValues(name).Set(0)
			}
		}
	}
}
