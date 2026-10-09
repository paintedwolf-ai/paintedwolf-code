package searchadmin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type replaceFixture struct {
	*searchFixture
	origin *project.Project
	other  *project.Project
}

func newReplaceFixture(t *testing.T) replaceFixture {
	t.Helper()
	f := newSearchFixture(t)
	origin := f.addProject(t, "Origin", "origin-root", map[string]string{
		"a.go": "package a\n\nconst Name = \"old\"\n",
		"b.go": "package b\n\nvar other = \"old\"\n",
	})
	other := f.addProject(t, "Other", "other-root", map[string]string{"c.go": "package c\n\nconst Name = \"old\"\n"})
	return replaceFixture{searchFixture: f, origin: origin, other: other}
}

func (f replaceFixture) preview(t *testing.T, req wire.SearchReplacePreviewRequest) wire.SearchReplacePreviewResponse {
	t.Helper()
	return decodeOK[wire.SearchReplacePreviewResponse](t, f.post(t, "/v1/search/replace/preview", jsonBody(t, req)))
}

func previewFile(t *testing.T, resp wire.SearchReplacePreviewResponse, path string) wire.SearchReplaceFilePreview {
	t.Helper()
	for _, file := range resp.Files {
		if file.Path == path {
			return file
		}
	}
	t.Fatalf("preview has no %s: %+v", path, resp.Files)
	return wire.SearchReplaceFilePreview{}
}

func TestPreviewSearchReplacementNarrowsToOrigin(t *testing.T) {
	f := newReplaceFixture(t)
	scoped := f.preview(t, wire.SearchReplacePreviewRequest{Query: "old", Replacement: "new", OriginProjectID: f.origin.ID})
	if scoped.State != wire.SearchReplacePreviewState(search.ReplacePreviewReady) || len(scoped.Files) != 2 || scoped.Truncated {
		t.Fatalf("scoped preview = %+v", scoped)
	}
	a := previewFile(t, scoped, "a.go")
	if a.RootID != f.origin.Roots[0].ID || a.SHA256 == "" || len(a.Hunks) != 1 {
		t.Fatalf("a.go preview = %+v", a)
	}
	if h := a.Hunks[0]; h.Line != 3 || !strings.Contains(h.Before, "old") || !strings.Contains(h.After, "new") {
		t.Fatalf("a.go hunk = %+v", h)
	}

	global := f.preview(t, wire.SearchReplacePreviewRequest{Query: "old", Replacement: "new", WholeWord: true})
	if len(global.Files) != 3 {
		t.Fatalf("global preview files = %+v", global.Files)
	}
}

func TestPreviewSearchReplacementRefusals(t *testing.T) {
	f := newReplaceFixture(t)
	for _, tc := range []struct {
		name string
		body string
		want wire.ApiErrorCode
	}{
		{"malformed body", `{`, wire.ApiErrorCodeInvalidJson},
		{"blank query", `{"query":" ","replacement":"x"}`, wire.ApiErrorCodeInvalidRequest},
		{"parse error", `{"query":"old kind:","replacement":"x"}`, wire.ApiErrorCodeSearchQueryInvalid},
		{"invalid regex", `{"query":"\"a[\"","replacement":"x","regex":true}`, wire.ApiErrorCodeSearchPatternInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireErrorCode(t, f.post(t, "/v1/search/replace/preview", tc.body), tc.want)
		})
	}
}

