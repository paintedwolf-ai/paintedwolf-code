package search

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

func replaceFixture(t *testing.T) (*project.Project, string) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"a.go":  "package a\n\nconst Name = \"old\"\nfunc Hello() {}\n",
		"b.go":  "package b\n\nconst Name = \"old\"\n",
		"c.txt": "nope\n",
	}
	for rel, body := range files {
		abs := filepath.Join(dir, rel)
		testutil.FailErr(t, "write "+rel, os.WriteFile(abs, []byte(body), 0o644))
	}
	p, err := project.CreateWithRoot(context.Background(), project.NewMemoryRegistry(), dir)
	testutil.FailErr(t, "create project", err)
	return p, dir
}

type testReplaceStore struct {
	project *project.Project
}

func (s testReplaceStore) ReadReplaceContent(rootID, path string) (ReplaceContentRead, error) {
	read, err := project.ReadProjectSource(s.project, project.SourceReadRequest{
		Path: path, RootID: rootID,
	})
	if err != nil {
		return ReplaceContentRead{}, err
	}
	return ReplaceContentRead{
		Content: read.Content, SHA256: read.SHA256, Encoding: read.Encoding,
		Truncated: read.OverLimit || read.Binary,
	}, nil
}

func TestPreviewReplaceHunksAndSHA(t *testing.T) {
	p, dir := replaceFixture(t)
	rootID := p.Roots[0].ID
	prev, err := settledReplacePreview(context.Background(), ReplacePreviewRequest{
		Query:       TextExpr{Text: "old"},
		Replacement: "new",
		Roots:       []CodeRoot{{ProjectID: p.ID, RootID: rootID, Path: dir}},
	})
	testutil.FailErr(t, "preview", err)
	if prev.Truncated {
		t.Fatal("small fixture should not truncate")
	}
	if len(prev.Files) != 2 {
		t.Fatalf("files = %d, want 2", len(prev.Files))
	}
	for _, f := range prev.Files {
		if f.SHA256 == "" || len(f.Hunks) == 0 {
			t.Fatalf("bad file %+v", f)
		}
		for _, h := range f.Hunks {
			if h.Line < 1 || h.Before == "" || h.After == "" {
				t.Fatalf("bad hunk %+v", h)
			}
			if !strings.Contains(h.After, "new") {
				t.Fatalf("after missing replacement: %q", h.After)
			}
		}
	}
}

func TestPreviewReplaceDecodesSelfIdentifyingEncodings(t *testing.T) {
	for _, encoding := range testutil.SelfIdentifyingTextEncodings() {
		t.Run(encoding, func(t *testing.T) {
			dir := t.TempDir()
			raw := testutil.EncodeTextFixture(t, "before old after\n", encoding)
			testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(dir, "document.txt"), raw, 0o644))
			preview, err := settledReplacePreview(context.Background(), ReplacePreviewRequest{
				Query: TextExpr{Text: "old"}, Replacement: "new",
				Roots: []CodeRoot{{ProjectID: "p1", RootID: "r1", Path: dir}},
			})
			testutil.FailErr(t, "preview", err)
			if len(preview.Files) != 1 || preview.Files[0].SHA256 != textfile.SHA256(raw) ||
				len(preview.Files[0].Hunks) != 1 || preview.Files[0].Hunks[0].After != "before new after" {
				t.Fatalf("preview = %+v", preview)
			}
		})
	}
}

