package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads and parses the configuration file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Expand environment variables
	expanded := os.ExpandEnv(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, nil
}

// Save writes the configuration to a file
func Save(cfg *Config, path string) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.AuthKey == "" {
		return fmt.Errorf("authKey is required")
	}

	if c.StateDir == "" {
		c.StateDir = "/data/tsnet"
	}

	// Validate services
	serviceNames := make(map[string]bool)
	for i, svc := range c.Services {
		if svc.Name == "" {
			return fmt.Errorf("service %d: name is required", i)
		}

		if serviceNames[svc.Name] {
			return fmt.Errorf("duplicate service name: %s", svc.Name)
		}
		serviceNames[svc.Name] = true

		if svc.Backend == "" {
			return fmt.Errorf("service %s: backend URL is required", svc.Name)
		}

		if !strings.HasPrefix(svc.Backend, "http://") && !strings.HasPrefix(svc.Backend, "https://") {
			return fmt.Errorf("service %s: backend URL must start with http:// or https://", svc.Name)
		}

		// Validate health check
		if svc.HealthCheck.Enabled {
			if svc.HealthCheck.Path == "" {
				return fmt.Errorf("service %s: healthCheck.path is required when health checks are enabled", svc.Name)
			}
			if svc.HealthCheck.Interval == 0 {
				c.Services[i].HealthCheck.Interval = 30 * 1000000000 // 30s default
			}
			if svc.HealthCheck.Timeout == 0 {
				c.Services[i].HealthCheck.Timeout = 5 * 1000000000 // 5s default
			}
			if svc.HealthCheck.UnhealthyThreshold == 0 {
				c.Services[i].HealthCheck.UnhealthyThreshold = 3
			}
		}
	}

	// Set defaults for management UI
	if c.ManagementUI.Enabled && c.ManagementUI.Hostname == "" {
		c.ManagementUI.Hostname = "tsnet-proxy-ui"
	}
	if c.ManagementUI.Enabled && c.ManagementUI.Port == 0 {
		c.ManagementUI.Port = 8080
	}

	// Set defaults for metrics
	if c.Metrics.Enabled && c.Metrics.Port == 0 {
		c.Metrics.Port = 9090
	}

	return nil
}
