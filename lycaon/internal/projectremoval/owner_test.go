//go:build integration

package projectremoval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/testutil/extstatetest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type registry struct {
	rows      map[string]*project.Project
	listError error
	getError  error
}

func (r *registry) Get(_ context.Context, id string) (*project.Project, error) {
	if r.getError != nil {
		return nil, r.getError
	}
	p, ok := r.rows[id]
	if !ok {
		return nil, project.ErrNotFound
	}
	return p, nil
}
func (r *registry) List(context.Context) ([]project.Project, error) {
	if r.listError != nil {
		return nil, r.listError
	}
	out := []project.Project{}
	for _, p := range r.rows {
		out = append(out, *p)
	}
	return out, nil
}
func fixture(t *testing.T) (*Owner, *registry, string) {
	t.Helper()
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	id := uuid.NewString()
	reg := &registry{rows: map[string]*project.Project{id: {ID: id, Name: "Remove me"}}}
	owner := &Owner{Projects: reg, Extensions: extstatetest.Owner(t), Store: NewStore(testdbfixture.Open(t, "removals.db")), SuggestionsApply: func(project.Project) bool { return true }}
	owner.Delete = func(_ context.Context, id string, _ bool) error { delete(reg.rows, id); return nil }
	return owner, reg, id
}
func install(t *testing.T, o *Owner, projectID, packID string) {
	t.Helper()
	dir := t.TempDir()
	extpackstest.WriteMinimalPack(t, dir, packID, 1)
	testutil.FailErr(t, "remove shared policy fixture", os.Remove(filepath.Join(dir, "policy", "ACME_HELLO.yaml")))
	policyID := strings.ToUpper(strings.ReplaceAll(packID, "/", "_"))
	extpackstest.MustWrite(t, filepath.Join(dir, "policy", policyID+".yaml"), "id: "+policyID+"\nemit: banner\nmessage: hi\neffect: warn\n")
	extstatetest.Apply(t, o.Extensions, extstatetest.DeviceScope(), extensionstate.InstallOp{Source: "path:" + dir})
	extstatetest.Apply(t, o.Extensions, extstatetest.DeviceScope(), extensionstate.SetInstalledFromOp{PackID: packID, ProjectID: projectID})
}
func review(t *testing.T, o *Owner, id string, packs ...string) wire.ProjectRemovalRequest {
	t.Helper()
	a, err := o.Assess(t.Context(), id)
	testutil.FailErr(t, "assess", err)
	return wire.ProjectRemovalRequest{OperationID: uuid.NewString(), AssessmentToken: a.AssessmentToken, RemoveExtensions: packs}
}
func assertInstalled(t *testing.T, o *Owner, id string, want bool) {
	t.Helper()
	state, err := o.Extensions.RemovalState()
	testutil.FailErr(t, "extension state", err)
	found := false
	for _, p := range state.Desired.Packs {
		if p.ID == id {
			found = true
		}
	}
	if found != want {
		t.Fatalf("installed %s = %v, want %v", id, found, want)
	}
}
func addProject(t *testing.T, reg *registry) *project.Project {
	t.Helper()
	id := uuid.NewString()
	p := &project.Project{ID: id, Name: "Other project", Roots: []project.Root{{ID: uuid.NewString(), Path: t.TempDir(), IsPrimary: true, Kind: project.RootKindAttached}}}
	reg.rows[id] = p
	return p
}
func suggestion(t *testing.T, p *project.Project, data string) {
	t.Helper()
	dir := filepath.Join(p.Roots[0].Path, ".paintedwolf")
	testutil.FailErr(t, "mkdir overlay", os.MkdirAll(dir, 0o700))
	testutil.FailErr(t, "write suggestions", os.WriteFile(filepath.Join(dir, "extensions.yaml"), []byte(data), 0o600))
}

