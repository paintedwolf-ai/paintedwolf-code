package toolhost

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

type stubParking struct{ id string }

func (stubParking) ParkProgram(context.Context, tools.ToolContext, tools.ProgramPark) (tools.ParkedProgram, error) {
	return nil, tools.ErrParkUnavailable
}

// The composed program runtime parks through the host's wait lease and
// reports a program the host no longer runs from its receipts.
func TestCommandServicesComposeProgramParkingAndReceipts(t *testing.T) {
	runtime, err := NewRuntime(RuntimeConfig{ConfigRoot: configlayout.FindModuleRoot(), Catalog: extpackstest.StockCatalog(t)})
	testutil.FailErr(t, "compose production tool host", err)
	parking := stubParking{id: "host-lease"}
	runtime.Commands.SetProgramParking(parking)
	if got, ok := runtime.Commands.programs().Parking.(stubParking); !ok || got != parking {
		t.Fatalf("program parking %#v", runtime.Commands.programs().Parking)
	}

	fixture := programtest.New(t, programtest.Options{})
	original := fixture.Runtime
	testutil.FailErr(t, "compose program services", runtime.Commands.SetProgramServices(ProgramServices{
		Recorder: fixture.Recorder, Blobs: original.Blobs, Projector: original.Projector, Progress: original.Progress, Sessions: original.Sessions,
	}))
	content, err := fixture.Call(t, t.Context(), "true")
	testutil.FailErr(t, "run program", err)
	var ran command.ProgramOutcome
	testutil.FailErr(t, "decode program", json.Unmarshal([]byte(content), &ran))
	reported, err := runtime.Commands.ProgramReceiptResult(t.Context(), fixture.Session, ran.ProgramID)
	testutil.FailErr(t, "report program from receipts", err)
	var receipt command.ProgramOutcome
	testutil.FailErr(t, "decode receipt result", json.Unmarshal([]byte(reported), &receipt))
	if receipt.ProgramID != ran.ProgramID || receipt.State != "completed" || receipt.Status != 0 || receipt.StopCode != "" {
		t.Fatalf("receipt result %s", reported)
	}
	var missing *CommandServices
	if _, err := missing.ProgramReceiptResult(t.Context(), fixture.Session, ran.ProgramID); err == nil {
		t.Fatal("an unconfigured host reported a program")
	}
}
