package mcp

import (
	"net/http"
	"testing"
)

// recordingRT captures the headers the transport actually put on the wire.
type recordingRT struct{ last http.Header }

func (r *recordingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.last = req.Header.Clone()
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
}

// Credentials inject at the transport, so a cross-host redirect must not receive them.
func TestHeaderRoundTripperGatesAuthByHost(t *testing.T) {
	rec := &recordingRT{}
	rt := &headerRoundTripper{
		base:     rec,
		headers:  http.Header{"X-Api-Key": []string{"key-secret"}},
		bearer:   "tok-secret",
		authHost: "good.example",
	}

	sameHost, err := http.NewRequest(http.MethodPost, "https://good.example:8443/mcp", nil)
	if err != nil {
		t.Fatalf("new same-host request: %v", err)
	}
	resp, err := rt.RoundTrip(sameHost)
	if err != nil {
		t.Fatalf("same-host round trip: %v", err)
	}
	_ = resp.Body.Close()
	if got := rec.last.Get("Authorization"); got != "Bearer tok-secret" {
		t.Fatalf("same host must carry bearer, got %q", got)
	}
	if got := rec.last.Get("X-Api-Key"); got != "key-secret" {
		t.Fatalf("same host must carry custom header, got %q", got)
	}

	crossHost, err := http.NewRequest(http.MethodPost, "https://attacker.example/steal", nil)
	if err != nil {
		t.Fatalf("new cross-host request: %v", err)
	}
	resp, err = rt.RoundTrip(crossHost)
	if err != nil {
		t.Fatalf("cross-host round trip: %v", err)
	}
	_ = resp.Body.Close()
	if got := rec.last.Get("Authorization"); got != "" {
		t.Fatalf("cross host must strip bearer, got %q", got)
	}
	if got := rec.last.Get("X-Api-Key"); got != "" {
		t.Fatalf("cross host must strip custom header, got %q", got)
	}
}
