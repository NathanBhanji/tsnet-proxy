package config

import "time"

// Config represents the main configuration structure
type Config struct {
	Services      []ServiceConfig `yaml:"services"`
	AuthKey       string          `yaml:"authKey"`
	APIKey        string          `yaml:"apiKey"`        // Tailscale API key for device deletion
	Tailnet       string          `yaml:"tailnet"`       // Tailnet name (e.g., example.com)
	StateDir      string          `yaml:"stateDir"`
	ManagementUI  ManagementUI    `yaml:"managementUI"`
	Metrics       MetricsConfig   `yaml:"metrics"`
}

// ServiceConfig represents a single service configuration
type ServiceConfig struct {
	Name        string            `yaml:"name"`
	Backend     string            `yaml:"backend"`
	Paths       []string          `yaml:"paths"`
	StripPrefix bool              `yaml:"stripPrefix"`
	HealthCheck HealthCheckConfig `yaml:"healthCheck"`
	TLS         TLSConfig         `yaml:"tls"`
}

// HealthCheckConfig represents health check settings
type HealthCheckConfig struct {
	Enabled            bool          `yaml:"enabled"`
	Path               string        `yaml:"path"`
	Interval           time.Duration `yaml:"interval"`
	Timeout            time.Duration `yaml:"timeout"`
	UnhealthyThreshold int           `yaml:"unhealthyThreshold"`
}

// TLSConfig represents TLS settings for backend connections
type TLSConfig struct {
	Enabled    bool `yaml:"enabled"`
	SkipVerify bool `yaml:"skipVerify"`
}

// ManagementUI represents the management UI configuration
type ManagementUI struct {
	Enabled  bool   `yaml:"enabled"`
	Hostname string `yaml:"hostname"`
	Port     int    `yaml:"port"`
}

// MetricsConfig represents metrics configuration
type MetricsConfig struct {
	Enabled bool `yaml:"enabled"`
	Port    int  `yaml:"port"`
}