func TestAssessmentNeverTreatsUnknownAsAbsent(t *testing.T) {
	for _, mode := range []string{"disabled", "malformed", "missing_root", "unreadable", "broken_manifest_link", "broken_overlay_link"} {
		t.Run(mode, func(t *testing.T) {
			o, reg, id := fixture(t)
			install(t, o, id, "acme/one")
			p := addProject(t, reg)
			switch mode {
			case "disabled":
				o.SuggestionsApply = func(project.Project) bool { return false }
			case "malformed":
				suggestion(t, p, "format: [broken")
			case "missing_root":
				testutil.FailErr(t, "remove root", os.Remove(p.Roots[0].Path))
			case "broken_manifest_link":
				dir := filepath.Join(p.Roots[0].Path, ".paintedwolf")
				testutil.FailErr(t, "create overlay directory", os.MkdirAll(dir, 0o700))
				testutil.FailErr(t, "link missing manifest", os.Symlink(filepath.Join(t.TempDir(), "missing"), filepath.Join(dir, "extensions.yaml")))
			case "broken_overlay_link":
				testutil.FailErr(t, "link missing overlay", os.Symlink(filepath.Join(t.TempDir(), "missing"), filepath.Join(p.Roots[0].Path, ".paintedwolf")))
			case "unreadable":
				suggestion(t, p, "format: 1")
				path := filepath.Join(p.Roots[0].Path, ".paintedwolf", "extensions.yaml")
				testutil.FailErr(t, "remove manifest", os.Remove(path))
				testutil.FailErr(t, "replace with directory", os.Mkdir(path, 0o700))
			}
			a, err := o.Assess(t.Context(), id)
			testutil.FailErr(t, "assess", err)
			if a.Complete || a.Extensions[0].Disposition != "unknown" || a.Checks[0].Reason == "" {
				t.Fatalf("assessment = %+v", a)
			}
		})
	}
}
func TestAssessmentRetainsDeclaredReferences(t *testing.T) {
	o, reg, id := fixture(t)
	install(t, o, id, "acme/one")
	p := addProject(t, reg)
	suggestion(t, p, "format: 1\nsuggest:\n  - id: acme/one\n    source: https://example.com/one.git\n")
	a, err := o.Assess(t.Context(), id)
	testutil.FailErr(t, "assess", err)
	if !a.Complete || a.Extensions[0].Disposition != "retained" {
		t.Fatalf("assessment = %+v", a)
	}
	_, err = o.Remove(t.Context(), id, review(t, o, id, "acme/one"))
	if !errors.Is(err, ErrInvalidSelection) {
		t.Fatalf("selection error = %v", err)
	}
}
func TestFailedDeletionKeepsExtensions(t *testing.T) {
	o, _, id := fixture(t)
	install(t, o, id, "acme/one")
	o.Delete = func(context.Context, string, bool) error {
		return &Failure{Code: "root_busy", Message: "busy", Documents: 2}
	}
	req := review(t, o, id, "acme/one")
	result, err := o.Remove(t.Context(), id, req)
	testutil.FailErr(t, "remove", err)
	if result.ProjectState != "retained" || result.FailureCode != "root_busy" || result.Documents != 2 {
		t.Fatalf("result = %+v", result)
	}
	assertInstalled(t, o, "acme/one", true)
}
func TestRemovalBatchAndExactRetry(t *testing.T) {
	o, _, id := fixture(t)
	install(t, o, id, "acme/one")
	install(t, o, id, "acme/two")
	req := review(t, o, id, "acme/one", "acme/two")
	result, err := o.Remove(t.Context(), id, req)
	testutil.FailErr(t, "remove", err)
	if result.ProjectState != "deleted" || result.CleanupState != "removed" {
		t.Fatalf("result = %+v", result)
	}
	assertInstalled(t, o, "acme/one", false)
	assertInstalled(t, o, "acme/two", false)
	o.Delete = func(context.Context, string, bool) error { t.Fatal("retry repeated deletion"); return nil }
	replay, err := o.Remove(t.Context(), id, req)
	testutil.FailErr(t, "replay", err)
	if replay.CleanupState != "removed" {
		t.Fatalf("replay = %+v", replay)
	}
	req.Force = true
	_, err = o.Remove(t.Context(), id, req)
	if !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("reuse error = %v", err)
	}
}
func TestEvidenceChangesBeforeAndDuringDeletion(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "during"}[during], func(t *testing.T) {
			o, reg, id := fixture(t)
			install(t, o, id, "acme/one")
			req := review(t, o, id, "acme/one")
			change := func() {
				p := addProject(t, reg)
				suggestion(t, p, "format: 1\nsuggest:\n  - id: acme/one\n    source: https://example.com/one.git\n")
			}
			if during {
				o.Delete = func(context.Context, string, bool) error { delete(reg.rows, id); change(); return nil }
			} else {
				change()
			}
			result, err := o.Remove(t.Context(), id, req)
			if during {
				testutil.FailErr(t, "remove", err)
				if result.ProjectState != "deleted" || result.CleanupState != "retained" {
					t.Fatalf("result = %+v", result)
				}
			} else if !errors.Is(err, ErrAssessmentChanged) {
				t.Fatalf("stale error = %v", err)
			}
			assertInstalled(t, o, "acme/one", true)
		})
	}
}
func TestDeleteWithoutCleanupDoesNotRequireAssessment(t *testing.T) {
	o, reg, id := fixture(t)
	o.Extensions = nil
	reg.listError = errors.New("inventory unavailable")
	result, err := o.Remove(t.Context(), id, wire.ProjectRemovalRequest{OperationID: uuid.NewString()})
	testutil.FailErr(t, "remove", err)
	if result.ProjectState != "deleted" || result.CleanupState != "not_requested" {
		t.Fatalf("result = %+v", result)
	}
}
func TestInterruptedReceiptSurvivesStoreRecreation(t *testing.T) {
	database := testdbfixture.Open(t, "removals.db")
	store := NewStore(database)
	id := uuid.NewString()
	operation := uuid.NewString()
	result := wire.ProjectRemovalResult{OperationID: operation, ProjectID: id, ProjectState: "deleted", CleanupState: "interrupted", Extensions: []string{"acme/one"}}
	testutil.FailErr(t, "begin receipt", store.begin(t.Context(), record{ProjectID: id, Request: "{}", Result: result}))
	o := &Owner{Store: NewStore(database)}
	replay, err := o.Result(t.Context(), id, operation)
	testutil.FailErr(t, "recover receipt", err)
	if replay.ProjectState != "deleted" || replay.CleanupState != "interrupted" || replay.FailureCode != "removal_interrupted" {
		t.Fatalf("replay = %+v", replay)
	}
	_, err = o.Result(t.Context(), uuid.NewString(), operation)
	if !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("cross-project read = %v", err)
	}
}

