package native

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestBufferedBytesAdmitsCapacityBeforeGrowthAndReleases(t *testing.T) {
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 64 << 10, State: 4096})
	if err != nil {
		t.Fatal(err)
	}
	revisions := ExecutionRevisions{Memory: memory}
	buffer, err := revisions.NewBufferedBytes(t.Context(), 4096, "whole_file_bytes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = buffer.Write(bytes.Repeat([]byte{'a'}, 4096)); err != nil {
		t.Fatal(err)
	}
	if _, err = buffer.Write([]byte{'b'}); err == nil || len(buffer.Bytes) != 4096 {
		t.Fatalf("overflow changed body: %v bytes %d", err, len(buffer.Bytes))
	}
	buffer.Close()
	if memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatalf("buffer reservations remain %+v", memory.InUse())
	}
}

func TestExecutionSourceVerifyStreamsEncodedIdentityAndRefusesFalseTextPin(t *testing.T) {
	for _, encoding := range []string{textfile.UTF8, textfile.UTF8BOM, textfile.UTF16LEBOM, textfile.UTF16BEBOM} {
		f := newPinnedDiskFixture(t, nil)
		raw, err := textfile.EncodeBounded("aé😀\r\n", encoding, textfile.LimitsForRaw(1024))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(f.dir, "source"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		source, err := f.revisions.CaptureSource(t.Context(), f.tctx, "source")
		if err != nil {
			t.Fatal(err)
		}
		pin := source.Revision
		source.Close()
		if err = f.revisions.Verify(t.Context(), f.tctx, pin); err != nil {
			t.Fatalf("%s %v", encoding, err)
		}
		raw[len(raw)-1] ^= 1
		if err = os.WriteFile(filepath.Join(f.dir, "source"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		var conflict *ExecutionRevisionConflict
		if err = f.revisions.Verify(t.Context(), f.tctx, pin); !errors.As(err, &conflict) {
			t.Fatalf("%s moved source verified: %v", encoding, err)
		}
	}
	f := newPinnedDiskFixture(t, map[string]string{"binary": "a\x00b"})
	captured, err := f.revisions.CaptureBytes(t.Context(), f.tctx, "binary", 1024)
	if err != nil {
		t.Fatal(err)
	}
	pin := captured.Revision
	captured.Close()
	pin.Representation = toolcontract.UTF8SourceBytes
	var conflict *ExecutionRevisionConflict
	if err = f.revisions.Verify(t.Context(), f.tctx, pin); !errors.As(err, &conflict) {
		t.Fatalf("binary acquired text authority: %v", err)
	}
}

func TestBufferedBytesRefusesGrowthWithoutLosingPriorBody(t *testing.T) {
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 64 << 10, State: 96 << 10})
	if err != nil {
		t.Fatal(err)
	}
	buffer, err := (ExecutionRevisions{Memory: memory}).NewBufferedBytes(t.Context(), 128<<10, "whole_file_bytes")
	if err != nil {
		t.Fatal(err)
	}
	original := bytes.Repeat([]byte{'x'}, 64<<10)
	if _, err = buffer.Write(original); err != nil {
		t.Fatal(err)
	}
	_, err = buffer.Write([]byte{'y'})
	var limit *toolcontract.ProgramMemoryLimitError
	if !errors.As(err, &limit) || !bytes.Equal(buffer.Bytes, original) || memory.InUse().State != 64<<10 {
		t.Fatalf("growth refusal %v held %+v body %d", err, memory.InUse(), len(buffer.Bytes))
	}
	buffer.Close()
	if memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatalf("growth leak %+v", memory.InUse())
	}
}

func TestBufferedBytesResetReusesOnlyAdmittedCapacity(t *testing.T) {
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: 4096})
	if err != nil {
		t.Fatal(err)
	}
	buffer, err := (ExecutionRevisions{Memory: memory}).NewBufferedBytes(t.Context(), 4096, "record_bytes")
	if err != nil {
		t.Fatal(err)
	}
	for range 100 {
		if err := buffer.Reset(); err != nil {
			t.Fatal(err)
		}
		if _, err := buffer.Write([]byte("new record")); err != nil {
			t.Fatal(err)
		}
		if string(buffer.Bytes) != "new record" || memory.InUse().State != 4096 {
			t.Fatalf("record %q memory %+v", buffer.Bytes, memory.InUse())
		}
	}
	buffer.Close()
	if !errors.Is(buffer.Reset(), io.ErrClosedPipe) || memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatalf("closed buffer retains memory %+v", memory.InUse())
	}
}
