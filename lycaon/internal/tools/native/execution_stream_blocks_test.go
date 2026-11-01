package native

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

func TestExecutionStreamProvesBytesAcrossRestoredMetadataAndTailSeek(t *testing.T) {
	original := bytes.Repeat([]byte{'a'}, 2*sourceBlockBytes+17)
	f := newPinnedDiskFixture(t, map[string]string{"source": string(original)})
	path := filepath.Join(f.dir, "source")
	info, err := os.Stat(path)
	testutil.FailErr(t, "stat path", err)
	stream, err := f.revisions.OpenStream(t.Context(), f.tctx, "source", 0)
	testutil.FailErr(t, "f.revisions.OpenStream failed", err)
	defer stream.Reader.Close()
	if stream.Revision.ContentSHA256 != textfile.SHA256(original) {
		t.Fatal("wrong full pin")
	}
	first := make([]byte, sourceBlockBytes)
	if _, err = io.ReadFull(stream.Reader, first); err != nil || !bytes.Equal(first, original[:sourceBlockBytes]) {
		t.Fatalf("first block %v", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	testutil.FailErr(t, "os.OpenFile failed", err)
	_, err = file.WriteAt([]byte("changed"), sourceBlockBytes+3)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		testutil.FailErr(t, "os.Chtimes failed", err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		t.Fatal("fixture did not restore metadata")
	}
	body, err := io.ReadAll(stream.Reader)
	var conflict *ExecutionRevisionConflict
	if len(body) != 0 || !errors.As(err, &conflict) || conflict.Pinned != stream.Revision || conflict.Current.ContentSHA256 != "" {
		t.Fatalf("changed block %q %v", body, err)
	}
	seeker := stream.Reader.(io.Seeker)
	if _, err = seeker.Seek(-17, io.SeekEnd); err != nil {
		testutil.FailErr(t, "seeker.Seek failed", err)
	}
	body, err = io.ReadAll(stream.Reader)
	if err != nil || !bytes.Equal(body, original[len(original)-17:]) {
		t.Fatalf("unchanged tail %q %v", body, err)
	}
	if _, err = seeker.Seek(sourceBlockBytes+3, io.SeekStart); err != nil {
		testutil.FailErr(t, "seeker.Seek failed", err)
	}
	if n, err := stream.Reader.Read(make([]byte, 3)); n != 0 || !errors.As(err, &conflict) {
		t.Fatalf("seek into changed block %d %v", n, err)
	}
}

func TestExecutionStreamRefusesTruncationGrowthAndPartialLastBlockChange(t *testing.T) {
	for _, kind := range []string{"truncate", "grow", "last"} {
		t.Run(kind, func(t *testing.T) {
			body := bytes.Repeat([]byte{'b'}, sourceBlockBytes+7)
			f := newPinnedDiskFixture(t, map[string]string{"source": string(body)})
			stream, err := f.revisions.OpenStream(t.Context(), f.tctx, "source", 0)
			testutil.FailErr(t, "f.revisions.OpenStream failed", err)
			defer stream.Reader.Close()
			path := filepath.Join(f.dir, "source")
			switch kind {
			case "truncate":
				err = os.Truncate(path, 3)
			case "grow":
				err = os.WriteFile(path, append(body, 'c'), 0600)
			case "last":
				body[len(body)-1] = 'z'
				err = os.WriteFile(path, body, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = stream.Reader.(io.Seeker).Seek(-7, io.SeekEnd)
			testutil.FailErr(t, "stream.Reader. failed", err)
			actual, err := io.ReadAll(stream.Reader)
			var conflict *ExecutionRevisionConflict
			if len(actual) != 0 || !errors.As(err, &conflict) {
				t.Fatalf("escaped %q %v", actual, err)
			}
		})
	}
}

func TestExecutionStreamBlockAdmissionCancellationAndRelease(t *testing.T) {
	for _, limits := range []toolcontract.ProgramMemoryLimits{{Working: sourceBlockBytes - 1, State: 1024}, {Working: sourceBlockBytes, State: 255}, {Working: sourceBlockBytes, State: 700}} {
		f := newPinnedDiskFixture(t, map[string]string{"source": string(bytes.Repeat([]byte{'x'}, 9*sourceBlockBytes))})
		memory, err := toolcontract.NewProgramMemory(limits)
		testutil.FailErr(t, "toolcontract.NewProgramMemory failed", err)
		f.revisions.Memory = memory
		_, err = f.revisions.OpenStream(t.Context(), f.tctx, "source", 0)
		var limit *toolcontract.ProgramMemoryLimitError
		if !errors.As(err, &limit) || memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
			t.Fatalf("admission %v held %+v", err, memory.InUse())
		}
	}
	f := newPinnedDiskFixture(t, map[string]string{"source": "binary\x00data"})
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: sourceBlockBytes, State: 1024})
	testutil.FailErr(t, "toolcontract.NewProgramMemory failed", err)
	f.revisions.Memory = memory
	ctx, cancel := context.WithCancel(t.Context())
	stream, err := f.revisions.OpenStream(ctx, f.tctx, "source", 0)
	testutil.FailErr(t, "f.revisions.OpenStream failed", err)
	if use := memory.InUse(); use.Working != sourceBlockBytes || use.State != 256 {
		t.Fatalf("missing live admission %+v", use)
	}
	cancel()
	if n, err := stream.Reader.Read(make([]byte, 4)); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %d %v", n, err)
	}
	_ = stream.Reader.Close()
	_ = stream.Reader.Close()
	if memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatalf("close leak %+v", memory.InUse())
	}
	if _, err = stream.Reader.Read(make([]byte, 4)); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}

