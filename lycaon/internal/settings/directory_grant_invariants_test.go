package settings_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/sensitivepath"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Rendering a read directory must not conceal different durable authority.
func TestDirectoryPlanRejectsLeaseAuthorityOutsideDisplayedScope(t *testing.T) {
	folder := canonDir(t, t.TempDir())
	file := filepath.Join(folder, "file.go")
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{Tool: "read", Files: []string{file}},
		Scope:      hitl.ActionScope{ProjectID: "project", ProjectDir: t.TempDir(), SessionID: "chat"},
	}
	for _, mutation := range []string{"broader lease", "write lease", "exact lease", "broader live grant", "missing lease path"} {
		t.Run(mutation, func(t *testing.T) {
			offers := settings.GrantedPathOffers(action, gate.FileTarget{Path: file, Mode: gate.ModeRead, OutsideRoots: true}, &gate.Decision{Primary: api.GateOutsideRootsRead}, nil)
			options := []hitl.ApprovalOption{hitl.GrantOption(offers[0])}
			compile := func() error {
				_, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn,
					hitl.ApprovalSubject{Kind: hitl.ApprovalSubjectAction, Title: "Read file", Targets: []hitl.ApprovalTarget{{Kind: "file", Label: file}}},
					hitl.ApprovalPresentation{Action: "Read file", Impact: "Read the selected directory", Gate: api.GateOutsideRootsRead, Cited: []hitl.PresentedFact{{Gate: api.GateOutsideRootsRead, Key: "path", Value: file, Source: "action"}}},
					[]api.ApprovalGate{api.GateOutsideRootsRead}, options, hitl.FaceContext{})
				return err
			}
			testutil.FailErr(t, "compile offered directory authority", compile())
			switch mutation {
			case "broader lease":
				options[0].Authority[1].Grant.GrantedPath.Path = filepath.Dir(folder)
			case "write lease":
				options[0].Authority[1].Grant.GrantedPath.Write = true
			case "exact lease":
				options[0].Authority[1].Grant.GrantedPath.Tree = false
			case "missing lease path":
				options[0].Authority[1].Grant.GrantedPath = nil
			case "broader live grant":
				options[0].Authority[0].GrantedPath.Path = filepath.Dir(folder)
			}
			if compile() == nil {
				t.Fatal("directory presentation concealed different authority")
			}
		})
	}
}

func TestBroadAttachedRootsRetainSensitiveReadReview(t *testing.T) {
	stageBundledApprovals(t, nil)
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "open approvals", err)
	testutil.FailErr(t, "use balanced posture", store.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureBalanced}))
	locations, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "load locations", err)
	sources := settings.NoSources()
	sources.Locations = locations
	review := settings.NewRuleApprovalGate(store, sources)
	for _, root := range []string{home(t), filepath.VolumeName(home(t)) + string(filepath.Separator)} {
		for _, path := range []string{filepath.Join(home(t), ".aws", "credentials"), filepath.Join(home(t), ".ssh", "id_ed25519")} {
			action := hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "read", Files: []string{path}}, Scope: hitl.ActionScope{ProjectID: "project", ProjectDir: root, SessionID: "chat"}}
			result, err := review.Evaluate(context.Background(), action)
			testutil.FailErr(t, "review protected read inside broad root", err)
			if !result.Required() || result.Decision.Primary != api.GateSensitiveLocation {
				t.Fatalf("root %q concealed protected read %q: %+v", root, path, result)
			}
			for _, offer := range review.GrantOffers(action, result) {
				access := offer.Grant.GrantedPath
				if access == nil || access.Tree || access.Write || access.Path != path || offer.DirectoryScope != "" {
					t.Fatalf("protected read authority widened: %+v", offer)
				}
			}
		}
	}
}
