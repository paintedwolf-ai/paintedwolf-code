package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func newSkillsTestManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(store.NewMemory(), nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
}

func writeSkill(t *testing.T, root, rel, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(rel), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	body := "---\nname: " + name + "\ndescription: " + desc + "\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
}

func TestEffectiveSkillsGateMatrix(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeSkill(t, root, settingsoverlay.DirName()+"/skills", "house-style", "House rules for this project.")

	cases := []struct {
		name      string
		deviceOn  bool
		projectOn bool
		wantSkill bool
	}{
		{"device_off", false, true, false},
		{"project_off", true, false, false},
		{"all_on", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newSkillsTestManager(t)
			surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "cs.yaml"))
			testutil.FailErr(t, "surfaces", err)
			if err := surfaces.PutEnabled(map[string]bool{projectcontrib.SurfaceSkills: tc.deviceOn}); err != nil {
				testutil.FailErr(t, "PutEnabled", err)
			}
			project.SetDefaultOpenPolicy(project.TestOpenPolicy())
			reg := project.NewMemoryRegistry()
			p, err := project.CreateWithRoot(ctx, reg, root)
			testutil.FailErr(t, "CreateWithRoot", err)
			if !tc.projectOn {
				if _, err := reg.SetTrustEnabled(ctx, p.ID, map[string]bool{projectcontrib.SurfaceSkills: false}); err != nil {
					testutil.FailErr(t, "SetTrustEnabled", err)
				}
			}
			m.SetProjectRegistry(reg)
			m.SetEffectiveCatalogDeps("", extpacks.Active(), surfaces)
			m.Profiles.SetSkillsGate(&settings.ProjectSurfaceGate{
				Surface:  projectcontrib.SurfaceSkills,
				Surfaces: surfaces,
				Projects: reg,
			})

			loaded, _ := m.Profiles.EffectiveSkills(ctx, p.ID, []string{root})
			found := false
			for _, sk := range loaded {
				if sk.Name == "house-style" && sk.Project {
					found = true
				}
			}
			if found != tc.wantSkill {
				t.Fatalf("project skill present=%v want %v", found, tc.wantSkill)
			}
		})
	}
}

func TestEffectiveSkillsGateOffReadsNothing(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	lycaon := filepath.Join(root, settingsoverlay.DirName(), "skills")
	agents := filepath.Join(root, ".agents", "skills")
	if err := os.MkdirAll(lycaon, 0o755); err != nil {
		testutil.FailErr(t, "mkdir lycaon", err)
	}
	if err := os.MkdirAll(agents, 0o755); err != nil {
		testutil.FailErr(t, "mkdir agents", err)
	}
	if err := os.Chmod(lycaon, 0o000); err != nil {
		testutil.FailErr(t, "chmod lycaon", err)
	}
	if err := os.Chmod(agents, 0o000); err != nil {
		testutil.FailErr(t, "chmod agents", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(lycaon, 0o755)
		_ = os.Chmod(agents, 0o755)
	})

	m := newSkillsTestManager(t)
	surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "cs.yaml"))
	testutil.FailErr(t, "surfaces", err)
	if err := surfaces.PutEnabled(map[string]bool{projectcontrib.SurfaceSkills: false}); err != nil {
		testutil.FailErr(t, "PutEnabled", err)
	}
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(ctx, reg, root)
	testutil.FailErr(t, "CreateWithRoot", err)
	m.SetProjectRegistry(reg)
	m.SetEffectiveCatalogDeps("", extpacks.Active(), surfaces)
	m.Profiles.SetSkillsGate(&settings.ProjectSurfaceGate{
		Surface:  projectcontrib.SurfaceSkills,
		Surfaces: surfaces,
		Projects: reg,
	})

	// With the gate off, DiscoverProject is never called.
	_, diags := m.Profiles.EffectiveSkills(ctx, p.ID, []string{root})
	for _, d := range diags {
		if strings.Contains(d.Message, "approval") || strings.Contains(d.Code, "path") {
			t.Fatalf("unexpected disk diagnostic with gate off: %+v", d)
		}
	}
}

func TestEffectiveSkillsUnwiredGateClosed(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeSkill(t, root, settingsoverlay.DirName()+"/skills", "house-style", "House rules.")
	m := newSkillsTestManager(t)
	// No SetSkillsGate.
	loaded, _ := m.Profiles.EffectiveSkills(ctx, "any", []string{root})
	for _, sk := range loaded {
		if sk.Project {
			t.Fatalf("unwired gate must not load project skills: %#v", sk)
		}
	}
}

func TestEffectiveSkillsShadowsStock(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeSkill(t, root, settingsoverlay.DirName()+"/skills", "verify-a-change", "Project attempt to replace stock.")

	m := newSkillsTestManager(t)
	surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "cs.yaml"))
	testutil.FailErr(t, "surfaces", err)
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(ctx, reg, root)
	testutil.FailErr(t, "CreateWithRoot", err)
	m.SetProjectRegistry(reg)
	moduleRoot := filepath.Join("..", "..")
	m.SetEffectiveCatalogDeps(moduleRoot, nil, surfaces)
	m.Profiles.SetSkillsGate(&settings.ProjectSurfaceGate{
		Surface:  projectcontrib.SurfaceSkills,
		Surfaces: surfaces,
		Projects: reg,
	})

	loaded, diags := m.Profiles.EffectiveSkills(ctx, p.ID, []string{root})
	var verify *skills.Skill
	for i := range loaded {
		if loaded[i].Name == "verify-a-change" {
			verify = &loaded[i]
		}
	}
	if verify == nil {
		t.Fatalf("stock skill missing; diags=%v", diags)
	}
	if verify.Project {
		t.Fatal("device skill must win over project shadow")
	}
	if strings.Contains(verify.Description, "Project attempt") {
		t.Fatal("project body must not replace stock")
	}
	found := false
	for _, d := range diags {
		if d.Code == extpacks.DiagSkillShadowed && d.UnitID == "skills/verify-a-change" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want skill_shadowed; diags=%v", diags)
	}
}

func TestEffectiveSkillsNoTaint(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeSkill(t, root, ".agents/skills", "release-check", "Release checklist.")

	mem := store.NewMemory()
	m := NewManager(mem, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	surfaces, err := settings.NewTrustSurfacesStoreAt(filepath.Join(t.TempDir(), "cs.yaml"))
	testutil.FailErr(t, "surfaces", err)
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(ctx, reg, root)
	testutil.FailErr(t, "CreateWithRoot", err)
	m.SetProjectRegistry(reg)
	m.SetEffectiveCatalogDeps("", extpacks.Active(), surfaces)
	m.Profiles.SetSkillsGate(&settings.ProjectSurfaceGate{
		Surface:  projectcontrib.SurfaceSkills,
		Surfaces: surfaces,
		Projects: reg,
	})

	sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create session", err)

	loaded, _ := m.Profiles.EffectiveSkills(ctx, p.ID, []string{root})
	found := false
	for _, sk := range loaded {
		if sk.Name == "release-check" && sk.Project {
			found = true
		}
	}
	if !found {
		t.Fatal("expected project skill")
	}
	if mem.SessionUntrustedContent(sess.ID) {
		t.Fatal("loading a project skill must not taint the session")
	}
}

func TestDiagSkillShadowedRegistered(t *testing.T) {
	for _, c := range extpacks.AllDiagnosticCodes() {
		if c == extpacks.DiagSkillShadowed {
			return
		}
	}
	t.Fatal("DiagSkillShadowed missing from registry")
}
