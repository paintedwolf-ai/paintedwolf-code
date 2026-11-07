package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	nativegenprovider "github.com/lycaon/lycaon/internal/llm/providers/nativegen"
)

func TestNativeDiscoveryNeverUsesHTTPFallback(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	profile := providerprofile.Profile{Discovery: providerprofile.DiscoveryNativeGeneration}
	core := &registryCore{discoveryClient: server.Client()}
	if entries, err := core.fetchProviderModels(t.Context(), profile, server.URL, ""); len(entries) != 0 || err == nil {
		t.Fatalf("absent runtime discovery: %+v, %v", entries, err)
	}
	core.localRuntime = nativegenprovider.NewRuntime(nativegenprovider.RuntimeConfig{})
	if err := core.localRuntime.Close(); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
	if entries, err := core.fetchProviderModels(t.Context(), profile, server.URL, ""); len(entries) != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("closed runtime discovery: %+v, %v", entries, err)
	}
	if entries, err := fetchDiscoveredModels(t.Context(), server.Client(), profile, server.URL, ""); len(entries) != 0 || err == nil {
		t.Fatalf("network path accepted native discovery: %+v, %v", entries, err)
	}
	if calls.Load() != 0 {
		t.Fatal("native discovery contacted an HTTP endpoint")
	}
}