func TestAssessmentChecksSecondaryRoots(t *testing.T) {
	o, reg, id := fixture(t)
	install(t, o, id, "acme/one")
	p := addProject(t, reg)
	secondary := project.Root{ID: uuid.NewString(), Path: t.TempDir(), Kind: project.RootKindAttached}
	p.Roots = append(p.Roots, secondary)
	secondaryProject := &project.Project{Roots: []project.Root{secondary}}
	suggestion(t, secondaryProject, "format: 1\nsuggest:\n  - id: acme/one\n    source: https://example.com/one.git\n")
	a, err := o.Assess(t.Context(), id)
	testutil.FailErr(t, "assess secondary root", err)
	if a.Extensions[0].Disposition != "retained" {
		t.Fatalf("secondary reference = %+v", a.Extensions[0])
	}
}

func TestDeviceMutationAfterDeleteRetainsExtensions(t *testing.T) {
	o, reg, id := fixture(t)
	install(t, o, id, "acme/one")
	req := review(t, o, id, "acme/one")
	o.Delete = func(context.Context, string, bool) error {
		delete(reg.rows, id)
		extstatetest.Apply(t, o.Extensions, extstatetest.DeviceScope(), extensionstate.SetPackEnabledOp{PackID: "acme/one", Enabled: false})
		return nil
	}
	result, err := o.Remove(t.Context(), id, req)
	testutil.FailErr(t, "remove", err)
	if result.CleanupState != "retained" {
		t.Fatalf("cleanup = %+v", result)
	}
	assertInstalled(t, o, "acme/one", true)
}

func TestDuplicateConcurrentRequestExecutesOnce(t *testing.T) {
	o, reg, id := fixture(t)
	req := wire.ProjectRemovalRequest{OperationID: uuid.NewString()}
	entered, release := make(chan struct{}), make(chan struct{})
	calls := 0
	o.Delete = func(context.Context, string, bool) error {
		calls++
		close(entered)
		<-release
		delete(reg.rows, id)
		return nil
	}
	type outcome struct {
		result wire.ProjectRemovalResult
		err    error
	}
	results := make(chan outcome, 2)
	go func() { r, e := o.Remove(t.Context(), id, req); results <- outcome{r, e} }()
	<-entered
	go func() { r, e := o.Remove(t.Context(), id, req); results <- outcome{r, e} }()
	close(release)
	for range 2 {
		out := <-results
		testutil.FailErr(t, "concurrent remove", out.err)
		if out.result.ProjectState != "deleted" {
			t.Fatalf("result = %+v", out.result)
		}
	}
	if calls != 1 {
		t.Fatalf("deletion count = %d", calls)
	}
}