func TestPlanReplaceSHAMismatchSkipsOtherPlans(t *testing.T) {
	p, dir := replaceFixture(t)
	rootID := p.Roots[0].ID
	prev, err := settledReplacePreview(context.Background(), ReplacePreviewRequest{
		Query:       TextExpr{Text: "old"},
		Replacement: "new",
		Roots:       []CodeRoot{{ProjectID: p.ID, RootID: rootID, Path: dir}},
	})
	testutil.FailErr(t, "preview", err)

	var aFile, bFile *ReplaceFilePreview
	for i := range prev.Files {
		switch prev.Files[i].Path {
		case "a.go":
			aFile = &prev.Files[i]
		case "b.go":
			bFile = &prev.Files[i]
		}
	}
	if aFile == nil || bFile == nil {
		t.Fatalf("missing files: %+v", prev.Files)
	}

	// Corrupt a.go on disk so its sha no longer matches.
	testutil.FailErr(t, "touch a.go", os.WriteFile(filepath.Join(dir, "a.go"), []byte("changed\n"), 0o644))

	indexes := make([]int, len(bFile.Hunks))
	for i := range bFile.Hunks {
		indexes[i] = i
	}
	result, err := PlanReplace(ReplacePlanRequest{
		Query:       TextExpr{Text: "old"},
		Replacement: "new",
		Store:       testReplaceStore{project: p},
		Files: []ReplaceApplyFile{
			{RootID: rootID, Path: "a.go", SHA256: aFile.SHA256, HunkIndexes: []int{0}},
			{RootID: rootID, Path: "b.go", SHA256: bFile.SHA256, HunkIndexes: indexes},
		},
	})
	testutil.FailErr(t, "plan", err)
	var aOut, bOut *ReplaceFileOutcome
	for i := range result.Files {
		switch result.Files[i].Path {
		case "a.go":
			aOut = &result.Files[i]
		case "b.go":
			bOut = &result.Files[i]
		}
	}
	if aOut == nil || !aOut.Skipped || aOut.Reason != "source_write_conflict" {
		t.Fatalf("a.go outcome = %+v", aOut)
	}
	if bOut == nil || !bOut.Applied {
		t.Fatalf("b.go outcome = %+v", bOut)
	}
	if len(result.Writes) != 1 || result.Writes[0].Path != "b.go" || !strings.Contains(result.Writes[0].Content, "new") {
		t.Fatalf("writes = %+v", result.Writes)
	}
}

func TestPlanReplaceKeptHunkSubset(t *testing.T) {
	p, dir := replaceFixture(t)
	rootID := p.Roots[0].ID
	testutil.FailErr(t, "rewrite a.go", os.WriteFile(filepath.Join(dir, "a.go"), []byte("one\ntwo\none\n"), 0o644))
	prev, err := settledReplacePreview(context.Background(), ReplacePreviewRequest{
		Query:       TextExpr{Text: "one"},
		Replacement: "ONE",
		Roots:       []CodeRoot{{ProjectID: p.ID, RootID: rootID, Path: dir}},
	})
	testutil.FailErr(t, "preview", err)
	var a *ReplaceFilePreview
	for i := range prev.Files {
		if prev.Files[i].Path == "a.go" {
			a = &prev.Files[i]
		}
	}
	if a == nil || len(a.Hunks) != 2 {
		t.Fatalf("want 2 hunks, got %+v", a)
	}
	result, err := PlanReplace(ReplacePlanRequest{
		Query:       TextExpr{Text: "one"},
		Replacement: "ONE",
		Store:       testReplaceStore{project: p},
		Files: []ReplaceApplyFile{
			{RootID: rootID, Path: "a.go", SHA256: a.SHA256, HunkIndexes: []int{0}},
		},
	})
	testutil.FailErr(t, "plan", err)
	if !result.Files[0].Applied || result.Files[0].Matches != 1 {
		t.Fatalf("outcome = %+v", result.Files[0])
	}
	if len(result.Writes) != 1 || result.Writes[0].Content != "ONE\ntwo\none\n" {
		t.Fatalf("writes = %+v", result.Writes)
	}
}

