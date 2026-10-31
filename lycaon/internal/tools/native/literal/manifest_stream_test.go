package literal

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestChecksumManifestPinsManyTargetsWithAdmittedDedupAndCleanup(t *testing.T) {
	files := map[string]string{}
	var manifest strings.Builder
	for i := range 100 {
		name := fmt.Sprintf("target-%03d", i)
		files[name] = "x"
		fmt.Fprintf(&manifest, "%s  %s\n", textfile.SHA256([]byte("x")), name)
	}
	fmt.Fprintf(&manifest, "%s  target-000\n", textfile.SHA256([]byte("x")))
	files["manifest"] = manifest.String()
	for _, state := range []int64{2 << 20, 160 << 10} {
		h := newHarness(t, files)
		memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 2 << 20, State: state})
		testutil.FailErr(t, "toolcontract.NewProgramMemory failed", err)
		h.owners.Revisions.Memory = memory
		result := h.exec("", "sha256sum", "-c", "manifest")
		if state < 1<<20 {
			result.refused(t, "pin metadata admission", "COMMAND_LIMIT_EXCEEDED")
			reject := toolrejection.AsToolReject(result.err)
			if reject.Data["limit_id"] != "program_state_bytes" {
				t.Fatalf("wrong account %+v", reject)
			}
			if strings.Count(result.stdout, ": OK\n") >= 101 {
				t.Fatal("refused capture still produced success")
			}
		} else {
			if result.err != nil || result.result.Status != 0 || strings.Count(result.stdout, ": OK\n") != 101 {
				t.Fatalf("manifest %q %v", result.stdout, result.err)
			}
			pins := h.pins.pins[result.execution]
			if len(pins) != 101 {
				t.Fatalf("pins %d; want manifest +100 distinct identities, repeated target deduplicated", len(pins))
			}
		}
		if held := memory.InUse(); held != (toolcontract.ProgramMemoryUsage{}) {
			t.Fatalf("pin/record leases remain %+v", held)
		}
	}
}
