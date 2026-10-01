package webresearch

import (
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// skipUnlessLiveDirectFetch gates network-dependent tests.
// Run with LYCAON_LIVE_DIRECT_FETCH=1:
// ./task test:digest -- ./internal/webresearch -run DirectLive -count=1 -timeout=5m
func skipUnlessLiveDirectFetch(t *testing.T) {
	t.Helper()
	testutil.SkipIfShort(t, "live public fetch")
	if os.Getenv("LYCAON_LIVE_DIRECT_FETCH") == "" {
		t.Skip("set LYCAON_LIVE_DIRECT_FETCH=1 to run")
	}
}
