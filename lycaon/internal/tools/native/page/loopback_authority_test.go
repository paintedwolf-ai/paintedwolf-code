package page

import (
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
)

// A page target on this machine takes the same reviewed grant http_request takes
// for the same reach. Without one the tool refuses and names the port, so the
// model can request exactly that capability instead of quietly rendering a local
// service into the transcript.
func TestURLModeRequiresTheLoopbackConnectGrant(t *testing.T) {
	t.Parallel()
	ungranted := tools.ToolContext{}
	for _, target := range []string{
		"http://127.0.0.1:11434/api/tags",
		"http://localhost:9200/_search",
		"https://127.0.0.1:8443/",
		"http://[::1]:3000/",
	} {
		err := requireLoopbackAuthority(target, ungranted)
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "SANDBOX_TRY_LOOPBACK_CONNECT" {
			t.Fatalf("ungranted %s: err=%v, want SANDBOX_TRY_LOOPBACK_CONNECT", target, err)
		}
		if reject.Data["port"] == nil {
			t.Fatalf("refusal for %s must name the port", target)
		}
	}
}

// The grant is the reviewed one, ports included.
func TestLoopbackGrantCoversItsReviewedPortsOnly(t *testing.T) {
	t.Parallel()
	narrowed := tools.ToolContext{LoopbackConnectGranted: true, LoopbackConnectPorts: []uint16{3000}}
	if err := requireLoopbackAuthority("http://127.0.0.1:3000/", narrowed); err != nil {
		t.Fatalf("reviewed port refused: %v", err)
	}
	if err := requireLoopbackAuthority("http://127.0.0.1:11434/api/tags", narrowed); err == nil {
		t.Fatal("a port outside the reviewed grant was allowed")
	}
	unnarrowed := tools.ToolContext{LoopbackConnectGranted: true}
	if err := requireLoopbackAuthority("http://127.0.0.1:11434/api/tags", unnarrowed); err != nil {
		t.Fatalf("unnarrowed grant refused a local port: %v", err)
	}
}

// project_dir captures serve from disk through the session's fetch hijack and
// open no socket, so they stay quiet. A non-local URL is refused by the capture
// path itself and is not this axis's decision.
func TestNonSocketTargetsNeedNoLocalGrant(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"", "   ", "https://example.com/docs", "file:///etc/passwd"} {
		if err := requireLoopbackAuthority(target, tools.ToolContext{}); err != nil {
			t.Fatalf("target %q must not need a local-network grant: %v", target, err)
		}
	}
}
