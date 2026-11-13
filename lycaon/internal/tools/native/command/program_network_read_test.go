package command

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
)

// Only destinations beyond this machine that a stage's processes actually
// connected to are network reads.
func TestReachedNetworkURLsAreConnectedRemoteDestinations(t *testing.T) {
	got := reachedNetworkURLs([]confine.EgressHost{
		{Host: "pypi.example.test", Port: 443, Transport: "http_connect", Allowed: true, Attempts: 2},
		{Host: "api.example.test", Port: 80, Transport: "http_request", RequestPath: "/v1", Allowed: true, Attempts: 1},
		{Host: "db.example.test", Port: 5432, Transport: "socks_tcp", Allowed: true, Attempts: 1},
		{Host: "denied.example.test", Port: 443, Transport: "http_connect", Allowed: false, Attempts: 1},
		{Host: "127.0.0.1", Port: 8080, Transport: "http_request", Allowed: true, Attempts: 1},
		{Host: "never.example.test", Port: 443, Transport: "http_connect", Allowed: true},
	})
	want := []string{"https://pypi.example.test:443", "http://api.example.test:80/v1", "tcp://db.example.test:5432"}
	if !slices.Equal(got, want) {
		t.Fatalf("reached = %v, want %v", got, want)
	}
}

// What an external process read from the network marks the stream it wrote
// and the file its output was redirected to, and nothing it did not write.
func TestNetworkReadsMarkOnlyWrittenStreams(t *testing.T) {
	root := t.TempDir()
	run := &programRun{tc: tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Path: root, IsPrimary: true}}}}}
	urls := []string{"https://pypi.example.test:443"}

	quiet := run.beginOpTracker()
	quiet.addNetworkRead(urls)
	run.finishOpTracker(t.Context(), quiet, outputStreamClass{topLevel: true}, false, outputStreamClass{topLevel: true}, false)
	if run.provenance.directExternalOutput {
		t.Fatal("a stage that wrote nothing marked the output")
	}

	saved := run.normalizeProvenancePath("", filepath.Join(root, "out.txt"))
	redirected := run.beginOpTracker()
	redirected.addNetworkRead(urls)
	run.finishOpTracker(t.Context(), redirected, outputStreamClass{redirectPath: saved, redirectOperator: ">"}, true, outputStreamClass{}, false)
	if got := run.provenance.custodyFiles[saved]; !slices.Equal(got, urls) {
		t.Fatalf("redirected output custody = %v", got)
	}

	direct := run.beginOpTracker()
	direct.addNetworkRead(urls)
	run.finishOpTracker(t.Context(), direct, outputStreamClass{topLevel: true}, true, outputStreamClass{}, false)
	if !run.provenance.directExternalOutput || !slices.Equal(run.provenance.externalURLs, urls) {
		t.Fatalf("written output not marked: %+v", run.provenance.externalURLs)
	}
}
