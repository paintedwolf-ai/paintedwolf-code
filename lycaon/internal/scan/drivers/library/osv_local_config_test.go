package library

import (
	"testing"

	"github.com/lycaon/lycaon/internal/egressclass"
)

func TestOSVLocalConfigUsesTheAdvisoryClassEndpoint(t *testing.T) {
	t.Parallel()
	cfg := osvLocalConfig("/cache", true)
	if cfg.GetRemoteHost() != egressclass.OSVAdvisoryEndpoint {
		t.Fatalf("remote host = %q, want %q", cfg.GetRemoteHost(), egressclass.OSVAdvisoryEndpoint)
	}
	if cfg.GetLocalPath() != "/cache" || !cfg.GetDownload() {
		t.Fatalf("config = %+v, want the cache path with download enabled", cfg)
	}
}
