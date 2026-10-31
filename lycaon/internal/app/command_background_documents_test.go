package app

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestProgramBackgroundReadTransfersUndeliveredDraftNotice(t *testing.T) {
	for _, initialRead := range []bool{false, true} {
		name := "first read after response"
		if initialRead {
			name = "foreground notice already delivered"
		}
		t.Run(name, func(t *testing.T) {
			f, service, p := documentProgram(t)
			path := filepath.Join(f.Dir, "a.txt")
			testutil.FailErr(t, "saved", os.WriteFile(path, []byte("saved\n"), 0o644))
			doc, err := service.Open(t.Context(), p, "a.txt", "r1", "", "window", nil)
			testutil.FailErr(t, "open", err)
			_, err = service.ReplaceSnapshot(t.Context(), doc.ID, p.ID, editordoc.SnapshotReplacement{DocumentCommand: editordoc.DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision}, Content: "human draft\n", EOL: "lf"})
			testutil.FailErr(t, "human edit", err)
			completed := make(chan bgprocess.Completion, 1)
			background := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{Complete: func(_ context.Context, c bgprocess.Completion) { completed <- c }})
			t.Cleanup(func() { _ = background.Lifecycle.Close(context.Background()) })
			f.Background, f.Runtime.Supervisor.Background = background, background
			gate := make(chan struct{})
			release := sync.OnceFunc(func() { close(gate) })
			defer release()
			original, calls := f.Runtime.Owners, 0
			f.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths, memory *toolcontract.ProgramMemory, admission tooloutput.SpillAdmission) (command.ProgramOwners, error) {
				owners, err := original(tc, paths, memory, admission)
				cat := owners.Natives["cat"]
				prepare := cat.Prepare
				cat.Prepare = func(ctx context.Context, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
					prepared, err := prepare(ctx, paths, dispatch, request)
					if err != nil {
						return prepared, err
					}
					invoke := prepared.Invoke
					prepared.Invoke = func(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
						calls++
						if !initialRead || calls == 2 {
							select {
							case <-gate:
							case <-ctx.Done():
								return toolcontract.ProducerResult{}, ctx.Err()
							}
						}
						return invoke(ctx, tc, streams)
					}
					return prepared, nil
				}
				owners.Natives["cat"] = cat
				return owners, err
			}
			source := "{ cat a.txt; cat a.txt; } &"
			if initialRead {
				source = "cat a.txt; " + source
			}
			outcome := callDocumentProgram(t, f, source)
			if outcome.Status != 0 || outcome.CompletedEffects != 0 {
				t.Fatalf("initial=%+v", outcome)
			}
			if !initialRead && (outcome.Stdout != "" || outcome.Stderr != "") {
				t.Fatalf("job escaped gate: %+v", outcome)
			}
			if initialRead && (outcome.Stdout != "saved\n" || outcome.Stderr == "") {
				t.Fatalf("initial read=%+v", outcome)
			}
			release()
			var completion bgprocess.Completion
			select {
			case completion = <-completed:
			case <-time.After(5 * time.Second):
				t.Fatal("background completion not delivered")
			}
			if completion.ReleaseNotices != nil {
				defer completion.ReleaseNotices()
			}
			if completion.ExitCode != 0 || completion.SessionID != f.Session {
				t.Fatalf("completion=%+v", completion)
			}
			snapshot, err := background.Output.ReadRawOutput(f.Session, completion.Handle, 0)
			testutil.FailErr(t, "background bytes", err)
			bytes := ""
			for _, chunk := range snapshot.Output.Chunks {
				if chunk.Stream != "stdout" {
					t.Fatalf("notice entered command stream: %+v", chunk)
				}
				bytes += chunk.Text
			}
			if bytes != "saved\nsaved\n" || snapshot.Output.Running || snapshot.Output.ExitCode == nil || *snapshot.Output.ExitCode != 0 {
				t.Fatalf("background bytes=%q snapshot=%+v", bytes, snapshot)
			}
			if initialRead && len(completion.UnsavedDocumentPaths) != 0 {
				t.Fatalf("notice repeated: %+v", completion)
			}
			if !initialRead && (len(completion.UnsavedDocumentPaths) != 1 || completion.UnsavedDocumentPaths[0] != path) {
				t.Fatalf("late notice lost: %+v", completion)
			}
			current, err := service.CurrentSnapshot(t.Context(), p.ID, doc.ID)
			testutil.FailErr(t, "draft", err)
			if !current.Dirty || current.Draft != "human draft\n" {
				t.Fatalf("draft=%+v", current)
			}
			disk, err := os.ReadFile(path)
			testutil.FailErr(t, "disk", err)
			if string(disk) != "saved\n" {
				t.Fatalf("disk=%q", disk)
			}
		})
	}
}