func TestPlanReplacePreservesSelfIdentifyingEncodings(t *testing.T) {
	for _, encoding := range testutil.SelfIdentifyingTextEncodings() {
		t.Run(encoding, func(t *testing.T) {
			p, dir := replaceFixture(t)
			rootID := p.Roots[0].ID
			disk := testutil.EncodeTextFixture(t, "old\n", encoding)
			path := filepath.Join(dir, "wide.txt")
			testutil.FailErr(t, "write fixture", os.WriteFile(path, disk, 0o644))

			read, err := project.ReadProjectSource(p, project.SourceReadRequest{Path: "wide.txt", RootID: rootID})
			testutil.FailErr(t, "read fixture", err)
			result, err := PlanReplace(ReplacePlanRequest{
				Query:       TextExpr{Text: "old"},
				Replacement: "new",
				Store:       testReplaceStore{project: p},
				Files: []ReplaceApplyFile{{
					RootID: rootID, Path: "wide.txt", SHA256: read.SHA256, HunkIndexes: []int{0},
				}},
			})
			testutil.FailErr(t, "plan replace", err)
			if len(result.Files) != 1 || !result.Files[0].Applied {
				t.Fatalf("replace result = %+v", result)
			}
			want := testutil.EncodeTextFixture(t, "new\n", encoding)
			if len(result.Writes) != 1 || !bytes.Equal(result.Writes[0].Before, disk) ||
				!bytes.Equal(result.Writes[0].After, want) || result.Writes[0].AfterSHA256 != textfile.SHA256(want) {
				t.Fatalf("planned write = %+v", result.Writes)
			}
		})
	}
}

func TestPreviewReplaceRegexCapture(t *testing.T) {
	p, dir := replaceFixture(t)
	rootID := p.Roots[0].ID
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.go"), []byte("v1\nv2\n"), 0o644))
	prev, err := settledReplacePreview(context.Background(), ReplacePreviewRequest{
		Query:       TextExpr{Text: `v(\d+)`},
		Replacement: "V$1",
		Flags:       MatchFlags{Regex: true},
		Roots:       []CodeRoot{{ProjectID: p.ID, RootID: rootID, Path: dir}},
	})
	testutil.FailErr(t, "preview", err)
	var a *ReplaceFilePreview
	for i := range prev.Files {
		if prev.Files[i].Path == "a.go" {
			a = &prev.Files[i]
		}
	}
	if a == nil || len(a.Hunks) < 1 {
		t.Fatalf("hunks = %+v", a)
	}
	if a.Hunks[0].After != "V1" {
		t.Fatalf("after = %q", a.Hunks[0].After)
	}
}

func TestPreviewReplaceMultiLineLiteral(t *testing.T) {
	p, dir := replaceFixture(t)
	rootID := p.Roots[0].ID
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.go"), []byte("alpha\nbeta\ngamma\n"), 0o644))
	prev, err := settledReplacePreview(context.Background(), ReplacePreviewRequest{
		Query:       TextExpr{Text: "alpha\nbeta"},
		Replacement: "one",
		Flags:       MatchFlags{CaseSensitive: true},
		Roots:       []CodeRoot{{ProjectID: p.ID, RootID: rootID, Path: dir}},
	})
	testutil.FailErr(t, "preview", err)
	var a *ReplaceFilePreview
	for i := range prev.Files {
		if prev.Files[i].Path == "a.go" {
			a = &prev.Files[i]
		}
	}
	if a == nil || len(a.Hunks) != 1 {
		t.Fatalf("hunks = %+v", a)
	}
	h := a.Hunks[0]
	if h.Line != 1 || h.EndLine != 2 || h.Before != "alpha\nbeta" || h.After != "one" {
		t.Fatalf("hunk = %+v", h)
	}
	result, err := PlanReplace(ReplacePlanRequest{
		Query:       TextExpr{Text: "alpha\nbeta"},
		Replacement: "one",
		Flags:       MatchFlags{CaseSensitive: true},
		Store:       testReplaceStore{project: p},
		Files: []ReplaceApplyFile{
			{RootID: rootID, Path: "a.go", SHA256: a.SHA256, HunkIndexes: []int{0}},
		},
	})
	testutil.FailErr(t, "plan", err)
	if !result.Files[0].Applied || len(result.Writes) != 1 || result.Writes[0].Content != "one\ngamma\n" {
		t.Fatalf("result = %+v", result)
	}
}

