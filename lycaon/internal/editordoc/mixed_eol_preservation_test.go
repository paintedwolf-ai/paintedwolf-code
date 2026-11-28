package editordoc

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func sha256String(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestApplyAgentEditPreservesUntouchedMixedEOLBytes(t *testing.T) {
	initial := "L1_crlf\r\nTARGET\nL3_lf\n"
	wantDisk := "L1_crlf\r\nEDITED\nL3_lf\n"
	wantSHA := sha256String(wantDisk)

	for _, tc := range []struct {
		name      string
		openTab   bool
		toolName  string
		wantPeers int
	}{
		// Matches E08-sidecar-mixed-eol: agent edits via replace-lines without an open Den tab;
		// OpenText implicitly admits the shared editordoc document with zero window participants.
		{name: "implicit_document_without_open_den_tab", openTab: false, toolName: "replace_lines", wantPeers: 0},
		// Explicit open Den tab with a joined window participant.
		{name: "open_den_tab_with_window_participant", openTab: true, toolName: "edit", wantPeers: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentFixture(t, map[string]string{"mixed.txt": initial})
			var doc *Document
			if tc.openTab {
				doc = f.open(t, "mixed.txt")
				var err error
				doc, err = f.service.Join(t.Context(), doc.ID, f.project.ID, ReplicaJoin{ClientID: "window-tab-1", Incarnation: uuid.NewString(), Epoch: doc.Epoch})
				testutil.FailErr(t, "join open Den tab", err)
			} else {
				var ok bool
				var err error
				doc, ok, err = f.service.OpenText(t.Context(), f.project.ID, sourcebranch.Trunk, f.rootID, "mixed.txt")
				testutil.FailErr(t, "implicit OpenText", err)
				if !ok || doc == nil {
					t.Fatalf("expected implicit OpenText to admit document, ok=%v", ok)
				}
			}
			if doc.ID == "" || doc.FileID == "" {
				t.Fatalf("expected committed document identity, got id=%q fileID=%q", doc.ID, doc.FileID)
			}
			if tc.openTab && (doc.ReplicaID == 0 || len(doc.Participants) != tc.wantPeers) {
				t.Fatalf("expected open Den tab to join replica & participant, got replicaID=%d participants=%d", doc.ReplicaID, len(doc.Participants))
			}
			if !tc.openTab && (doc.ReplicaID != 0 || len(doc.Participants) != 0) {
				t.Fatalf("expected implicit OpenText document to have no joined tab, got replicaID=%d participants=%d", doc.ReplicaID, len(doc.Participants))
			}
			if !doc.MixedEOL || !doc.BaseMixedEOL {
				t.Fatalf("expected MixedEOL=true on open, got mixed=%v baseMixed=%v", doc.MixedEOL, doc.BaseMixedEOL)
			}

			edit := agentEdit(doc, "L1_crlf\nEDITED\nL3_lf\n")
			edit.ToolName = tc.toolName
			previews, err := f.service.PreviewAgentEdits(t.Context(), []AgentEdit{edit})
			testutil.FailErr(t, "preview", err)
			if len(previews) != 1 || previews[0].AfterSHA256 != wantSHA {
				t.Fatalf("preview AfterSHA256 = %q, want %q", previews[0].AfterSHA256, wantSHA)
			}

			res, err := f.service.ApplyAgentEdit(t.Context(), edit)
			testutil.FailErr(t, "apply", err)
			if !res.Saved || res.Document.Dirty {
				t.Fatalf("expected clean saved result, got %+v", res)
			}
			gotDisk := f.disk(t, "mixed.txt")
			if sha256String(gotDisk) != wantSHA {
				t.Fatalf("mixed EOL bytes altered on disk:\ngot:  %q\nwant: %q", gotDisk, wantDisk)
			}
			if !res.Document.MixedEOL || !res.Document.BaseMixedEOL {
				t.Fatalf("expected MixedEOL and BaseMixedEOL to remain true, got mixed=%v baseMixed=%v", res.Document.MixedEOL, res.Document.BaseMixedEOL)
			}
			if res.Document.BaseSHA256 != wantSHA {
				t.Fatalf("BaseSHA256 = %q, want %q", res.Document.BaseSHA256, wantSHA)
			}
			if len(f.recorder.records) != 1 || f.recorder.records[0].Origin != api.SourceChangeOriginAgent || f.recorder.records[0].ToolName != tc.toolName {
				t.Fatalf("unexpected publication owner record: %+v", f.recorder.records)
			}
		})
	}
}

