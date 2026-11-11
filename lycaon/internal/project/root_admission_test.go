package project_test

import (
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceworkspace"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/rootfacts"
)

func TestRootAdmissionFollowsTheStandingWriteRootCheck(t *testing.T) {
	facts := rootfacts.Install(t)
	if admitted, code := (project.Root{Path: facts.Work}).Admission(); !admitted || code != "" {
		t.Fatalf("work folder admission = (%v, %q), want admitted", admitted, code)
	}
	for _, root := range facts.EditorOnly() {
		admitted, code := (project.Root{Path: root.Path}).Admission()
		if admitted || code != root.Code {
			t.Errorf("%s admission = (%v, %q), want (false, %q)", root.Name, admitted, code, root.Code)
		}
	}
}

func TestAgentReadingsHoldAdmittedRootsOnly(t *testing.T) {
	facts := rootfacts.Install(t)
	primary := true
	params := project.CreateParams{Roots: []project.AttachRootParams{{Path: facts.ContainsState, IsPrimary: &primary}, {Path: facts.Work}}}
	for _, root := range facts.EditorOnly()[1:] {
		params.Roots = append(params.Roots, project.AttachRootParams{Path: root.Path})
	}
	p, err := project.NewMemoryRegistry().Create(t.Context(), params)
	testutil.FailErr(t, "create project", err)
	work := rootByPath(t, p, facts.Work)

	refs := project.RootRefsFrom(p)
	if len(refs) != 1 || refs[0].ID != work.ID || !refs[0].IsPrimary {
		t.Fatalf("RootRefsFrom = %+v, want only the work root as primary", refs)
	}
	if got := project.RootPaths(p); !slices.Equal(got, []string{facts.Work}) {
		t.Fatalf("RootPaths = %v, want [%s]", got, facts.Work)
	}
	if got := project.PrimaryRootPath(p); got != facts.Work {
		t.Fatalf("PrimaryRootPath = %q, want the first admitted root", got)
	}
	if got := project.AttachedPrimaryPath(p); got != facts.ContainsState {
		t.Fatalf("AttachedPrimaryPath = %q, want the attached primary", got)
	}
	if id, path, err := project.ResolveWorkspaceRoot(p, ""); err != nil || id != work.ID || path != facts.Work {
		t.Fatalf("ResolveWorkspaceRoot(\"\") = (%q, %q, %v), want the work root", id, path, err)
	}
	home := rootByPath(t, p, facts.ContainsState)
	if _, _, err := project.ResolveWorkspaceRoot(p, home.ID); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("ResolveWorkspaceRoot(editor-only) err = %v, want ErrNotFound", err)
	}

	source := p.SourceRoots()
	if len(source) != len(p.Roots) || source[0].ID != home.ID || !source[0].IsPrimary {
		t.Fatalf("SourceRoots = %+v, want every root with the attached primary first", source)
	}
	if got, want := p.WorkspaceID(), sourceworkspace.ID(p.ID, source); got != want {
		t.Fatalf("WorkspaceID = %q, want the identity of every attached root %q", got, want)
	}
}

func TestProjectWireStatesAdmission(t *testing.T) {
	facts := rootfacts.Install(t)
	params := project.CreateParams{Roots: []project.AttachRootParams{{Path: facts.Work}}}
	for _, root := range facts.EditorOnly() {
		params.Roots = append(params.Roots, project.AttachRootParams{Path: root.Path})
	}
	p, err := project.NewMemoryRegistry().Create(t.Context(), params)
	testutil.FailErr(t, "create project", err)
	want := map[string]string{facts.Work: ""}
	for _, root := range facts.EditorOnly() {
		want[root.Path] = root.Code
	}
	for _, root := range project.ToAPI(p).Roots {
		code, ok := want[root.Path]
		if !ok {
			t.Fatalf("unexpected wire root %q", root.Path)
		}
		switch {
		case code == "" && (!root.Admitted || root.AdmissionCode != nil):
			t.Errorf("%s: admitted=%v code=%v, want admitted with no code", root.Path, root.Admitted, root.AdmissionCode)
		case code != "" && (root.Admitted || root.AdmissionCode == nil || *root.AdmissionCode != code):
			t.Errorf("%s: admitted=%v code=%v, want editor-only with %q", root.Path, root.Admitted, root.AdmissionCode, code)
		}
	}
}

func TestAllAdmittedProjectReadingsAgree(t *testing.T) {
	facts := rootfacts.Install(t)
	other := facts.Work + "-other"
	testutil.FailErr(t, "mkdir", os.MkdirAll(other, 0o700))
	p, err := project.NewMemoryRegistry().Create(t.Context(), project.CreateParams{Roots: []project.AttachRootParams{{Path: facts.Work}, {Path: other}}})
	testutil.FailErr(t, "create project", err)
	if !slices.Equal(project.RootRefsFrom(p), p.SourceRoots()) {
		t.Fatalf("RootRefsFrom = %+v, SourceRoots = %+v; an all-admitted project must read the same", project.RootRefsFrom(p), p.SourceRoots())
	}
	if project.PrimaryRootPath(p) != project.AttachedPrimaryPath(p) {
		t.Fatal("primary paths differ for an all-admitted project")
	}
}

func rootByPath(t *testing.T, p *project.Project, path string) projectroot.RootRef {
	t.Helper()
	for _, root := range p.SourceRoots() {
		if root.Path == path {
			return root
		}
	}
	t.Fatalf("no root at %s", path)
	return projectroot.RootRef{}
}
