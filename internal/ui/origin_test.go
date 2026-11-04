package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestManagementRejectsCrossOriginMutations(t *testing.T) {
	h, path := newTestHandler(t, "authKey: test-key\nservices: []\n")
	s := &UIServer{apiHandler: h}
	handler := s.handler()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		target := "/api/services"
		body := `{"name":"unwanted","backend":"http://internal-admin:8080"}`
		if method == http.MethodPut {
			target = "/api/config"
			body = "authKey: attacker-key\nservices: []\n"
		}
		if method == http.MethodDelete {
			target += "/existing"
		}
		for _, fetchSite := range []string{"", "cross-site", "same-site"} {
			t.Run(method+"/"+fetchSite, func(t *testing.T) {
				req := httptest.NewRequest(method, "http://proxy.example.ts.net"+target, strings.NewReader(body))
				// text/plain is a CORS-safelisted content type: a hostile page can send
				// this JSON POST without the server authorizing a preflight.
				req.Header.Set("Content-Type", "text/plain")
				req.Header.Set("Origin", "http://attacker.example")
				if fetchSite != "" {
					req.Header.Set("Sec-Fetch-Site", fetchSite)
				}
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, req)
				if rr.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403", rr.Code)
				}
			})
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("cross-origin request changed config")
	}
	if len(h.manager.GetAllServices()) != 0 {
		t.Fatal("cross-origin request changed services")
	}
}

func TestManagementAllowsSameOriginAndCLI(t *testing.T) {
	for _, origin := range []string{"", "http://proxy.example.ts.net"} {
		t.Run(origin, func(t *testing.T) {
			h, path := newTestHandler(t, "authKey: test-key\nservices: []\n")
			handler := (&UIServer{apiHandler: h}).handler()
			body := "authKey: test-key\nservices: []\nmetrics:\n  port: 9191\n"
			req := httptest.NewRequest(http.MethodPut, "http://proxy.example.ts.net/api/config", strings.NewReader(body))
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rr.Code, rr.Body)
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(saved) != body {
				t.Fatal("authorized update was not saved")
			}
		})
	}
}
