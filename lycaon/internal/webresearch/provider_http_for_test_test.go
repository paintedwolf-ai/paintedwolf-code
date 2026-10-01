package webresearch

import (
	"net/http"
	"testing"
)

func TestSetProviderHTTPClientForTestRequiresLYCAONTest(t *testing.T) {
	t.Setenv("LYCAON_TEST", "")
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when LYCAON_TEST is unset")
		}
	}()
	SetProviderHTTPClientForTest(&http.Client{})
}

func TestSetProviderHTTPClientForTestInstallsAndClears(t *testing.T) {
	t.Setenv("LYCAON_TEST", "1")
	prev := providerHTTPClient
	t.Cleanup(func() { providerHTTPClient = prev })

	c := &http.Client{}
	SetProviderHTTPClientForTest(c)
	if providerHTTPClient != c {
		t.Fatal("client not installed")
	}
	SetProviderHTTPClientForTest(nil)
	if providerHTTPClient != nil {
		t.Fatal("client not cleared")
	}
}
