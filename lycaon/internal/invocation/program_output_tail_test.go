package invocation_test

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

func lastLines(body string, n int) string {
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

// Receipt pages carry a whole-line tail only when the preview stops short, and
// a line count only for complete capture.
func TestExecutionOutputCarriesTailAndLineCount(t *testing.T) {
	long := ""
	for i := range 400 {
		long += fmt.Sprintf("traceback frame %d\n", i)
	}
	cases := []struct {
		name      string
		body      io.Reader
		complete  bool
		wantTail  string
		wantLines *int64
	}{
		{name: "continues", body: strings.NewReader(long), complete: true, wantTail: lastLines(long, 6), wantLines: ptr(int64(400))},
		{name: "fits", body: strings.NewReader("ok\ndone"), complete: true, wantTail: "", wantLines: ptr(int64(2))},
		{name: "incomplete", body: io.MultiReader(strings.NewReader(long), iotest.ErrReader(errors.New("producer failed"))), complete: false, wantTail: lastLines(long, 6)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newProgramReceiptFixture(t)
			child := f.child(t)
			f.start(t, child)
			execution := child.Invocation().Identity.ExecutionID
			quota := &outputQuota{}
			store := f.recorder.ProgramBlobStore(t.Context(), blobstore.Store{Root: t.TempDir(), Dir: tooloutput.ToolOutputSpillDir})
			lifetime, err := invocation.NewProgramOutputRetention(t.Context(), f.recorder, store, f.session, quota)
			testutil.FailErr(t, "create lifetime", err)
			testutil.FailErr(t, "restore lifetime", lifetime.Restore(t.Context()))
			spill, err := tooloutput.RetainScreenedStream(t.Context(), tooloutput.ScreenedReader(io.NopCloser(c.body)), tooloutput.StreamSpillOptions{Store: store, ClaimID: execution + ":stdout", MaximumBytes: 1 << 20, PreviewBytes: 4 << 10, Admission: quota, WorkingMemory: quota, RetainedState: quota})
			testutil.FailErr(t, "retain stdout", err)
			defer spill.Close()
			ref := "output:program:" + f.program + ":" + execution + ":stdout"
			_, err = lifetime.Attach(t.Context(), f.program, invocation.RetainedOutput{ExecutionID: execution, Stream: "stdout", Reference: ref, Complete: c.complete}, spill)
			testutil.FailErr(t, "attach stdout", err)
			page, err := f.recorder.ReadProgramActivityPage(t.Context(), f.session, f.program, execution, "", 6)
			testutil.FailErr(t, "read activity page", err)
			var output *api.CommandExecutionOutput
			for i := range page.Executions[0].Outputs {
				if page.Executions[0].Outputs[i].Stream == "stdout" {
					output = &page.Executions[0].Outputs[i]
				}
			}
			if output == nil {
				t.Fatal("stdout missing from receipt page")
			}
			if output.TailPreview != c.wantTail {
				t.Fatalf("tail %q; want %q", output.TailPreview, c.wantTail)
			}
			if (output.LineCount == nil) != (c.wantLines == nil) || (output.LineCount != nil && *output.LineCount != *c.wantLines) {
				t.Fatalf("line count %v; want %v", output.LineCount, c.wantLines)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
