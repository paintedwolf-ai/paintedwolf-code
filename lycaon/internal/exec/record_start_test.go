package exec

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// The target proves it ran by creating a file. The launch helper holds it until the record is
// durable, so the record always sees no file, and a refused record means the target never runs.
func TestRecordStartHoldsTheTargetUntilTheProcessIsNamed(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	var recorded []int
	plan := HostLaunch("record start")
	plan.RecordStart = func(pid int) error {
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("target ran before its record: %v", err)
		}
		recorded = append(recorded, pid)
		return nil
	}
	started := 0
	_, code, err := Run(t.Context(), "/bin/sh", []string{"-c", `: > "$0"`, marker}, ExecOpts{
		Launch: plan, Started: func(int) { started++ },
	})
	testutil.FailErr(t, "run", err)
	if code != 0 || len(recorded) != 1 || recorded[0] <= 0 || started != 1 {
		t.Fatalf("code=%d recorded=%v started=%d", code, recorded, started)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("target did not run after its record: %v", err)
	}
}

func TestRefusedRecordKillsTheProcessBeforeItsTargetRuns(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	refused := errors.New("no record")
	plan := HostLaunch("record start")
	plan.RecordStart = func(int) error { return refused }

	_, _, err := Run(t.Context(), "/bin/sh", []string{"-c", `: > "$0"`, marker}, ExecOpts{Launch: plan})
	if !errors.Is(err, refused) {
		t.Fatalf("run = %v, want the record's refusal", err)
	}
	_, err = StartPTY(t.Context(), "/bin/sh", []string{"-c", `: > "$0"`, marker}, PTYOpts{Launch: plan})
	if !errors.Is(err, refused) {
		t.Fatalf("pty = %v, want the record's refusal", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target ran without a record: %v", err)
	}
}
