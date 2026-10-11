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
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/testutil"
)

const (
	primaryTestSkill   = "release-check"
	secondaryTestSkill = "debug-capture"
)

func writeProjectSkillFixtures(t *testing.T, projectRoot string) {
	t.Helper()
	files := map[string]string{
		filepath.Join(primaryTestSkill, "SKILL.md"): `---
name: release-check
description: Verify release inputs without publishing.
---
# Release check

Read the checklist before proceeding. Stop before git push.
`,
		filepath.Join(primaryTestSkill, "references", "checklist.md"): "# Checklist\n\nVerify version and artifacts.\n",
		filepath.Join(secondaryTestSkill, "SKILL.md"): `---
name: debug-capture
description: Collect local debug evidence without clearing or sending it.
---
# Debug capture

Read the capture map before collecting evidence.
`,
		filepath.Join(secondaryTestSkill, "references", "capture-map.md"): "# Capture map\n\nInspect local logs.\n",
	}
	base := filepath.Join(projectRoot, ".agents", "skills")
	for rel, body := range files {
		path := filepath.Join(base, rel)
		testutil.FailErr(t, "mkdir skill fixture", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write skill fixture", os.WriteFile(path, []byte(body), 0o644))
	}
}

func TestProjectSkillFixturesTreeOnly(t *testing.T) {
	root := t.TempDir()
	writeProjectSkillFixtures(t, root)
	srcRoot := filepath.Join(root, ".agents", "skills")
	entries, err := os.ReadDir(srcRoot)
	testutil.FailErr(t, "read .agents/skills", err)
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) != 2 {
		t.Fatalf(".agents/skills dirs = %v want %s and %s only", names, primaryTestSkill, secondaryTestSkill)
	}
	wantSet := map[string]bool{primaryTestSkill: true, secondaryTestSkill: true}
	for _, n := range names {
		if !wantSet[n] {
			t.Fatalf("unexpected dogfood skill dir %q", n)
		}
	}
}

func TestProjectSkillFixtureDiscovery(t *testing.T) {
	root := t.TempDir()
	writeProjectSkillFixtures(t, root)

	want := map[string]struct {
		desc string
		ref  string
	}{
		primaryTestSkill: {
			desc: "Verify release inputs without publishing.",
			ref:  "references/checklist.md",
		},
		secondaryTestSkill: {
			desc: "Collect local debug evidence without clearing or sending it.",
			ref:  "references/capture-map.md",
		},
	}

	got, notes := skills.DiscoverProject([]string{root})
	if len(notes) != 0 {
		t.Fatalf("discover notes=%v", notes)
	}
	byName := map[string]skills.Skill{}
	for _, sk := range got {
		byName[sk.Name] = sk
	}
	if len(byName) != len(want) {
		t.Fatalf("discovered %d skills want %d; got %v", len(byName), len(want), byName)
	}
	for name, meta := range want {
		sk, ok := byName[name]
		if !ok || !sk.Project {
			t.Fatalf("missing project skill %q: %#v", name, sk)
		}
		if sk.Description != meta.desc {
			t.Fatalf("%s description = %q want %q", name, sk.Description, meta.desc)
		}
		if sk.UnitID != "skills/"+name {
			t.Fatalf("%s UnitID = %q", name, sk.UnitID)
		}
		if len(sk.Resources) != 1 || sk.Resources[0] != meta.ref {
			t.Fatalf("%s resources = %#v want [%q]", name, sk.Resources, meta.ref)
		}
		// Resource listing does not host-read reference contents into the body.
		refBytes, err := os.ReadFile(filepath.Join(sk.Dir, filepath.FromSlash(meta.ref)))
		testutil.FailErr(t, "read reference", err)
		if len(refBytes) == 0 {
			t.Fatalf("%s reference empty", name)
		}
		if strings.Contains(sk.Body, "VERSION / CHANGELOG") || strings.Contains(sk.Body, "Typical file") {
			t.Fatalf("%s body must not inline reference tables", name)
		}
		assertProjectSkillBodySafe(t, name, sk.Body)
	}
}

func assertProjectSkillBodySafe(t *testing.T, name, body string) {
	t.Helper()
	lower := strings.ToLower(body)
	if strings.Contains(lower, "allowed-tools") {
		t.Fatalf("%s body must omit allowed-tools", name)
	}
	if strings.Contains(body, "#!/") || strings.Contains(body, "scripts/") {
		t.Fatalf("%s body must not ship executable script directives", name)
	}
	if strings.Contains(body, "./task debug:clear-all") || strings.Contains(body, "./task db:wipe") {
		t.Fatalf("%s body must not instruct running clear/wipe tasks", name)
	}
	if strings.Contains(body, ":fresh") && !strings.Contains(lower, "requiring explicit") && !strings.Contains(lower, "forbidden") {
		t.Fatalf("%s body mentions :fresh without requiring explicit approval", name)
	}
	if strings.Contains(body, "git push") && !strings.Contains(body, "Stop before") {
		t.Fatalf("%s body must not treat git push as an unconditional step", name)
	}
}

func TestProjectSkillFixtureGateMatrix(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeProjectSkillFixtures(t, root)

	cases := []struct {
		name      string
		deviceOn  bool
		projectOn bool
		want      bool
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
			found := map[string]bool{}
			for _, sk := range loaded {
				if sk.Project && (sk.Name == primaryTestSkill || sk.Name == secondaryTestSkill) {
					found[sk.Name] = true
				}
			}
			if tc.want {
				if len(found) != 2 {
					t.Fatalf("want both project skills; found %v", found)
				}
			} else if len(found) != 0 {
				t.Fatalf("gate off must hide project skills; found %v", found)
			}
		})
	}
}

func TestProjectSkillFixtureDeviceWinsShadow(t *testing.T) {
	root := t.TempDir()
	writeProjectSkillFixtures(t, root)

	device := []skills.Skill{{
		Name:        primaryTestSkill,
		Description: "Device winner.",
		Body:        "device-body",
		UnitID:      "skills/" + primaryTestSkill,
		PackID:      "painted-wolf/platform",
	}}
	projectSkills, _ := skills.DiscoverProject([]string{root})
	merged, diags := extpacks.CombineSkills(device, nil, projectSkills, nil)
	var prep *skills.Skill
	for i := range merged {
		if merged[i].Name == primaryTestSkill {
			prep = &merged[i]
		}
	}
	if prep == nil || prep.Body != "device-body" || prep.Project {
		t.Fatalf("device must win %s collision: %#v", primaryTestSkill, prep)
	}
	found := false
	for _, d := range diags {
		if d.Code == extpacks.DiagSkillShadowed && d.UnitID == "skills/"+primaryTestSkill {
			found = true
		}
	}
	if !found {
		t.Fatalf("want skill_shadowed for %s; diags=%v", primaryTestSkill, diags)
	}
	// Sibling dogfood skill still merges when it does not collide.
	var sawDebug bool
	for _, sk := range merged {
		if sk.Name == secondaryTestSkill && sk.Project {
			sawDebug = true
		}
	}
	if !sawDebug {
		t.Fatalf("%s must remain after %s collision", secondaryTestSkill, primaryTestSkill)
	}
}
