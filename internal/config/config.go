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
	return Parse(data)
}

// Parse parses raw YAML configuration (as found on disk), expanding
// environment variables and applying validation and defaults.
func Parse(data []byte) (*Config, error) {
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

// SaveService changes only one service in the unexpanded file. Serializing the
// running Config would copy environment-provided credentials into the file.
// A nil service removes the named service.
func SaveService(path, name string, service *ServiceConfig) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("config must be a YAML mapping")
	}
	root := doc.Content[0]
	var services, servicesKey *yaml.Node
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "services" {
			services = root.Content[i+1]
			servicesKey = root.Content[i]
			break
		}
	}
	if services == nil {
		services = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "services"}, services)
	}
	// Parse accepts YAML null (including a bare services:) as an empty list.
	// Normalize in place so comments and the rest of the raw document survive.
	if services.Kind == yaml.ScalarNode && services.Tag == "!!null" {
		// Collection line comments are not reliably emitted by yaml.v3,
		// especially for empty sequences. Keep them above the mapping key.
		for _, node := range []*yaml.Node{servicesKey, services} {
			if node.LineComment != "" {
				if servicesKey.HeadComment != "" {
					servicesKey.HeadComment += "\n"
				}
				servicesKey.HeadComment += node.LineComment
				node.LineComment = ""
			}
		}
		services.Kind = yaml.SequenceNode
		services.Tag = "!!seq"
		services.Value = ""
		services.Style = 0
	}
	if services.Kind != yaml.SequenceNode {
		return fmt.Errorf("services must be a YAML sequence")
	}
	kept := make([]*yaml.Node, 0, len(services.Content)+1)
	for _, node := range services.Content {
		var entry struct {
			Name string `yaml:"name"`
		}
		if err := node.Decode(&entry); err != nil {
			return err
		}
		if os.ExpandEnv(entry.Name) != name {
			kept = append(kept, node)
		}
	}
	if service != nil {
		var node yaml.Node
		if err := node.Encode(service); err != nil {
			return err
		}
		kept = append(kept, &node)
	}
	services.Content = kept
	data, err = yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	// Restrict existing files too; WriteFile's mode only applies to new files.
	if err := os.Chmod(path, 0600); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
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