func TestPreviewReplaceMultiLineRegex(t *testing.T) {
	p, dir := replaceFixture(t)
	rootID := p.Roots[0].ID
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.go"), []byte("keep\nstart\nmid\nend\nkeep\n"), 0o644))
	prev, err := settledReplacePreview(context.Background(), ReplacePreviewRequest{
		Query:       TextExpr{Text: `(?s)start.*end`},
		Replacement: "gone",
		Flags:       MatchFlags{Regex: true},
		Roots:       []CodeRoot{{ProjectID: p.ID, RootID: rootID, Path: dir}},
	})
	testutil.FailErr(t, "preview", err)
	var a *ReplaceFilePreview
	for i := range prev.Files {
		if prev.Files[i].Path == "a.go" {
			a = &prev.Files[i]
		}
	}
	if a == nil || len(a.Hunks) != 1 {
		t.Fatalf("hunks = %+v", a)
	}
	h := a.Hunks[0]
	if h.Line != 2 || h.EndLine != 4 || h.After != "gone" || h.Before != "start\nmid\nend" {
		t.Fatalf("hunk = %+v", h)
	}
}

func TestPreviewReplaceLineAnchorsStayPerLine(t *testing.T) {
	p, dir := replaceFixture(t)
	rootID := p.Roots[0].ID
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.go"), []byte("top\nfoo mid\nfoo end\n"), 0o644))
	prev, err := settledReplacePreview(context.Background(), ReplacePreviewRequest{
		Query:       TextExpr{Text: `^foo`},
		Replacement: "bar",
		Flags:       MatchFlags{Regex: true},
		Roots:       []CodeRoot{{ProjectID: p.ID, RootID: rootID, Path: dir}},
	})
	testutil.FailErr(t, "preview", err)
	var a *ReplaceFilePreview
	for i := range prev.Files {
		if prev.Files[i].Path == "a.go" {
			a = &prev.Files[i]
		}
	}
	if a == nil || len(a.Hunks) != 2 {
		t.Fatalf("want line-anchored matches on both lines, got %+v", a)
	}
	if a.Hunks[0].Line != 2 || a.Hunks[1].Line != 3 {
		t.Fatalf("hunk lines = %+v", a.Hunks)
	}
}

func TestPreviewReplaceContextClassification(t *testing.T) {
	p, dir := replaceFixture(t)
	rootID := p.Roots[0].ID
	src := "package a\n\n// old comment\nconst S = \"old text\"\nvar old = 1\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "a.go"), []byte(src), 0o644))
	prev, err := settledReplacePreview(context.Background(), ReplacePreviewRequest{
		Query:       TextExpr{Text: "old"},
		Replacement: "new",
		Flags:       MatchFlags{CaseSensitive: true, WholeWord: true},
		Roots:       []CodeRoot{{ProjectID: p.ID, RootID: rootID, Path: dir}},
	})
	testutil.FailErr(t, "preview", err)
	var a *ReplaceFilePreview
	for i := range prev.Files {
		if prev.Files[i].Path == "a.go" {
			a = &prev.Files[i]
		}
	}
	if a == nil || len(a.Hunks) != 3 {
		t.Fatalf("hunks = %+v", a)
	}
	wantContexts := []api.SearchReplaceHunkContext{
		api.SearchReplaceContextCommentOrString,
		api.SearchReplaceContextCommentOrString,
		api.SearchReplaceContextCode,
	}
	for i, h := range a.Hunks {
		if h.Context != wantContexts[i] {
			t.Fatalf("hunk %d (line %d) context = %q, want %q", i, h.Line, h.Context, wantContexts[i])
		}
	}
}
