package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func writeProjectSkill(t *testing.T, root, relDir, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(relDir), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir skill", err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write SKILL.md", err)
	}
	return path
}

func skillBody(name, desc, body string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n" + body
}

func TestDiscoverProjectBothDirectories(t *testing.T) {
	root := t.TempDir()
	writeProjectSkill(t, root, settingsoverlay.DirName()+"/skills", "house-style", skillBody("house-style", "House rules.", "A\n"))
	writeProjectSkill(t, root, ".agents/skills", "release-check", skillBody("release-check", "Release steps.", "B\n"))

	got, notes := DiscoverProject([]string{root})
	if len(notes) != 0 {
		t.Fatalf("notes=%v", notes)
	}
	byName := map[string]Skill{}
	for _, sk := range got {
		byName[sk.Name] = sk
	}
	if sk, ok := byName["house-style"]; !ok || !sk.Project || !sk.UserProvided || sk.UnitID != "skills/house-style" {
		t.Fatalf("house-style = %#v", sk)
	}
	if sk, ok := byName["release-check"]; !ok || !sk.Project || !sk.UserProvided || sk.UnitID != "skills/release-check" {
		t.Fatalf("release-check = %#v", sk)
	}
}

func TestDiscoverProjectLycaonBeatsAgents(t *testing.T) {
	root := t.TempDir()
	writeProjectSkill(t, root, settingsoverlay.DirName()+"/skills", "same", skillBody("same", "native", "native\n"))
	writeProjectSkill(t, root, ".agents/skills", "same", skillBody("same", "agents", "agents\n"))

	got, notes := DiscoverProject([]string{root})
	if len(got) != 1 || !strings.HasPrefix(got[0].Body, "native") {
		t.Fatalf("got=%#v", got)
	}
	found := false
	for _, n := range notes {
		if n.Note == NoteShadowed && n.Name == "same" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want shadow note, notes=%v", notes)
	}
}

func TestDiscoverProjectMultiRoot(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	writeProjectSkill(t, a, settingsoverlay.DirName()+"/skills", "from-a", skillBody("from-a", "a", "a\n"))
	writeProjectSkill(t, b, settingsoverlay.DirName()+"/skills", "from-b", skillBody("from-b", "b", "b\n"))
	writeProjectSkill(t, b, settingsoverlay.DirName()+"/skills", "from-a", skillBody("from-a", "b-copy", "lose\n"))

	got, notes := DiscoverProject([]string{a, b})
	byName := map[string]Skill{}
	for _, sk := range got {
		byName[sk.Name] = sk
	}
	if len(byName) != 2 {
		t.Fatalf("want 2 skills, got %#v notes=%v", got, notes)
	}
	if !strings.HasPrefix(byName["from-a"].Body, "a") {
		t.Fatalf("first root must win: %#v", byName["from-a"])
	}
}

func TestDiscoverProjectIgnoresNonSkills(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, settingsoverlay.DirName(), "skills")
	if err := os.MkdirAll(filepath.Join(base, "ok"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	if err := os.WriteFile(filepath.Join(base, "README.md"), []byte("nope"), 0o644); err != nil {
		testutil.FailErr(t, "readme", err)
	}
	if err := os.MkdirAll(filepath.Join(base, "ok", "scripts"), 0o755); err != nil {
		testutil.FailErr(t, "scripts", err)
	}
	if err := os.MkdirAll(filepath.Join(base, "nested", "sub"), 0o755); err != nil {
		testutil.FailErr(t, "nested", err)
	}
	if err := os.WriteFile(filepath.Join(base, "nested", "sub", "SKILL.md"), []byte(skillBody("sub", "d", "")), 0o644); err != nil {
		testutil.FailErr(t, "nested skill", err)
	}
	writeProjectSkill(t, root, settingsoverlay.DirName()+"/skills", "ok", skillBody("ok", "ok skill", "body\n"))

	got, notes := DiscoverProject([]string{root})
	if len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("got=%#v notes=%v", got, notes)
	}
}