func TestExplicitUserEOLNormalizationStillConvertsMixedEOL(t *testing.T) {
	initial := "L1_crlf\r\nTARGET\nL3_lf\n"
	wantDisk := "L1_crlf\nTARGET\nL3_lf\n"

	f := newAgentFixture(t, map[string]string{"mixed.txt": initial})
	doc := f.open(t, "mixed.txt")
	updated, err := f.service.ReplaceSnapshot(t.Context(), doc.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision},
		Content:         "L1_crlf\nTARGET\nL3_lf\n",
		EOL:             "lf",
		MixedEOL:        false,
	})
	testutil.FailErr(t, "normalize EOL", err)
	if updated.MixedEOL || !updated.Dirty {
		t.Fatalf("expected MixedEOL=false Dirty=true after explicit normalization, got mixed=%v dirty=%v", updated.MixedEOL, updated.Dirty)
	}
	saved, err := f.service.Save(t.Context(), f.project, updated.ID, "window", uuid.NewString(), "", 0, updated.Revision)
	testutil.FailErr(t, "save normalized", err)
	if got := f.disk(t, "mixed.txt"); got != wantDisk {
		t.Fatalf("normalized disk = %q, want %q", got, wantDisk)
	}
	if saved.MixedEOL || saved.BaseMixedEOL || saved.Dirty {
		t.Fatalf("expected uniform clean doc after normalization save, got mixed=%v baseMixed=%v dirty=%v", saved.MixedEOL, saved.BaseMixedEOL, saved.Dirty)
	}
}

func TestMixedEOLPreviewAndSaveFailClosedWhenPreimageChangedOnDisk(t *testing.T) {
	initial := "L1_crlf\r\nTARGET\nL3_lf\n"
	outside := "L1_crlf\r\nOUTSIDE_CHANGED\nL3_lf\n"
	outsideSHA := sha256String(outside)

	f := newAgentFixture(t, map[string]string{"mixed.txt": initial})
	doc, ok, err := f.service.OpenText(t.Context(), f.project.ID, sourcebranch.Trunk, f.rootID, "mixed.txt")
	testutil.FailErr(t, "open mixed", err)
	if !ok || !doc.MixedEOL {
		t.Fatalf("expected mixed-EOL document, ok=%v mixed=%v", ok, doc.MixedEOL)
	}
	// Mutate disk underneath so the authorized BaseSHA256 preimage no longer matches disk.
	f.write(t, "mixed.txt", outside)

	// Preview MUST fail closed with ErrSourceWriteConflict (never silently fall back to uniform EOL).
	edit := agentEdit(doc, "L1_crlf\nEDITED\nL3_lf\n")
	if _, err := f.service.PreviewAgentEdits(t.Context(), []AgentEdit{edit}); !errors.Is(err, projectsource.ErrSourceWriteConflict) {
		t.Fatalf("PreviewAgentEdits on changed mixed-EOL preimage = %v, want ErrSourceWriteConflict", err)
	}
	// Save MUST also fail closed and leave disk bytes untouched.
	if _, err := f.service.Save(t.Context(), f.project, doc.ID, "window", uuid.NewString(), "", 0, doc.Revision); !errors.Is(err, projectsource.ErrSourceWriteConflict) {
		t.Fatalf("Save on changed mixed-EOL preimage = %v, want ErrSourceWriteConflict", err)
	}
	if got := sha256String(f.disk(t, "mixed.txt")); got != outsideSHA {
		t.Fatalf("failed mixed-EOL save mutated disk: got=%s want=%s", got, outsideSHA)
	}
}
