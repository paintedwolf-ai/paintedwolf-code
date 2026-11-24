package coordinatorcontrol

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	setupanchor "github.com/lycaon/lycaon/internal/testsetup/anchor"
	"github.com/lycaon/lycaon/pkg/api"
	"reflect"
	"testing"
	"time"
)

type scanRecipients struct {
	t       *testing.T
	cursors []string
}

func (s *scanRecipients) WorkspaceNotificationIDs(_ context.Context, path, after string) ([]string, error) {
	if path != "/project" {
		s.t.Fatalf("notification project=%q", path)
	}
	s.cursors = append(s.cursors, after)
	switch after {
	case "":
		return []string{"workflow", "ordinary"}, nil
	case "ordinary":
		return []string{"next"}, nil
	default:
		return nil, nil
	}
}

type scanEvidence struct{}

func (scanEvidence) ActiveRunOwnsScanEvidence(_ context.Context, id string) bool {
	return id == "workflow"
}

func TestScanDeltaPagesRecipientsWithoutDuplicatingWorkflowEvidence(t *testing.T) {
	setupanchor.Install()
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{})
	recipients := &scanRecipients{t: t}
	scans := &Scans{Runtime: rt, Sessions: recipients, Evidence: scanEvidence{}}
	findings := []api.SecurityFinding{{RuleID: "rule", Message: "observed finding"}}
	scans.Delta(t.Context(), api.CodeScan{ID: "scan", CanonicalPath: "/project"}, findings, nil)
	if !reflect.DeepEqual(recipients.cursors, []string{"", "ordinary", "next"}) {
		t.Fatalf("notification pages=%v", recipients.cursors)
	}
	for _, id := range []string{"ordinary", "next"} {
		if _, ok := rt.Anchors().Kicks().PeekPendingKickID(id); !ok {
			t.Fatalf("scan delta omitted recipient %q", id)
		}
	}
	if _, ok := rt.Anchors().Kicks().PeekPendingKickID("workflow"); ok {
		t.Fatal("workflow evidence was duplicated as a global delta")
	}
}

func TestScanFinishedNotifiesUnwokenWatchSessions(t *testing.T) {
	setupanchor.Install()
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{})
	loop := rt.CoordinatorLoop()
	loop.Waits.EnterSleep(
		t.Context(),
		"sleeping-sess",
		time.Now().UTC().Add(5*time.Minute),
		"waiting for scan",
		[]loopwake.WaitTrigger{loopwake.WaitTriggerScanDone},
		nil,
		loopwake.SleepMoverHost,
	)

	var watched []string
	scans := &Scans{
		Runtime: rt,
		Wait: ScanWaitState{
			Requested: func(_ context.Context, sessionID, scanID string) bool {
				return scanID == "scan-1" && (sessionID == "sleeping-sess" || sessionID == "watch-sess")
			},
		},
		WatchSessions: func(context.Context) ([]string, error) {
			return []string{"sleeping-sess", "unrequested-sess", "watch-sess"}, nil
		},
		OnWatchScanFinished: func(_ context.Context, sessionID string, _ api.CodeScan) bool {
			watched = append(watched, sessionID)
			return true
		},
	}
	scans.Finished(t.Context(), api.CodeScan{
		ID:            "scan-1",
		Status:        api.CodeScanStatusCompleted,
		Categories:    []api.ScanCategory{"security"},
		FindingsCount: 2,
	})
	if !reflect.DeepEqual(watched, []string{"watch-sess"}) {
		t.Fatalf("watched scan sessions = %v, want [watch-sess]", watched)
	}
}
