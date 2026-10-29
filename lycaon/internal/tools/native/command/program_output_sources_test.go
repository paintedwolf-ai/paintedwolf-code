package command

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestOptionalSourceCaptureAdmitsAndReleasesMemory(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(root, "file"), []byte("source\n"), 0600))
	for _, limit := range []int64{1024, 1 << 20} {
		memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: limit, State: 1 << 20})
		testutil.FailErr(t, "memory owner", err)
		run := &programRun{memory: memory, tc: tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Path: root}}}}}
		pin := toolcontract.SourcePin{SourceID: "disk:r1/file", Representation: toolcontract.OriginalEncodedBytes, EndByte: 7}
		path, captured, release, ok := (&ProgramRuntime{}).loadCapturedSourceForPin(t.Context(), run, pin, 7)
		if limit == 1024 {
			if ok || captured != nil || release != nil || memory.InUse().Working != 0 {
				t.Fatalf("unadmitted capture=%q %q %v memory=%+v", path, captured, ok, memory.InUse())
			}
			continue
		}
		if !ok || path != "file" || string(captured) != "source\n" || memory.InUse().Working == 0 {
			t.Fatalf("admitted capture=%q %q %v memory=%+v", path, captured, ok, memory.InUse())
		}
		release()
		if memory.InUse().Working != 0 {
			t.Fatalf("capture lease leaked %+v", memory.InUse())
		}
		pin.Representation = toolcontract.LogicalUTF8LF
		testutil.FailErr(t, "seed unsupported text", os.WriteFile(filepath.Join(root, "file"), []byte{0xff}, 0600))
		if _, _, _, ok := (&ProgramRuntime{}).loadCapturedSourceForPin(t.Context(), run, pin, 1); ok || memory.InUse().Working != 0 {
			t.Fatal("failed decoding retained capture memory or granted credit")
		}
	}
}
