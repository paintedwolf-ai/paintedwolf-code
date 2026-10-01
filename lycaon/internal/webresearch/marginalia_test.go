package webresearch

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProviderMarginaliaResponseEnvelope(t *testing.T) {
	body := []byte(`{"query":"query","license":"CC-BY-NC-SA 4.0","results":[{"url":"https://example.com/marginalia","title":"Hit","description":"snippet"}]}`)
	hits, err := parseMarginaliaHits(body)
	testutil.FailErr(t, "parse Marginalia response", err)
	if len(hits) != 1 || hits[0].Provider != "marginalia" || hits[0].URL != "https://example.com/marginalia" {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestProviderMarginaliaRejectsBareArray(t *testing.T) {
	_, err := parseMarginaliaHits([]byte(`[{"url":"https://example.com/marginalia","title":"Hit","description":"snippet"}]`))
	if err == nil {
		t.Fatal("expected bare array response to fail")
	}
}
