package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServerConstructorsNeverDefaultToTestCredential(t *testing.T) {
	servers := map[string]*Server{
		"normal":   NewServer(requiredTestDeps(t, Dependencies{}), nil, ""),
		"recovery": NewRecoveryServer(context.Background(), RecoveryServerOpts{DBPath: t.TempDir() + "/store.db"}),
	}
	for name, srv := range servers {
		t.Run(name, func(t *testing.T) {
			if srv.apiToken != "" {
				t.Fatalf("constructor supplied credential %q", srv.apiToken)
			}
			req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
			req.Header.Set("Authorization", TestAuthHeader())
			rec := httptest.NewRecorder()
			srv.requireClientAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unconfigured server accepted test credential") })).ServeHTTP(rec, req)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d", rec.Code)
			}
		})
	}
}
