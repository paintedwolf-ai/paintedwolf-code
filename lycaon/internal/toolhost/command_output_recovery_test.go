package toolhost

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/zstdcodec"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/survey"
)

func TestOutputViewHostReadRepairsChangedDeletedAndHardlinkedCopies(t *testing.T) {
	for _, mode := range []string{"modified", "deleted", "hard-link"} {
		t.Run(mode, func(t *testing.T) {
			f := newOutputDeliveryFixture(t)
			var tc tools.ToolContext
			f.Context = func(_ string, current *tools.ToolContext) { current.Host.ModelContextWindow = 16000; tc = *current }
			var content strings.Builder
			for i := 0; i < 400; i++ {
				content.WriteString("original source line for retained evidence and exact output recovery\n")
			}
			original := content.String()
			testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(f.Dir, "source.txt"), []byte(original), 0600))
			raw, err := f.Call(t, t.Context(), "cat source.txt")
			testutil.FailErr(t, "retain actual source output", err)
			out := decodeProgramOutcome(t, raw)
			path := out.Outputs[0].OutputPath
			target := filepath.Join(f.OutputDir, out.ProgramID+".stdout")
			before := len(f.Delivered())
			if before == 0 {
				t.Fatal("initial source delivery missing")
			}
			alias := filepath.Join(f.Dir, "alias")
			if mode == "deleted" {
				testutil.FailErr(t, "delete view", os.Remove(target))
			} else {
				writePath := target
				if mode == "hard-link" {
					testutil.FailErr(t, "plant alias", os.Link(target, alias))
					writePath = alias
				}
				testutil.FailErr(t, "change view mode", os.Chmod(writePath, 0600))
				testutil.FailErr(t, "change view bytes", os.WriteFile(writePath, []byte(strings.Repeat("x", len(original))), 0600))
			}
			// Command reads use the current view; neither absence nor changed bytes can
			// reuse the original file's mapping and claim unseen source coverage.
			_, _ = f.Call(t, t.Context(), "cat "+path)
			if got := len(f.Delivered()); got != before {
				t.Fatalf("invalid view added source credit: before=%d after=%d", before, got)
			}
			read := &survey.ReadTool{Boundary: f.Runtime.Boundary, OutputViews: f.Runtime}
			host, err := read.Run(t.Context(), map[string]any{"path": path, "limit": 3}, tc)
			testutil.FailErr(t, "read authoritative host output", err)
			if !strings.Contains(host, "original source line") || strings.Contains(host, "xxxxxxxx") {
				t.Fatalf("host served changed view: %q", host)
			}
			repaired, err := os.ReadFile(target)
			testutil.FailErr(t, "read repaired view", err)
			if string(repaired) != original {
				t.Fatalf("repaired bytes differ: %d", len(repaired))
			}
			if got := len(f.Delivered()); got != before {
				t.Fatalf("repair manufactured source credit: %d", got)
			}
			if mode == "hard-link" {
				prior, err := os.ReadFile(alias)
				testutil.FailErr(t, "read detached alias", err)
				if string(prior) == original {
					t.Fatal("repair changed outside alias rather than replacing view")
				}
			}
			_, err = f.Call(t, t.Context(), "sed -n '200,201p' "+path)
			testutil.FailErr(t, "read repaired command view", err)
			if len(f.Delivered()) <= before {
				t.Fatal("verified repaired view did not restore original source mapping")
			}
		})
	}
}

type outputViewOpen func(context.Context, tools.ToolContext, projectpaths.Resolved) (io.ReadCloser, error)

func (f outputViewOpen) OpenOutputView(ctx context.Context, tc tools.ToolContext, path projectpaths.Resolved) (io.ReadCloser, error) {
	return f(ctx, tc, path)
}

func TestOutputViewHostReadUsesHeldRetainedReaderAndSessionIdentity(t *testing.T) {
	f := newOutputDeliveryFixture(t)
	var tc tools.ToolContext
	f.Context = func(_ string, current *tools.ToolContext) { tc = *current }
	raw, err := f.Call(t, t.Context(), "printf 'retained bytes\n'")
	testutil.FailErr(t, "retain output", err)
	out := decodeProgramOutcome(t, raw)
	target := filepath.Join(f.OutputDir, out.ProgramID+".stdout")
	read := &survey.ReadTool{Boundary: f.Runtime.Boundary, OutputViews: outputViewOpen(func(ctx context.Context, current tools.ToolContext, path projectpaths.Resolved) (io.ReadCloser, error) {
		held, err := f.Runtime.OpenOutputView(ctx, current, path)
		if err != nil {
			return nil, err
		}
		testutil.FailErr(t, "mutate pathname after held reader", os.Chmod(target, 0600))
		testutil.FailErr(t, "write competing bytes", os.WriteFile(target, []byte("competing bytes\n"), 0600))
		return held, nil
	})}
	result, err := read.Run(t.Context(), map[string]any{"path": out.Outputs[0].OutputPath}, tc)
	testutil.FailErr(t, "read held retained bytes", err)
	if !strings.Contains(result, "retained bytes") || strings.Contains(result, "competing bytes") {
		t.Fatalf("pathname reopen race: %q", result)
	}
	read.OutputViews = f.Runtime
	foreign := tc
	foreign.Identity.SessionID = "foreign-output-session"
	if result, err = read.Run(t.Context(), map[string]any{"path": out.Outputs[0].OutputPath}, foreign); err == nil || result != "" {
		t.Fatalf("foreign host read=%q err=%v", result, err)
	}
	unchanged, err := os.ReadFile(target)
	testutil.FailErr(t, "read foreign refusal destination", err)
	if string(unchanged) != "competing bytes\n" {
		t.Fatal("foreign scope repaired another session's view")
	}
}

func TestOutputViewHostReadRejectsRetainedDigestMismatchBeforePublication(t *testing.T) {
	f := newOutputDeliveryFixture(t)
	var tc tools.ToolContext
	f.Context = func(_ string, current *tools.ToolContext) { tc = *current }
	raw, err := f.Call(t, t.Context(), "printf 'retained bytes\n'")
	testutil.FailErr(t, "retain output", err)
	out := decodeProgramOutcome(t, raw)
	var encoded string
	testutil.FailErr(t, "read actual retained identity", f.Database.QueryRowContext(t.Context(), `SELECT blob_json FROM invocation_program_outputs WHERE program_id=? AND reference=?`, out.ProgramID, out.StdoutRef).Scan(&encoded))
	var blob blobstore.Blob
	testutil.FailErr(t, "decode retained identity", json.Unmarshal([]byte(encoded), &blob))
	bad, err := zstdcodec.Compress(strings.NewReader("tampered bytes\n"))
	testutil.FailErr(t, "compress wrong retained content", err)
	store := f.Recorder.ProgramBlobStore(t.Context(), f.Runtime.Blobs)
	testutil.FailErr(t, "corrupt retained physical bytes", os.WriteFile(filepath.Join(store.Root, filepath.FromSlash(blob.Rel)), bad, 0600))
	target := filepath.Join(f.OutputDir, out.ProgramID+".stdout")
	testutil.FailErr(t, "remove view", os.Remove(target))
	read := &survey.ReadTool{Boundary: f.Runtime.Boundary, OutputViews: f.Runtime}
	result, err := read.Run(t.Context(), map[string]any{"path": out.Outputs[0].OutputPath}, tc)
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != tools.CommandOutputReadUnavailableCode || reject.Data["path"] != out.Outputs[0].OutputPath || result != "" {
		t.Fatalf("unverified blob returned=%q err=%v", result, err)
	}
	if _, err = os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("unverified blob published: %v", err)
	}
}
