package confine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBuildProfileReadRootsAllowBackNotWritable(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	skillDir := filepath.Join(cfg, "packs", "stock", "skills", "verify-a-change")
	testutil.FailErr(t, "mkdir skill", os.MkdirAll(skillDir, 0o700))
	proj := t.TempDir()

	p, err := confine.BuildProfile(confine.Confinement{
		Roots:     []string{proj},
		ReadRoots: []string{skillDir},
	})
	testutil.FailErr(t, "BuildProfile", err)

	allowBack := readAllowBackBlock(t, p)
	assertBlockCoversPath(t, allowBack, skillDir, "skill read allow-back")

	writeAllow, _ := profileBlock(t, p, blockAllowWrite, 0)
	if strings.Contains(writeAllow, skillDir) {
		t.Fatalf("ReadRoots must not appear in write allow: %s", writeAllow)
	}
}

func TestSeatbeltSkillReadRoots(t *testing.T) {
	self := requireSeatbelt(t)
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	secret := filepath.Join(cfg, "credential-vault.age")
	testutil.FailErr(t, "write credentials", os.WriteFile(secret, []byte("key: topsecret\n"), 0o600))

	skillDir := filepath.Join(cfg, "skills", "demo")
	testutil.FailErr(t, "mkdir skill", os.MkdirAll(skillDir, 0o700))
	probe := filepath.Join(skillDir, "references", "note.md")
	testutil.FailErr(t, "mkdir refs", os.MkdirAll(filepath.Dir(probe), 0o700))
	testutil.FailErr(t, "seed probe", os.WriteFile(probe, []byte("ok\n"), 0o600))

	proj := t.TempDir()
	c := confine.Confinement{Roots: []string{proj}, ReadRoots: []string{skillDir}}

	if code, out := confinedRun(t, self, c, "/bin/cat", probe); code != 0 {
		t.Fatalf("skill reference must be readable, exit=%d out=%s", code, out)
	}
	if code, out := confinedRun(t, self, c, "/bin/cat", secret); code == 0 {
		t.Fatalf("config credential must stay denied, out=%s", out)
	}
	if code, out := confinedRun(t, self, c, "/usr/bin/touch", filepath.Join(skillDir, "evil.txt")); code == 0 {
		t.Fatalf("writes under skill directory must be denied, out=%s", out)
	}
}