// The full pin must hash the exact ordered blocks, even if a writer changes a
// block already scanned while another block is being captured.
func TestExecutionStreamInitialCapturePinsOrderedBlocks(t *testing.T) {
	f := newPinnedDiskFixture(t, map[string]string{"source": ""})
	resolved, err := projectpaths.ResolveRead(t.Context(), f.revisions.Boundary, f.tctx, "source")
	testutil.FailErr(t, "projectpaths.ResolveRead failed", err)
	first := bytes.Repeat([]byte{'a'}, sourceBlockBytes)
	second := bytes.Repeat([]byte{'b'}, 17)
	input := &captureRewriteReader{first: first, second: second}
	captured, err := f.revisions.scanSource(t.Context(), resolved, input, true)
	testutil.FailErr(t, "f.revisions.scanSource failed", err)
	defer captured.Close()
	original := append(bytes.Repeat([]byte{'a'}, sourceBlockBytes), second...)
	if captured.revision.ContentSHA256 != textfile.SHA256(original) || captured.blocks[0] == captured.blocks[1] {
		t.Fatal("pin did not name scanned blocks")
	}
	captured.position = 0
	got := make([]byte, 1)
	_, err = captured.read(got, func(_ int64, p []byte) (int, error) { return copy(p, first), nil })
	var conflict *ExecutionRevisionConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("capture-time rewrite was credited: %v", err)
	}
}

type captureRewriteReader struct {
	first, second []byte
	reads         int
}

func (r *captureRewriteReader) Read(p []byte) (int, error) {
	r.reads++
	switch r.reads {
	case 1:
		return copy(p, r.first), nil
	case 2:
		r.first[0] = 'z'
		return copy(p, r.second), io.EOF
	default:
		return 0, io.EOF
	}
}

func TestExecutionStreamCompressedProofsPreserveBytesAndRelease(t *testing.T) {
	original := bytes.Repeat([]byte("a\x00\xff"), sourceBlockBytes)
	compressed, err := zstdcodec.Compress(bytes.NewReader(original))
	testutil.FailErr(t, "zstdcodec.Compress failed", err)
	for _, mutate := range []bool{false, true} {
		t.Run(fmt.Sprint(mutate), func(t *testing.T) {
			f := newPinnedDiskFixture(t, map[string]string{"source": string(compressed)})
			memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 64 << 20, State: 4096})
			testutil.FailErr(t, "toolcontract.NewProgramMemory failed", err)
			f.revisions.Memory = memory
			resolved, err := projectpaths.ResolveRead(t.Context(), f.revisions.Boundary, f.tctx, "source")
			testutil.FailErr(t, "projectpaths.ResolveRead failed", err)
			stream, err := f.revisions.openCompressedStream(t.Context(), f.tctx, resolved)
			testutil.FailErr(t, "f.revisions.openCompressedStream failed", err)
			if stream.Revision.ContentSHA256 != textfile.SHA256(original) || stream.FirstNullOffset != 1 {
				t.Fatal("wrong decoded pin")
			}
			if mutate {
				if err = os.Truncate(filepath.Join(f.dir, "source"), 1); err != nil {
					testutil.FailErr(t, "os.Truncate failed", err)
				}
			}
			got, err := io.ReadAll(stream.Reader)
			var conflict *ExecutionRevisionConflict
			if mutate {
				if len(got) != 0 || !errors.As(err, &conflict) {
					t.Fatalf("changed compressed bytes %d %v", len(got), err)
				}
			} else if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("decoded bytes %d %v", len(got), err)
			}
			_ = stream.Reader.Close()
			_ = stream.Reader.Close()
			if memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
				t.Fatalf("compressed leak %+v", memory.InUse())
			}
		})
	}
}

// Holding the real reader alive across collection detects a whole-input
// gather independently of transient garbage and the model-screening stage.
func TestExecutionStreamRetainedHeapIsProofSizedFor100MB(t *testing.T) {
	f := newPinnedDiskFixture(t, nil)
	file, err := os.Create(filepath.Join(f.dir, "source"))
	testutil.FailErr(t, "os.Create failed", err)
	chunk := bytes.Repeat([]byte{'x'}, 100_000)
	for range 1000 {
		if _, err = file.Write(chunk); err != nil {
			testutil.FailErr(t, "file.Write failed", err)
		}
	}
	if err = file.Close(); err != nil {
		testutil.FailErr(t, "file.Close failed", err)
	}
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 64 << 20, State: 64 << 20})
	testutil.FailErr(t, "toolcontract.NewProgramMemory failed", err)
	f.revisions.Memory = memory
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	stream, err := f.revisions.OpenStream(t.Context(), f.tctx, "source", 0)
	testutil.FailErr(t, "f.revisions.OpenStream failed", err)
	runtime.GC()
	runtime.ReadMemStats(&after)
	if after.HeapAlloc > before.HeapAlloc+(4<<20) || memory.Peak().State > 1<<20 || memory.Peak().Working > sourceBlockBytes {
		t.Fatalf("100 MB gather: heap before=%d after=%d admission=%+v", before.HeapAlloc, after.HeapAlloc, memory.Peak())
	}
	n, err := io.Copy(io.Discard, stream.Reader)
	if err != nil || n != 100_000_000 {
		t.Fatalf("read %d %v", n, err)
	}
	_ = stream.Reader.Close()
	runtime.KeepAlive(stream)
	if memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatalf("leak %+v", memory.InUse())
	}
	t.Logf("100 MB held reader: retained heap delta=%d admitted peak=%+v", int64(after.HeapAlloc)-int64(before.HeapAlloc), memory.Peak())
}
