package survey

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	oldToken = "TOKEN=old-value\n"
	newToken = "TOKEN=new-value\n"
)

// Every source_history path that returns a credential file's text records its
// delivery first, and withholds the text when it cannot.
func TestSourceHistoryACDF19CredentialTextRecordsItsDeliveryOnEveryPath(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(h *historyHarness) (tools.ToolContext, map[string]any)
		want  []string
	}{
		{
			name: "default diff on the trunk",
			setup: func(h *historyHarness) (tools.ToolContext, map[string]any) {
				h.beginSession(historySession)
				h.record(effectSeed{path: ".env", content: newToken, before: oldToken})
				return h.tctx, map[string]any{"path": ".env", "mode": "diff"}
			},
			want: []string{oldToken, newToken},
		},
		{
			name: "explicit version diff",
			setup: func(h *historyHarness) (tools.ToolContext, map[string]any) {
				h.record(effectSeed{path: ".env", content: newToken, before: oldToken})
				return h.tctx, map[string]any{"path": ".env", "mode": "diff", "version_id": h.head(".env").VersionID}
			},
			want: []string{oldToken, newToken},
		},
		{
			name: "version mode",
			setup: func(h *historyHarness) (tools.ToolContext, map[string]any) {
				h.record(effectSeed{path: ".env", content: newToken})
				return h.tctx, map[string]any{"path": ".env", "mode": "version", "version_id": h.head(".env").VersionID}
			},
			want: []string{newToken},
		},
		{
			name: "ledger answer on a write leg",
			setup: func(h *historyHarness) (tools.ToolContext, map[string]any) {
				h.recordOnWorker(effectSeed{path: ".env", content: newToken, before: oldToken})
				return h.workerWith(&fakeTree{}), map[string]any{"path": ".env", "mode": "diff"}
			},
			want: []string{oldToken, newToken},
		},
		{
			name: "ledger answer when the tree cannot compare",
			setup: func(h *historyHarness) (tools.ToolContext, map[string]any) {
				h.recordOnWorker(effectSeed{path: ".env", content: newToken, before: oldToken})
				return h.workerWith(&fakeTree{err: errors.New("manifest unreadable")}), map[string]any{"path": ".env", "mode": "diff"}
			},
			want: []string{oldToken, newToken},
		},
		{
			name: "live file on a write leg",
			setup: func(h *historyHarness) (tools.ToolContext, map[string]any) {
				return h.workerWith(&fakeTree{path: workspacebaseline.PathComparison{
					Kind: workspacebaseline.ChangeModified, Abs: filepath.Join(h.dir, ".env"),
					Before: oldToken, After: newToken, HasBefore: true, HasAfter: true,
				}}), map[string]any{"path": ".env", "mode": "diff"}
			},
			want: []string{oldToken, newToken},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHistoryHarness(t)
			h.writeFile(".env", newToken)
			tctx, args := c.setup(h)
			recorder := &deliveredRecorder{}
			tctx.Effects.CredentialFiles = recorder
			out, err := h.tool.Run(t.Context(), args, tctx)
			testutil.FailErr(t, "source_history", err)
			if out == "" || len(recorder.reads) != len(c.want) {
				t.Fatalf("delivered %+v, want %d reads", recorder.reads, len(c.want))
			}
			for i, read := range recorder.reads {
				if read.Content != c.want[i] || read.Path != ".env" || read.RootID != "r1" || read.Container != ".env" {
					t.Fatalf("read %d = %+v, want %q at .env in r1", i, read, c.want[i])
				}
			}
			recorder.err = errors.New("evidence store down")
			if withheld, err := h.tool.Run(t.Context(), args, tctx); err == nil {
				t.Fatalf("result = %q, want the text withheld", withheld)
			}
		})
	}
}

// Text a file held under a credential name stays credential text after a move.
func TestSourceHistoryACDF19MovedCredentialFileRecordsWhereItsTextCameFrom(t *testing.T) {
	h := newHistoryHarness(t)
	h.record(effectSeed{path: ".env", content: oldToken, session: "s0", op: api.SourceChangeOpCreate})
	h.beginSession(historySession)
	h.record(effectSeed{path: "settings.txt", fromPath: ".env", op: api.SourceChangeOpRename})
	h.record(effectSeed{path: "settings.txt", content: newToken})
	recorder := &deliveredRecorder{}
	h.tctx.Effects.CredentialFiles = recorder
	result := h.sessionDiff("settings.txt")
	if result.Diff == "" || len(recorder.reads) != 1 || recorder.reads[0].Path != ".env" || recorder.reads[0].Content != oldToken {
		t.Fatalf("delivered %+v for diff %q, want the old name's text", recorder.reads, result.Diff)
	}
}

// Without text in the answer there is nothing to record.
func TestSourceHistoryACDF19AnswerWithNoTextRecordsNothing(t *testing.T) {
	large := func(prefix string) string {
		text := ""
		for i := range 5_000 {
			text += fmt.Sprintf("%s_%d=value\n", prefix, i)
		}
		return text
	}
	for name, compared := range map[string]workspacebaseline.PathComparison{
		"sides too different": {Kind: workspacebaseline.ChangeModified, Before: large("A"), After: large("B"), HasBefore: true, HasAfter: true},
		"no text side":        {Kind: workspacebaseline.ChangeModified, Before: oldToken, HasBefore: true},
		"identical sides":     {Kind: workspacebaseline.ChangeModified, Before: oldToken, After: oldToken, HasBefore: true, HasAfter: true},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHistoryHarness(t)
			h.writeFile(".env", newToken)
			compared.Abs = filepath.Join(h.dir, ".env")
			worker := h.workerWith(&fakeTree{path: compared})
			recorder := &deliveredRecorder{}
			worker.Effects.CredentialFiles = recorder
			_, err := h.tool.Run(t.Context(), map[string]any{"path": ".env", "mode": "diff"}, worker)
			testutil.FailErr(t, "diff", err)
			if len(recorder.reads) != 0 {
				t.Fatalf("delivered %+v for an answer with no text", recorder.reads)
			}
		})
	}
}
