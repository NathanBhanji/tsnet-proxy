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

	// Active connections
	activeConnections = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "tsnet_proxy_active_connections",
			Help: "Number of active connections per service",
		},
		[]string{"service"},
	)
)

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
	mux.Handle("/metrics", promhttp.Handler())

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

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (rw *responseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.written = true
		rw.ResponseWriter.WriteHeader(code)
	}
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.ResponseWriter.Write(b)
}

// MetricsMiddleware wraps an HTTP handler with Prometheus metrics collection
func MetricsMiddleware(serviceName string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap response writer to capture status code
		rw := &responseWriter{ResponseWriter: w, statusCode: 200}

		// Track active connections
		activeConnections.WithLabelValues(serviceName).Inc()
		defer activeConnections.WithLabelValues(serviceName).Dec()

		// Handle request
		next.ServeHTTP(rw, r)

		// Record metrics
		duration := time.Since(start).Seconds()
		requestsTotal.WithLabelValues(serviceName, r.Method, strconv.Itoa(rw.statusCode)).Inc()
		requestDuration.WithLabelValues(serviceName, r.Method).Observe(duration)
	})
}
