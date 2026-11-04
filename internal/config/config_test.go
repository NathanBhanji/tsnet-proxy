package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveServiceDoesNotPersistEnvironmentSecrets(t *testing.T) {
	t.Setenv("TEST_AUTH", "dummy-auth-secret")
	t.Setenv("TEST_API", "dummy-api-secret")
	t.Setenv("TEST_BACKEND", "http://user:dummy-password@internal:8080")
	t.Setenv("TEST_EXISTING_NAME", "existing")
	raw := "authKey: ${TEST_AUTH}\napiKey: ${TEST_API}\nservices:\n  - name: ${TEST_EXISTING_NAME}\n    backend: ${TEST_BACKEND}\n"
	path := filepath.Join(t.TempDir(), "services.yaml")
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	added := ServiceConfig{Name: "added", Backend: "http://added:8080"}
	if err := SaveService(path, added.Name, &added); err != nil {
		t.Fatal(err)
	}
	assertPrivateConfig := func() string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"dummy-auth-secret", "dummy-api-secret", "dummy-password"} {
			if strings.Contains(string(data), secret) {
				t.Fatal("environment secret persisted")
			}
		}
		for _, placeholder := range []string{"${TEST_AUTH}", "${TEST_API}"} {
			if !strings.Contains(string(data), placeholder) {
				t.Fatalf("lost placeholder %s", placeholder)
			}
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("config mode = %o", info.Mode().Perm())
		}
		return string(data)
	}
	after := assertPrivateConfig()
	if !strings.Contains(after, "${TEST_BACKEND}") {
		t.Fatal("existing service placeholder lost")
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Services) != 2 || cfg.Services[1].Name != "added" {
		t.Fatal("add did not persist")
	}
	if err := SaveService(path, "existing", nil); err != nil {
		t.Fatal(err)
	}
	assertPrivateConfig()
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Services) != 1 || cfg.Services[0].Name != "added" {
		t.Fatal("remove did not match expanded name")
	}
}

func TestSaveServiceWithNullServices(t *testing.T) {
	t.Setenv("NULL_TEST_AUTH", "dummy-null-auth-secret")
	t.Setenv("NULL_TEST_API", "dummy-null-api-secret")
	for _, services := range []string{"services: null", "services:"} {
		t.Run(services, func(t *testing.T) {
			raw := "# keep this comment\nauthKey: ${NULL_TEST_AUTH}\napiKey: ${NULL_TEST_API}\nmetrics:\n  enabled: true\n  port: 9191\n" + services + " # empty services\n"
			path := filepath.Join(t.TempDir(), "services.yaml")
			if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil || len(cfg.Services) != 0 {
				t.Fatalf("initial load = %+v, %v", cfg, err)
			}
			// Deleting from null must also produce a valid empty sequence.
			if err := SaveService(path, "missing", nil); err != nil {
				t.Fatal(err)
			}
			cfg, err = Load(path)
			if err != nil || len(cfg.Services) != 0 {
				t.Fatalf("empty reload = %+v, %v", cfg, err)
			}
			// Restore the null input to exercise adding directly to each null style.
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			added := ServiceConfig{Name: "added", Backend: "http://added:8080"}
			if err := SaveService(path, added.Name, &added); err != nil {
				t.Fatal(err)
			}
			cfg, err = Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.Services) != 1 || cfg.Services[0].Name != added.Name || cfg.Services[0].Backend != added.Backend {
				t.Fatalf("added service lost on reload: %+v", cfg.Services)
			}
			if cfg.Metrics.Port != 9191 || !cfg.Metrics.Enabled {
				t.Fatalf("metrics changed: %+v", cfg.Metrics)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"${NULL_TEST_AUTH}", "${NULL_TEST_API}", "# keep this comment", "# empty services"} {
				if !strings.Contains(string(data), want) {
					t.Fatalf("lost %q in %s", want, data)
				}
			}
			for _, secret := range []string{cfg.AuthKey, cfg.APIKey} {
				if strings.Contains(string(data), secret) {
					t.Fatal("expanded secret persisted")
				}
			}
		})
	}
}
