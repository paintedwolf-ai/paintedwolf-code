package observability

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestRuntimeDescriptorsRemainObservableWhenNondumpable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "performance.jsonl")
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRuntimeDescriptorsNondumpableHelper$")
	command.Env = append(os.Environ(), "PW_PERF_DESCRIPTORS_HELPER=1", "LYCAON_PERF_DEBUG=1", "LYCAON_PERF_DEBUG_FILE="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("nondumpable resource helper: %v\n%s", err, output)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		testutil.FailErr(t, "read nondumpable process runtime sample", err)
	}
	var record PerformanceRecord
	if err := json.Unmarshal(data, &record); err != nil {
		testutil.FailErr(t, "decode nondumpable process runtime sample", err)
	}
	if record.Kind != "runtime" || record.Runtime == nil || record.Runtime.Gauges["process_fds"] <= 0 {
		t.Fatalf("nondumpable process omitted its positive descriptor measurement: %+v", record.Runtime)
	}
}

func TestRuntimeDescriptorsNondumpableHelper(t *testing.T) {
	if os.Getenv("PW_PERF_DESCRIPTORS_HELPER") != "1" {
		return
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		testutil.FailErr(t, "protect isolated helper memory", err)
	}
	value, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil {
		testutil.FailErr(t, "observe isolated helper memory protection", err)
	}
	if value != 0 {
		t.Fatalf("isolated resource helper dumpability = %d, want 0", value)
	}
	RecordRuntimeSample(nil)
	closePerformance()
}
