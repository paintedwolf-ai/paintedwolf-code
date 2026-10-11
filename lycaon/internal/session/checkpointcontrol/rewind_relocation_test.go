package checkpointcontrol

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRelocatedRewindRecoveryLeavesOriginalInstallationUntouched(t *testing.T) {
	for _, phase := range []string{"prepared", "applying", "files_applied"} {
		for _, originalPresent := range []bool{false, true} {
			t.Run(phase+map[bool]string{false: "/original absent", true: "/original present"}[originalPresent], func(t *testing.T) {
				mgr, journal, path := prepareJournalFixture(t)
				if phase == "files_applied" {
					_, err := journal.Apply()
					testutil.FailErr(t, "apply before interruption", err)
				}
				if phase == "applying" {
					journal.Phase = sessioncheckpoint.PhaseApplying
					testutil.FailErr(t, "record applying phase", journal.Write())
				}
				op := sessionstore.RewindOperation{ID: journal.ID, SessionID: journal.TranscriptID, AnchorMessageID: journal.AnchorMessageID, ProjectDir: journal.ProjectDir, InputDigest: rewindInputDigest(journal.TranscriptID, journal.AnchorMessageID)}
				testutil.FailErr(t, "prepare receipt", mgr.store.PrepareRewind(t.Context(), op))
				if phase != "prepared" {
					testutil.FailErr(t, "record phase", mgr.store.SetRewindPhase(t.Context(), op.ID, phase, ""))
				}
				before, err := os.ReadFile(journal.Path())
				testutil.FailErr(t, "read original", err)
				destination := t.TempDir()
				rel, err := sessioncheckpoint.JournalRelPath(op.ProjectDir, op.ID)
				testutil.FailErr(t, "locate journal", err)
				relocated := filepath.Join(destination, rel)
				testutil.FailErr(t, "create destination", os.MkdirAll(filepath.Dir(relocated), 0700))
				testutil.FailErr(t, "copy journal", os.WriteFile(relocated, before, 0600))
				if !originalPresent {
					testutil.FailErr(t, "remove original journal", os.Remove(journal.Path()))
				}
				mgr.captures.dataDir = destination
				for boot := 0; boot < 2; boot++ {
					testutil.FailErr(t, "recover relocated rewind", mgr.RecoverRewindsForSession(t.Context(), op.SessionID))
				}
				row, err := mgr.store.GetRewindOperation(t.Context(), op.ID)
				testutil.FailErr(t, "read receipt", err)
				wantStatus, wantBytes := "rolled_back", "after"
				if phase == "files_applied" {
					wantStatus, wantBytes = "committed", "before"
				}
				current, err := os.ReadFile(path)
				testutil.FailErr(t, "read project", err)
				if row.Status != wantStatus || string(current) != wantBytes {
					t.Fatalf("receipt=%+v, bytes=%q", row, current)
				}
				if originalPresent {
					after, err := os.ReadFile(journal.Path())
					testutil.FailErr(t, "read untouched original", err)
					if !bytes.Equal(before, after) {
						t.Fatal("recovery changed original journal")
					}
				}
			})
		}
	}
}

func TestUnreadableRewindRemainsPendingAcrossBoots(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "identity mismatch"}[corrupt], func(t *testing.T) {
			mgr, journal, _ := prepareJournalFixture(t)
			op := sessionstore.RewindOperation{ID: journal.ID, SessionID: journal.TranscriptID, AnchorMessageID: journal.AnchorMessageID, ProjectDir: journal.ProjectDir, InputDigest: rewindInputDigest(journal.TranscriptID, journal.AnchorMessageID)}
			testutil.FailErr(t, "prepare receipt", mgr.store.PrepareRewind(t.Context(), op))
			if corrupt {
				journal.TranscriptID = "wrong-session"
				testutil.FailErr(t, "replace journal identity", journal.Write())
			} else {
				testutil.FailErr(t, "remove journal", os.Remove(journal.Path()))
			}
			for boot := 0; boot < 2; boot++ {
				if err := mgr.RecoverRewindsForSession(t.Context(), op.SessionID); err == nil {
					t.Fatal("unreadable journal admitted")
				}
				row, err := mgr.store.GetRewindOperation(t.Context(), op.ID)
				testutil.FailErr(t, "read pending receipt", err)
				if row.Status != "prepared" {
					t.Fatalf("unresolved journal became terminal: %s", row.Status)
				}
			}
		})
	}
}
