package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NathanBhanji/tsnet-proxy/internal/activity"
	"github.com/NathanBhanji/tsnet-proxy/internal/config"
	"github.com/NathanBhanji/tsnet-proxy/internal/health"
	"github.com/NathanBhanji/tsnet-proxy/internal/manager"
)

func TestSameServiceConfigIgnoresRepresentationOnly(t *testing.T) {
	base := config.ServiceConfig{Name: "api", Backend: "http://api:8080"}

	// nil vs empty paths, zero vs populated-but-disabled health check / TLS
	a := base
	a.Paths = nil
	b := base
	b.Paths = []string{}
	b.HealthCheck = config.HealthCheckConfig{Enabled: false, Path: "/health", Interval: 30 * time.Second}
	b.TLS = config.TLSConfig{Enabled: false, SkipVerify: true}
	if !sameServiceConfig(a, b) {
		t.Fatal("configs that differ only in disabled/empty fields should be equal")
	}

	// a real change must be detected
	c := base
	c.Backend = "http://api:9090"
	if sameServiceConfig(a, c) {
		t.Fatal("backend change not detected")
	}
	d := base
	d.HealthCheck = config.HealthCheckConfig{Enabled: true, Path: "/health"}
	if sameServiceConfig(a, d) {
		t.Fatal("enabling health checks not detected")
	}
}

func TestRestartRequiredFields(t *testing.T) {
	running := &config.Config{AuthKey: "a", Tailnet: "t", StateDir: "/data",
		ManagementUI: config.ManagementUI{Enabled: true, Hostname: "ui", Port: 8080},
		Metrics:      config.MetricsConfig{Enabled: true, Port: 9090}}
	next := *running
	if got := restartRequiredFields(running, &next); len(got) != 0 {
		t.Fatalf("identical configs reported %v", got)
	}
	next.APIKey = "k"
	next.Metrics.Port = 9091
	next.Services = []config.ServiceConfig{{Name: "x"}} // services never require restart
	got := restartRequiredFields(running, &next)
	if len(got) != 2 || got[0] != "apiKey" || got[1] != "metrics" {
		t.Fatalf("restartRequiredFields = %v, want [apiKey metrics]", got)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "services.yaml")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Fatalf("content = %q, want new", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestServiceNameFromPath(t *testing.T) {
	for p, want := range map[string]string{
		"/api/services/grafana": "grafana",
		"/api/services/":        "",
		"/api/services":         "",
	} {
		got, ok := serviceNameFromPath(p)
		if got != want || ok != (want != "") {
			t.Errorf("serviceNameFromPath(%q) = %q,%v", p, got, ok)
		}
	}
}

func TestMaskSecret(t *testing.T) {
	if m := maskSecret("tskey-auth-kABCDEFGHIJ-1234567890abcd"); m != "tskey-auth…abcd" {
		t.Errorf("maskSecret long = %q", m)
	}
	if m := maskSecret("short"); m != "••••••••" {
		t.Errorf("maskSecret short = %q", m)
	}
	if m := maskSecret(""); m != "" {
		t.Errorf("maskSecret empty = %q", m)
	}
}

func newTestHandler(t *testing.T, yaml string) (*APIHandler, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "services.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	mgr := manager.NewManager(cfg.AuthKey, dir, "", "")
	checker := health.NewChecker(mgr)
	return NewAPIHandler(mgr, checker, cfg, path, activity.NewLog(10)), path
}

func TestConfigGetReturnsRawFile(t *testing.T) {
	raw := "authKey: \"${TS_AUTHKEY_TEST}\"\nservices: []\n"
	t.Setenv("TS_AUTHKEY_TEST", "tskey-test")
	h, _ := newTestHandler(t, raw)

	rr := httptest.NewRecorder()
	h.Config(rr, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status = %d: %s", rr.Code, rr.Body)
	}
	if rr.Body.String() != raw {
		t.Fatalf("GET body = %q, want the raw file (placeholders intact)", rr.Body.String())
	}
}

func TestConfigPutValidatesWritesAndReports(t *testing.T) {
	t.Setenv("TS_AUTHKEY_TEST", "tskey-test")
	h, path := newTestHandler(t, "authKey: \"${TS_AUTHKEY_TEST}\"\nservices: []\nmetrics:\n  enabled: true\n  port: 9090\n")

	// invalid YAML / config is rejected and the file untouched
	rr := httptest.NewRecorder()
	h.Config(rr, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader("services: []\n")))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PUT invalid status = %d, want 400: %s", rr.Code, rr.Body)
	}
	if got, _ := os.ReadFile(path); !strings.Contains(string(got), "TS_AUTHKEY_TEST") {
		t.Fatal("invalid PUT overwrote the config file")
	}

	// valid config with no services: written verbatim, metrics change flagged
	body := "authKey: \"${TS_AUTHKEY_TEST}\"\nservices: []\nmetrics:\n  enabled: true\n  port: 9191\n"
	rr = httptest.NewRecorder()
	h.Config(rr, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", rr.Code, rr.Body)
	}
	var res ApplyResult
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Status != "success" || len(res.Errors) != 0 || len(res.Added)+len(res.Removed)+len(res.Updated) != 0 {
		t.Fatalf("unexpected result %+v", res)
	}
	if len(res.RestartRequired) != 1 || res.RestartRequired[0] != "metrics" {
		t.Fatalf("restartRequired = %v, want [metrics]", res.RestartRequired)
	}
	if got, _ := os.ReadFile(path); string(got) != body {
		t.Fatalf("file = %q, want PUT body verbatim", got)
	}
}

func TestActivityEndpoint(t *testing.T) {
	t.Setenv("TS_AUTHKEY_TEST", "tskey-test")
	h, _ := newTestHandler(t, "authKey: \"${TS_AUTHKEY_TEST}\"\nservices: []\n")
	h.activity.Record("grafana", "GET", "/d/1", 200, 12*time.Millisecond)
	h.activity.Record("api", "POST", "/v1", 502, time.Millisecond)

	rr := httptest.NewRecorder()
	h.Activity(rr, httptest.NewRequest(http.MethodGet, "/api/activity?limit=1", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body)
	}
	var got []activity.Entry
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Service != "api" || got[0].Status != 502 || got[0].Method != "POST" {
		t.Fatalf("got %+v, want the newest entry only", got)
	}

	rr = httptest.NewRecorder()
	h.Activity(rr, httptest.NewRequest(http.MethodGet, "/api/activity?limit=zero", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad limit status = %d", rr.Code)
	}
}
