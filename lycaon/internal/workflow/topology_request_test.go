package workflow

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTopologyRequestAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, request, fallback    string
		ambient, requestless, want bool
	}{
		{name: "pending"},
		{name: "explicit", request: "Inspect the project", want: true},
		{name: "default", fallback: "Inspect the project", want: true},
		{name: "ambient waiting", ambient: true},
		{name: "without request contract", requestless: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, _, _, _ := testManager(t)
			manifest := requestTestManifest("topology-request", workflowdef.RequestCadenceOnce, tc.fallback)
			if tc.requestless {
				manifest.Request = nil
			}
			if tc.ambient {
				manifest.Attach.Policy = workflowdef.AttachPolicySessionCreate
			}
			mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"topology-request@1.0.0": manifest})
			var run *api.WorkflowRun
			var err error
			if tc.ambient {
				run, err = mgr.StartAmbient(t.Context(), "sess-1", manifest.ID, manifest.Version)
			} else {
				run, err = mgr.StartHuman(t.Context(), "sess-1", api.StartWorkflowRunRequest{
					WorkflowID: manifest.ID, WorkflowVersion: manifest.Version, Request: tc.request,
				})
			}
			testutil.FailErr(t, "start workflow", err)
			ready, err := mgr.TopologyRequestReady(t.Context(), run.ID)
			testutil.FailErr(t, "check topology request", err)
			if ready != tc.want {
				t.Fatalf("topology ready = %v, want %v", ready, tc.want)
			}
		})
	}
}

func TestTopologyRequestAdmissionFailsOnUnreadableState(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	readErr := errors.New("request state unavailable")
	mgr.Store = scaffoldVarsFailingStore{RunStore: mgr.Store, err: readErr}
	ready, err := mgr.TopologyRequestReady(t.Context(), "run")
	if ready || !errors.Is(err, readErr) {
		t.Fatalf("unreadable state admitted topology: ready=%v err=%v", ready, err)
	}
}
