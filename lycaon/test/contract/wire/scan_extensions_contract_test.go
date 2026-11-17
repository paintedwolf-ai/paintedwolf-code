package contract

import (
	"net/http"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestListScannersOpenAPIRoute(t *testing.T) {
	t.Parallel()
	srv := NewStubServer()
	t.Cleanup(srv.Close)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/v1/scanners", nil)
	resp, err := srv.Client().Do(req)
	contractcheck.FailErr(t, "srv.Client failed", err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
