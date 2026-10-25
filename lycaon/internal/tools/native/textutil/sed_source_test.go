//go:build darwin || linux

package textutil

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestSedLazyInputRetainsCapturedPinAndConsumedOffsets(t *testing.T) {
	h := newHarness(t)
	first := strings.Repeat("a", 70<<10)
	body := first + "\né\nlast"
	h.seed("records", body)
	owners := *h.owners
	owners.Revisions.Memory = h.memory
	inv := &invocation{ctx: t.Context(), owners: &owners, ports: h.ports(), spec: owners.specs["sed"], request: commandprogram.OperationRequest{Invocation: toolcontract.OperationInvocation{Identity: toolcontract.ExecutionIdentity{ExecutionID: "sed-source"}}}}
	inputs, err := inv.openInputs(commandprogram.Fields{{Value: "records"}})
	testutil.FailErr(t, "describe lazy source", err)
	if len(h.pins.executions) != 0 {
		t.Fatal("describing input opened or pinned the source")
	}
	reader := (&sedRun{inv: inv}).input(inputs)
	digest := sha256.Sum256([]byte(body))
	for i, want := range []struct {
		text       string
		offset     int64
		terminated bool
	}{{first, 0, true}, {"é", int64(len(first) + 1), true}, {"last", int64(len(first) + 4), false}} {
		record, err := reader.Next()
		testutil.FailErr(t, "read captured record", err)
		if string(record.Text) != want.text || record.Offset != want.offset || record.Terminated != want.terminated || record.Pin == nil || record.Pin.SHA256 != hex.EncodeToString(digest[:]) || record.Pin.EndByte != int64(len(body)) {
			t.Fatalf("record %d has incorrect bytes, offset or source: %+v", i, record)
		}
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("end of source: %v", err)
	}
	if len(h.pins.executions["sed-source"]) != 1 {
		t.Fatal("lazy source was recaptured between records")
	}
	closeSources(inputs)
	inv.close()
	if used := h.memory.InUse(); used.Working != 0 || used.State != 0 {
		t.Fatalf("source retained resources after close: %+v", used)
	}
}
