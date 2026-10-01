package observability_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/observability"
)

func TestLogCurator_emitsParseableRow(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	observability.LogCurator(observability.CuratorEvent{
		Tool: "read", View: "outline", Target: "pkg/a.go",
		Selected: 3, Total: 10, CacheHit: true, SessionID: "sess-1",
	})
	out := buf.String()
	for _, want := range []string{
		`msg=tool_curator`,
		`tool=read`,
		`view=outline`,
		`target=pkg/a.go`,
		`selected=3`,
		`total=10`,
		`cache_hit=true`,
		`sess-1`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("log = %q want substring %q", out, want)
		}
	}
}