func TestDiscoverProjectSymlinkDirRefused(t *testing.T) {
	root := t.TempDir()
	real := t.TempDir()
	writeProjectSkill(t, real, "x", "linked", skillBody("linked", "d", "secret\n"))
	linkParent := filepath.Join(root, settingsoverlay.DirName(), "skills")
	if err := os.MkdirAll(linkParent, 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	if err := os.Symlink(filepath.Join(real, "x", "linked"), filepath.Join(linkParent, "linked")); err != nil {
		t.Skip("symlink unavailable")
	}
	got, _ := DiscoverProject([]string{root})
	if len(got) != 0 {
		t.Fatalf("symlinked skill dir must be refused: %#v", got)
	}
}

func TestDiscoverProjectSymlinkSkillMDRefused(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(target, []byte("SHOULD-NOT-APPEAR"), 0o600); err != nil {
		testutil.FailErr(t, "secret", err)
	}
	dir := filepath.Join(root, ".agents", "skills", "x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "SKILL.md")); err != nil {
		t.Skip("symlink unavailable")
	}
	got, _ := DiscoverProject([]string{root})
	if len(got) != 0 {
		t.Fatalf("symlinked SKILL.md must be refused: %#v", got)
	}
	for _, sk := range got {
		if strings.Contains(sk.Body, "SHOULD-NOT-APPEAR") || strings.Contains(sk.Description, "SHOULD-NOT-APPEAR") {
			t.Fatal("target bytes leaked into skill")
		}
	}
}

func TestDiscoverProjectOversizeBeforeRead(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, settingsoverlay.DirName(), "skills", "huge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	path := filepath.Join(dir, "SKILL.md")
	f, err := os.Create(path)
	testutil.FailErr(t, "create", err)
	if err := f.Truncate(100 << 20); err != nil {
		testutil.FailErr(t, "truncate", err)
	}
	_ = f.Close()

	got, notes := DiscoverProject([]string{root})
	if len(got) != 0 {
		t.Fatalf("oversize must not load: %#v", got)
	}
	found := false
	for _, n := range notes {
		if n.Note == NoteTooLarge {
			found = true
		}
	}
	if !found {
		t.Fatalf("want too_large, notes=%v", notes)
	}
}

func TestDiscoverProjectMalformedFailSoft(t *testing.T) {
	root := t.TempDir()
	writeProjectSkill(t, root, settingsoverlay.DirName()+"/skills", "good", skillBody("good", "ok", "g\n"))
	writeProjectSkill(t, root, settingsoverlay.DirName()+"/skills", "bad", "---\nname: bad\n---\n")

	got, notes := DiscoverProject([]string{root})
	if len(got) != 1 || got[0].Name != "good" {
		t.Fatalf("got=%#v", got)
	}
	found := false
	for _, n := range notes {
		if n.Note == NoteFieldMissing && n.Name == "bad" {
			found = true
		}
	}
	if !found {
		t.Fatalf("notes=%v", notes)
	}
}

func TestDiscoverProjectCap(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 130; i++ {
		name := "p" + pad3(i)
		writeProjectSkill(t, root, settingsoverlay.DirName()+"/skills", name, skillBody(name, "d", "b\n"))
	}
	got, notes := DiscoverProject([]string{root})
	if len(got) != CatalogMax {
		t.Fatalf("loaded=%d want %d", len(got), CatalogMax)
	}
	found := false
	for _, n := range notes {
		if n.Note == NoteCatalogFull {
			found = true
		}
	}
	if !found {
		t.Fatalf("want catalog_full, notes=%v", notes)
	}
}

func TestMergeAdditiveStockWins(t *testing.T) {
	stock := []Skill{{Name: "verify-a-change", UnitID: "skills/verify-a-change", Body: "stock"}}
	project := []Skill{{
		Name: "verify-a-change", UnitID: "skills/verify-a-change", Body: "project",
		Dir: "/tmp/proj/" + settingsoverlay.Rel("skills/verify-a-change"), Project: true, UserProvided: true,
	}}
	merged, notes := MergeAdditive(stock, project)
	if len(merged) != 1 || merged[0].Body != "stock" {
		t.Fatalf("merged=%#v", merged)
	}
	if len(notes) != 1 || notes[0].Note != NoteShadowed {
		t.Fatalf("notes=%v", notes)
	}
}

func TestMergeAdditiveFreshName(t *testing.T) {
	stock := []Skill{{Name: "a", UnitID: "skills/a"}}
	project := []Skill{{Name: "b", UnitID: "skills/b", Project: true, UserProvided: true}}
	merged, notes := MergeAdditive(stock, project)
	if len(merged) != 2 || len(notes) != 0 {
		t.Fatalf("merged=%#v notes=%v", merged, notes)
	}
	if merged[0].UnitID != "skills/a" || merged[1].UnitID != "skills/b" {
		t.Fatalf("order %#v", merged)
	}
}

func TestMergeAdditiveStockSurvivesCap(t *testing.T) {
	stock := []Skill{{Name: "zzz-stock", UnitID: "skills/zzz-stock"}}
	project := make([]Skill, 0, CatalogMax)
	for i := 0; i < CatalogMax; i++ {
		name := "p" + pad3(i)
		project = append(project, Skill{
			Name: name, UnitID: "skills/" + name,
			Dir: "/tmp/proj/" + settingsoverlay.Rel("skills/") + name, Project: true, UserProvided: true,
		})
	}
	merged, notes := MergeAdditive(stock, project)
	if len(merged) != CatalogMax {
		t.Fatalf("merged=%d want %d", len(merged), CatalogMax)
	}
	foundStock := false
	for _, sk := range merged {
		if sk.UnitID == "skills/zzz-stock" {
			foundStock = true
		}
	}
	if !foundStock {
		t.Fatal("stock skill must survive the catalog cap")
	}
	var dropped []ProjectNote
	for _, n := range notes {
		if n.Note == NoteCatalogFull {
			dropped = append(dropped, n)
		}
	}
	if len(dropped) != 1 || dropped[0].UnitID != "skills/p127" || dropped[0].Source == "" {
		t.Fatalf("want one sourced catalog_full note for skills/p127, got %v", dropped)
	}
}

func TestDiscoverProjectTraversalNameRejected(t *testing.T) {
	root := t.TempDir()
	// A directory named with dots cannot be a valid skill name; Parse refuses it.
	dir := filepath.Join(root, settingsoverlay.DirName(), "skills", "not..valid")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillBody("not..valid", "d", "")), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
	got, notes := DiscoverProject([]string{root})
	if len(got) != 0 {
		t.Fatalf("invalid name must not load: %#v", got)
	}
	found := false
	for _, n := range notes {
		if n.Note == NoteNameInvalid {
			found = true
		}
	}
	if !found {
		t.Fatalf("notes=%v", notes)
	}
}

func pad3(i int) string {
	s := itoa(i)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}
