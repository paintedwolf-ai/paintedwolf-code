package workflowadmin_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestListWorkflowTemplatesWithoutCatalogIsEmpty(t *testing.T) {
	f := newRoutesFixture(t, nil)
	list := decodeOK[wire.WorkflowTemplateListResponse](t, f.do(t, http.MethodGet, "/v1/workflow-templates", "", ""), http.StatusOK)
	if list.Templates == nil || len(list.Templates) != 0 {
		t.Fatalf("templates = %#v", list.Templates)
	}
}

func TestComposeWorkflowRefusesBeforeComposing(t *testing.T) {
	f := newRoutesFixture(t, nil)
	sess := f.createSession(t)
	compose := "/v1/sessions/" + sess.ID + "/workflows/compose"
	fromTemplate := "/v1/sessions/" + sess.ID + "/workflows/compose-from-template"
	persist := "/v1/sessions/" + sess.ID + "/workflows/draft-flow/persist"
	missing := "/v1/sessions/" + uuid.NewString()

	tests := []struct {
		name        string
		path        string
		contentType string
		body        string
		want        wire.ApiErrorCode
	}{
		{name: "compose unknown session", path: missing + "/workflows/compose", contentType: httpio.MediaTypeYAML, body: "id: x\n", want: wire.ApiErrorCodeSessionNotFound},
		{name: "compose json body", path: compose, contentType: httpio.MediaTypeJSON, body: `{}`, want: wire.ApiErrorCodeUnsupportedMediaType},
		{name: "compose bad dry_run", path: compose + "?dry_run=perhaps", contentType: httpio.MediaTypeYAML, body: "id: x\n", want: wire.ApiErrorCodeInvalidQuery},
		{name: "template unknown session", path: missing + "/workflows/compose-from-template", contentType: httpio.MediaTypeJSON, body: `{"template_id":"t"}`, want: wire.ApiErrorCodeSessionNotFound},
		{name: "template malformed", path: fromTemplate, contentType: httpio.MediaTypeJSON, body: `{`, want: wire.ApiErrorCodeInvalidJson},
		{name: "template bad dry_run", path: fromTemplate + "?dry_run=perhaps", contentType: httpio.MediaTypeJSON, body: `{"template_id":"t"}`, want: wire.ApiErrorCodeInvalidQuery},
		{name: "persist unknown session", path: missing + "/workflows/draft-flow/persist", contentType: httpio.MediaTypeJSON, body: `{}`, want: wire.ApiErrorCodeSessionNotFound},
		{name: "persist malformed", path: persist, contentType: httpio.MediaTypeJSON, body: `{`, want: wire.ApiErrorCodeInvalidJson},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantError(t, f.do(t, http.MethodPost, tt.path, tt.contentType, tt.body), tt.want)
		})
	}
}

func TestApproveBlueprintRequiresTheBoundRunAndCurrentContent(t *testing.T) {
	f := newRoutesFixture(t, nil)
	bp, err := f.blueprints.Create(t.Context(), f.project.ID, "Rate limiter plan", "", "", "# Plan\n\nRefill from a monotonic clock.\n")
	testutil.FailErr(t, "create blueprint", err)
	bound := f.seedRun(t, bp.Path)
	unbound := f.seedRun(t, "")
	approve := "/v1/projects/" + f.project.ID + "/blueprints/" + bp.ID + "/approve"
	body := func(runID, digest string) string {
		return fmt.Sprintf(`{"workflow_run_id":%q,"expected_revision":%d,"content_digest":%q}`, runID, bound.Revision, digest)
	}

	tests := []struct {
		name string
		path string
		body string
		want wire.ApiErrorCode
	}{
		{name: "unknown project", path: "/v1/projects/" + uuid.NewString() + "/blueprints/" + bp.ID + "/approve", body: body(bound.ID, "d"), want: wire.ApiErrorCodeProjectNotFound},
		{name: "unknown blueprint", path: "/v1/projects/" + f.project.ID + "/blueprints/" + uuid.NewString() + "/approve", body: body(bound.ID, "d"), want: wire.ApiErrorCodeBlueprintNotFound},
		{name: "malformed", path: approve, body: `{`, want: wire.ApiErrorCodeInvalidJson},
		{name: "run bound elsewhere", path: approve, body: body(unbound.ID, "d"), want: wire.ApiErrorCodeWorkflowRunNotActive},
		{name: "stale content", path: approve, body: body(bound.ID, "sha256:stale"), want: wire.ApiErrorCodeBlueprintContentConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantError(t, f.postJSON(t, tt.path, tt.body), tt.want)
		})
	}

	still, err := f.blueprints.Get(t.Context(), f.project.ID, bp.Path)
	testutil.FailErr(t, "reload blueprint", err)
	if still.Status != wire.BlueprintStatusDraft {
		t.Fatalf("refused approval changed blueprint status to %s", still.Status)
	}
}

func TestLaunchBlueprintRefusesIncompatibleTargets(t *testing.T) {
	f := newRoutesFixture(t, nil)
	bp, err := f.blueprints.Create(t.Context(), f.project.ID, "Launch plan", "", "", "# Plan\n")
	testutil.FailErr(t, "create blueprint", err)
	launch := "/v1/projects/" + f.project.ID + "/blueprints/" + bp.ID + "/launch"

	wantError(t, f.postJSON(t, "/v1/projects/"+f.project.ID+"/blueprints/"+uuid.NewString()+"/launch", `{}`), wire.ApiErrorCodeBlueprintNotFound)
	wantError(t, f.postJSON(t, launch, `{`), wire.ApiErrorCodeInvalidJson)
	wantError(t, f.postJSON(t, launch, `{"target_workflow_id":"no-such-workflow"}`), wire.ApiErrorCodeBlueprintLaunchIncompatible)

	sessions, err := f.sessions.List(t.Context())
	testutil.FailErr(t, "list sessions", err)
	if len(sessions) != 0 {
		t.Fatalf("refused launch materialized %d sessions", len(sessions))
	}
}
