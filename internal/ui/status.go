package ui

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

// statusTimeout bounds how long we wait on each tsnet server's local status.
const statusTimeout = 3 * time.Second

// SettingsResponse is the masked, read-only view of the running configuration
// served at GET /api/settings.
type SettingsResponse struct {
	// Tailnet is the MagicDNS suffix (e.g. "tail1234.ts.net") reported by the
	// UI's own tsnet node — the suffix service hostnames resolve under. Falls
	// back to the configured tailnet name if the node isn't connected yet.
	Tailnet string `json:"tailnet"`
	// TailnetName is the tailnet/org name from the config (used for the API).
	TailnetName   string `json:"tailnetName"`
	AuthKeyMasked string `json:"authKeyMasked"`
	APIKeySet     bool   `json:"apiKeySet"`
	APIKeyMasked  string `json:"apiKeyMasked,omitempty"`
	StateDir      string `json:"stateDir"`
	ConfigPath    string `json:"configPath"`
	BackendState  string `json:"backendState"`
	ManagementUI  struct {
		Enabled  bool   `json:"enabled"`
		Hostname string `json:"hostname"`
		Port     int    `json:"port"`
		URL      string `json:"url,omitempty"`
	} `json:"managementUI"`
	Metrics struct {
		Enabled bool `json:"enabled"`
		Port    int  `json:"port"`
	} `json:"metrics"`
}

// handleSettings serves GET /api/settings.
func (s *UIServer) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cfg := s.config
	resp := SettingsResponse{
		Tailnet:       cfg.Tailnet,
		TailnetName:   cfg.Tailnet,
		AuthKeyMasked: maskSecret(cfg.AuthKey),
		APIKeySet:     cfg.APIKey != "",
		APIKeyMasked:  maskSecret(cfg.APIKey),
		StateDir:      cfg.StateDir,
		ConfigPath:    s.configPath,
	}
	resp.ManagementUI.Enabled = cfg.ManagementUI.Enabled
	resp.ManagementUI.Hostname = cfg.ManagementUI.Hostname
	resp.ManagementUI.Port = cfg.ManagementUI.Port
	resp.Metrics.Enabled = cfg.Metrics.Enabled
	resp.Metrics.Port = cfg.Metrics.Port

	ctx, cancel := context.WithTimeout(r.Context(), statusTimeout)
	defer cancel()
	if st, err := tsnetStatus(ctx, s.tsnetServer); err == nil && st != nil {
		resp.BackendState = st.BackendState
		if st.CurrentTailnet != nil && st.CurrentTailnet.MagicDNSSuffix != "" {
			resp.Tailnet = st.CurrentTailnet.MagicDNSSuffix
		}
		if st.Self != nil && st.Self.DNSName != "" {
			resp.ManagementUI.URL = "http://" + trimDot(st.Self.DNSName)
		}
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

// DeviceResponse is one tsnet node owned by this proxy (the UI or a service).
type DeviceResponse struct {
	Name     string     `json:"name"`
	Role     string     `json:"role"` // "ui" or "service"
	DNSName  string     `json:"dnsName,omitempty"`
	IP       string     `json:"ip,omitempty"`
	IPv6     string     `json:"ipv6,omitempty"`
	Online   bool       `json:"online"`
	State    string     `json:"state,omitempty"` // ipn backend state, e.g. Running
	LastSeen *time.Time `json:"lastSeen,omitempty"`
	Error    string     `json:"error,omitempty"`
}

// handleDevices serves GET /api/devices: the tailnet devices this proxy runs,
// read from each embedded tsnet node's local status (no API key needed).
func (s *UIServer) handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	type node struct {
		name, role string
		srv        *tsnet.Server
	}
	nodes := []node{{name: s.config.ManagementUI.Hostname, role: "ui", srv: s.tsnetServer}}
	for name, svc := range s.manager.GetAllServices() {
		nodes = append(nodes, node{name: name, role: "service", srv: svc.GetTsnetServer()})
	}

	ctx, cancel := context.WithTimeout(r.Context(), statusTimeout)
	defer cancel()

	devices := make([]DeviceResponse, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Add(1)
		go func(i int, n node) {
			defer wg.Done()
			d := DeviceResponse{Name: n.name, Role: n.role}
			st, err := tsnetStatus(ctx, n.srv)
			switch {
			case err != nil:
				d.Error = err.Error()
			case st == nil || st.Self == nil:
				d.State = "unknown"
			default:
				d.State = st.BackendState
				d.Online = st.Self.Online
				d.DNSName = trimDot(st.Self.DNSName)
				for _, ip := range st.Self.TailscaleIPs {
					if ip.Is4() && d.IP == "" {
						d.IP = ip.String()
					} else if ip.Is6() && d.IPv6 == "" {
						d.IPv6 = ip.String()
					}
				}
				if !st.Self.LastSeen.IsZero() {
					t := st.Self.LastSeen
					d.LastSeen = &t
				}
			}
			devices[i] = d
		}(i, n)
	}
	wg.Wait()

	sort.Slice(devices, func(i, j int) bool {
		if devices[i].Role != devices[j].Role {
			return devices[i].Role == "ui" // UI device first, then services
		}
		return devices[i].Name < devices[j].Name
	})

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, devices)
}

// tsnetStatus returns the local status of a tsnet server, or an error if the
// server is nil or not started.
func tsnetStatus(ctx context.Context, srv *tsnet.Server) (*ipnstate.Status, error) {
	if srv == nil {
		return nil, errNotStarted
	}
	lc, err := srv.LocalClient()
	if err != nil {
		return nil, err
	}
	return lc.Status(ctx)
}

type notStartedError struct{}

func (notStartedError) Error() string { return "tsnet server not started" }

var errNotStarted error = notStartedError{}

// maskSecret hides all but a recognisable prefix and the last four characters
// of a key, e.g. "tskey-auth-k…Ab3x". Short values are fully masked.
func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) > 16 {
		return s[:10] + "…" + s[len(s)-4:]
	}
	return "••••••••"
}

func trimDot(s string) string {
	if len(s) > 0 && s[len(s)-1] == '.' {
		return s[:len(s)-1]
	}
	return s
}
