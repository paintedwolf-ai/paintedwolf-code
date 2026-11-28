package command_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

type launchLogCapture struct {
	mu      sync.Mutex
	records []map[string]string
}

func (c *launchLogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *launchLogCapture) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *launchLogCapture) WithGroup(string) slog.Handler            { return c }
func (c *launchLogCapture) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "command external launch" {
		return nil
	}
	attrs := map[string]string{}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	c.mu.Lock()
	c.records = append(c.records, attrs)
	c.mu.Unlock()
	return nil
}

func (c *launchLogCapture) snapshot() []map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]map[string]string, len(c.records))
	copy(out, c.records)
	return out
}

func TestExternalLaunchDiskProvenanceControls(t *testing.T) {
	t.Run("capture_off_default_emits_nothing", func(t *testing.T) {
		t.Setenv(configdir.EnvDev, "")
		t.Setenv(configdir.EnvHarness, "")
		capture := &launchLogCapture{}
		prev := slog.Default()
		slog.SetDefault(slog.New(capture))
		t.Cleanup(func() { slog.SetDefault(prev) })

		f := newProgramFixture(t)
		raw, err := f.call(t, t.Context(), `external -- /bin/echo default-ok`)
		testutil.FailErr(t, "run external echo with capture off", err)
		if outcome := decodeOutcome(t, raw); outcome.Status != 0 || outcome.Refusal != nil || outcome.Stdout != "default-ok\n" {
			t.Fatalf("outcome = %+v", outcome)
		}
		if got := capture.snapshot(); len(got) != 0 {
			t.Fatalf("capture-off emitted %d provenance records, want 0: %+v", len(got), got)
		}
	})

	t.Run("capture_on_started_edge_matches_independent_disk_digest", func(t *testing.T) {
		t.Setenv(configdir.EnvDev, "1")
		t.Setenv(configdir.EnvHarness, "1")
		capture := &launchLogCapture{}
		prev := slog.Default()
		slog.SetDefault(slog.New(capture))
		t.Cleanup(func() { slog.SetDefault(prev) })

		f := newProgramFixture(t)
		raw, err := f.call(t, t.Context(), `external -- /bin/echo provenance-ok`)
		testutil.FailErr(t, "run external echo", err)
		outcome := decodeOutcome(t, raw)
		if outcome.Status != 0 || outcome.Refusal != nil || outcome.Stdout != "provenance-ok\n" {
			t.Fatalf("outcome = %+v", outcome)
		}

		page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, outcome.ProgramID, "", 20)
		testutil.FailErr(t, "read program page", err)
		var execID string
		for _, exec := range page.Executions {
			if strings.Contains(exec.Command, "/bin/echo") {
				execID = exec.ID
			}
		}
		if execID == "" {
			t.Fatalf("missing /bin/echo execution in %+v", page.Executions)
		}

		recs := capture.snapshot()
		if len(recs) != 1 {
			t.Fatalf("got %d launch provenance records, want 1: %+v", len(recs), recs)
		}
		rec := recs[0]
		if rec["execution_id"] != execID || rec["executable_path"] == "" || rec["executable_sha256_status"] != "ok" || rec["executable_provenance_basis"] != "post_start_path_sample" {
			t.Fatalf("unexpected provenance record: %+v", rec)
		}
		resolved, err := filepath.EvalSymlinks(rec["executable_path"])
		testutil.FailErr(t, "resolve executable_path", err)
		body, err := os.ReadFile(resolved)
		testutil.FailErr(t, "read executable bytes", err)
		sum := sha256.Sum256(body)
		if want := hex.EncodeToString(sum[:]); rec["executable_sha256"] != want {
			t.Fatalf("executable_sha256 = %q, want %q", rec["executable_sha256"], want)
		}
	})

	t.Run("refused_or_missing_start_emits_no_launch_provenance", func(t *testing.T) {
		t.Setenv(configdir.EnvDev, "1")
		t.Setenv(configdir.EnvHarness, "1")
		capture := &launchLogCapture{}
		prev := slog.Default()
		slog.SetDefault(slog.New(capture))
		t.Cleanup(func() { slog.SetDefault(prev) })

		f := newProgramFixture(t)
		rawMissing, err := f.call(t, t.Context(), `external -- /no/such/binary-bb09-provenance`)
		testutil.FailErr(t, "run missing external", err)
		if got := decodeOutcome(t, rawMissing); got.Status != 127 {
			t.Fatalf("missing binary outcome = %+v, want status 127", got)
		}

		fRefused := newProgramFixture(t)
		fRefused.Context = func(_ string, tc *tools.ToolContext) {
			tc.Source.RecordViewProcess = func(int) error { return errors.New("injected start refusal") }
		}
		_, _ = fRefused.call(t, t.Context(), `external -- /bin/echo refused-start`)
		if got := capture.snapshot(); len(got) != 0 {
			t.Fatalf("refused/missing starts emitted provenance: %+v", got)
		}
	})
}
