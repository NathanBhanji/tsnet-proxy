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