func TestApplySearchReplacementWritesSelectedHunks(t *testing.T) {
	f := newReplaceFixture(t)
	preview := f.preview(t, wire.SearchReplacePreviewRequest{Query: "old", Replacement: "new", OriginProjectID: f.origin.ID})
	a, b := previewFile(t, preview, "a.go"), previewFile(t, preview, "b.go")
	req := wire.SearchReplaceApplyRequest{
		OperationID: uuid.NewString(), Query: "old", Replacement: "new", OriginProjectID: f.origin.ID,
		Files: []wire.SearchReplaceApplyFile{
			{RootID: a.RootID, Path: a.Path, SHA256: a.SHA256, Hunks: []int{0}},
			{RootID: b.RootID, Path: b.Path, SHA256: strings.Repeat("0", 64), Hunks: []int{0}},
			{RootID: a.RootID, Path: "missing.go", SHA256: a.SHA256, Hunks: []int{0}},
		},
	}
	resp := decodeOK[wire.SearchReplaceApplyResponse](t, f.post(t, "/v1/search/replace/apply", jsonBody(t, req)))
	if resp.BatchID != req.OperationID || len(resp.Files) != 3 {
		t.Fatalf("apply response = %+v", resp)
	}
	if got := resp.Files[0]; !got.Applied || got.Skipped || got.Matches != 1 {
		t.Fatalf("a.go outcome = %+v", got)
	}
	if got := resp.Files[1]; got.Applied || !got.Skipped || got.Reason != "source_write_conflict" {
		t.Fatalf("stale b.go outcome = %+v", got)
	}
	if got := resp.Files[2]; got.Applied || !got.Skipped || got.Reason != "source_not_found" {
		t.Fatalf("missing file outcome = %+v", got)
	}
	written, err := os.ReadFile(filepath.Join(f.origin.Roots[0].Path, "a.go"))
	testutil.FailErr(t, "read replaced file", err)
	if !strings.Contains(string(written), `"new"`) {
		t.Fatalf("a.go = %q", written)
	}
	untouched, err := os.ReadFile(filepath.Join(f.origin.Roots[0].Path, "b.go"))
	testutil.FailErr(t, "read stale file", err)
	if !strings.Contains(string(untouched), `"old"`) {
		t.Fatalf("stale b.go was rewritten: %q", untouched)
	}

	replay := decodeOK[wire.SearchReplaceApplyResponse](t, f.post(t, "/v1/search/replace/apply", jsonBody(t, req)))
	if replay.BatchID != resp.BatchID || len(replay.Files) != 3 || !replay.Files[0].Applied {
		t.Fatalf("replay = %+v", replay)
	}

	req.Replacement = "different"
	requireErrorCode(t, f.post(t, "/v1/search/replace/apply", jsonBody(t, req)), wire.ApiErrorCodeSourceWriteConflict)
	if len(f.sourceErrors) != 1 || !errors.Is(f.sourceErrors[0], project.ErrSourceMutationConflict) {
		t.Fatalf("source errors = %v", f.sourceErrors)
	}
}

func TestApplySearchReplacementRefusals(t *testing.T) {
	f := newReplaceFixture(t)
	apply := func(fields string) string {
		return fmt.Sprintf(`{"operation_id":%q,"replacement":"new","files":[]%s}`, uuid.NewString(), fields)
	}
	origin := fmt.Sprintf(`,"origin_project_id":%q`, f.origin.ID)
	for _, tc := range []struct {
		name string
		body string
		want wire.ApiErrorCode
	}{
		{"malformed body", `{"query":1}`, wire.ApiErrorCodeInvalidRequest},
		{"blank query", apply(`,"query":" "` + origin), wire.ApiErrorCodeInvalidRequest},
		{"operation id not a uuid", `{"operation_id":"op-1","query":"old","replacement":"new","files":[]` + origin + `}`, wire.ApiErrorCodeInvalidRequest},
		{"origin required", apply(`,"query":"old"`), wire.ApiErrorCodeInvalidRequest},
		{"unknown origin", apply(`,"query":"old","origin_project_id":"missing-project"`), wire.ApiErrorCodeProjectNotFound},
		{"parse error", apply(`,"query":"old kind:"` + origin), wire.ApiErrorCodeSearchQueryInvalid},
		{"file-only query", apply(`,"query":"kind:file old"` + origin), wire.ApiErrorCodeInvalidRequest},
		{"query scoped to another project", apply(`,"query":"project:other old"` + origin), wire.ApiErrorCodeInvalidRequest},
		{"ambiguous replacement pattern", apply(`,"query":"old OR older"` + origin), wire.ApiErrorCodeSearchPatternInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireErrorCode(t, f.post(t, "/v1/search/replace/apply", tc.body), tc.want)
		})
	}
	if len(f.sourceErrors) != 0 {
		t.Fatalf("refusals reached the source error writer: %v", f.sourceErrors)
	}
}

func TestReplaceStoreMapsSourceErrors(t *testing.T) {
	other := errors.New("other")
	for _, tc := range []struct {
		in, want error
	}{
		{nil, nil},
		{project.ErrSourceWriteConflict, search.ErrReplaceWriteConflict},
		{fmt.Errorf("wrapped: %w", project.ErrSourceWriteTooLarge), search.ErrReplaceTooLarge},
		{project.ErrSourceBinary, search.ErrReplaceBinary},
		{project.ErrSourceNotFound, search.ErrReplaceNotFound},
		{project.ErrSourcePathDenied, search.ErrReplacePathDenied},
		{other, other},
	} {
		if got := mapReplaceStoreErr(tc.in); !errors.Is(got, tc.want) {
			t.Fatalf("mapReplaceStoreErr(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
