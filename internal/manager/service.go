package manager

import (
	"net/http/httputil"
	"sync/atomic"

	"github.com/NathanBhanji/tsnet-proxy/internal/config"
	"tailscale.com/tsnet"
)

// Service represents a running service with its tsnet server and reverse proxy
type Service struct {
	Config       config.ServiceConfig
	tsnetServer  *tsnet.Server
	reverseProxy *httputil.ReverseProxy
	healthy      atomic.Bool
}

// NewService creates a new Service instance
func NewService(cfg config.ServiceConfig) *Service {
	svc := &Service{
		Config: cfg,
	}
	svc.healthy.Store(true) // Assume healthy initially
	return svc
}

// IsHealthy returns the current health status
func (s *Service) IsHealthy() bool {
	return s.healthy.Load()
}

// SetHealthy updates the health status
func (s *Service) SetHealthy(healthy bool) {
	s.healthy.Store(healthy)
}

// GetTsnetServer returns the tsnet server instance
func (s *Service) GetTsnetServer() *tsnet.Server {
	return s.tsnetServer
}

// GetReverseProxy returns the reverse proxy instance
func (s *Service) GetReverseProxy() *httputil.ReverseProxy {
	return s.reverseProxy
}
