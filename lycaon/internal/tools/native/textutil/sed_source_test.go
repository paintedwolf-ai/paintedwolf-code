//go:build darwin || linux

package textutil

import (
	"bytes"
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

type mappedSedOutput struct {
	bytes.Buffer
	mappings []toolcontract.StreamSourceMapping
}

func (o *mappedSedOutput) RecordSourceMapping(mapping toolcontract.StreamSourceMapping) error {
	o.mappings = append(o.mappings, mapping)
	return nil
}

func TestSedSelectedRecordPublishesExactSourceMapping(t *testing.T) {
	h := newHarness(t)
	h.seed("records", "one\ntwo\nthree\n")
	request, release := h.bind("sed -n '2p' records")
	defer release()
	var output mappedSedOutput
	result, err := h.owners.Builtins(h.ports())["sed"].Invoke(t.Context(), request, commandprogram.Streams{Input: commandprogram.NullInput(), Output: &output, Error: io.Discard})
	testutil.FailErr(t, "run selected record", err)
	if result.Status != 0 || output.String() != "two\n" {
		t.Fatalf("selected output: result=%+v bytes=%q", result, output.String())
	}
	if len(output.mappings) != 1 || output.mappings[0].SourceStartByte != 4 || output.mappings[0].SourceEndByte != 8 || output.mappings[0].OutputStartByte != 0 || output.mappings[0].OutputEndByte != 4 {
		t.Fatalf("selected record lost exact source mapping: %+v", output.mappings)
	}
}

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
