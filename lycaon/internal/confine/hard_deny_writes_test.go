package confine_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHardDenyWriteSpecsIncludesOverlaysAndAgents(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	proj := t.TempDir()

	specs, err := confine.HardDenyWriteSpecs(confine.Confinement{Roots: []string{proj}})
	testutil.FailErr(t, "HardDenyWriteSpecs", err)
	exe, err := os.Executable()
	testutil.FailErr(t, "Executable", err)
	exeResolved := filepath.Clean(exe)
	if rp, err := filepath.EvalSymlinks(exe); err == nil {
		exeResolved = rp
	}
	cfgResolved := cfg
	if rp, err := filepath.EvalSymlinks(cfg); err == nil {
		cfgResolved = rp
	}
	var hasControlPlane, hasPerm, hasAgents, hasLauncher bool
	for _, s := range specs {
		if s.Subpath == cfgResolved {
			hasControlPlane = true
		}
		if strings.Contains(s.Literal, settingsoverlay.BasenameApprovals) && strings.Contains(s.Literal, settingsoverlay.DirName()) {
			hasPerm = true
		}
		if strings.Contains(s.Regex, "[Aa][Gg][Ee]") && strings.HasSuffix(s.Subpath, filepath.Base(proj)) {
			hasAgents = true
		}
		if s.Literal == exeResolved {
			hasLauncher = true
		}
	}
	if !hasControlPlane {
		t.Fatalf("expected UserConfigDir subtree deny, got %#v", specs)
	}
	if !hasPerm {
		t.Fatalf("expected project approvals.yaml literal, got %#v", specs)
	}
	if !hasAgents {
		t.Fatalf("expected recursive AGENTS.md regex, got %#v", specs)
	}
	if !hasLauncher {
		t.Fatalf("expected launcher literal %q, got %#v", exeResolved, specs)
	}
}

// Workspace allow-backs do not expose adjacent host state.
func TestBuildProfileDeniesControlPlaneSubtreeWithWorkspaceAllowBack(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	cfgResolved := cfg
	if rp, err := filepath.EvalSymlinks(cfg); err == nil {
		cfgResolved = rp
	}
	draft := filepath.Join(cfgResolved, "drafts", "d1")
	testutil.FailErr(t, "mkdir draft", os.MkdirAll(draft, 0o700))

	p, err := confine.BuildSeatbeltProfile(confine.Confinement{
		Roots:             []string{"/proj"},
		GrantedWriteRoots: []string{draft},
	})
	testutil.FailErr(t, "BuildSeatbeltProfile", err)
	denyWrite, denyAt := profileBlock(t, p, blockDenyWrite, 0)
	assertBlockMentions(t, denyWrite, cfgResolved, "control-plane subtree deny")
	tail := p[denyAt+len(denyWrite):]
	if !strings.Contains(tail, "(subpath \""+draft+"\")") {
		t.Fatalf("granted workspace under the control plane must be re-allowed after the deny:\n%s", p)
	}
}

func TestBuildProfileRendersHardDenyWrites(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	proj := "/proj"
	p, err := confine.BuildSeatbeltProfile(confine.Confinement{Roots: []string{proj}})
	testutil.FailErr(t, "BuildSeatbeltProfile", err)
	// Each entry must appear in the deny block.
	denyWrite, _ := profileBlockContaining(t, p, blockDenyWrite, "approvals.yaml")
	assertBlockMentions(t, denyWrite, "approvals.yaml", "write hard-deny")
	policyDeny, _ := profileBlockContaining(t, p, blockDenyWrite, `[Aa][Gg][Ee]`)
	assertBlockMentions(t, policyDeny, "(regex ", "instruction approval boundary")
	if !strings.Contains(p, `[Aa][Gg][Ee]`) {
		t.Fatalf("profile must render recursive AGENTS.md deny:\n%s", p)
	}
}

func TestReviewedWriteRootOverridesBaselineButNotProtectedOrAgentPolicy(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	home := t.TempDir()
	t.Setenv("HOME", home)
	confine.SetCredentialStorePathsSource(func() []string { return []string{"~/.aws/"} })
	t.Cleanup(func() { confine.SetCredentialStorePathsSource(nil) })

	readRoot := filepath.Join(home, "reviewed-read-root")
	project := t.TempDir()
	sink := filepath.Join(project, settingsoverlay.DirName(), settingsoverlay.BasenameApprovals)
	p, err := confine.BuildSeatbeltProfile(confine.Confinement{
		Roots: []string{project}, ReadRoots: []string{readRoot},
		GrantedWriteRoots:    []string{readRoot},
		ProtectedWriteGrants: []confine.ProtectedPathGrant{{ResolvedPath: sink}},
	})
	testutil.FailErr(t, "BuildSeatbeltProfile", err)

	firstDeny, firstDenyAt := profileBlock(t, p, blockDenyWrite, 0)
	assertBlockCoversPath(t, firstDeny, readRoot, "baseline read-root deny")
	reviewedAllow, reviewedAllowAt := profileBlock(t, p, blockAllowWrite, firstDenyAt+len(firstDeny))
	assertBlockCoversPath(t, reviewedAllow, readRoot, "reviewed write-root allow-back")
	protectedDeny, protectedDenyAt := profileBlock(t, p, blockDenyWrite, reviewedAllowAt+len(reviewedAllow))
	assertBlockCoversPath(t, protectedDeny, filepath.Join(home, ".aws"), "protected-store re-deny")
	// A protected-path grant cannot open agent policy; only a policy grant can.
	policyDeny, policyDenyAt := profileBlockContaining(t, p, blockDenyWrite, sink)
	assertBlockMentions(t, policyDeny, sink, "agent-policy deny after protected grants")
	if !(firstDenyAt < reviewedAllowAt && reviewedAllowAt < protectedDenyAt && protectedDenyAt < policyDenyAt) {
		t.Fatalf("write authority layers out of order: baseline=%d reviewed=%d protected=%d policy=%d", firstDenyAt, reviewedAllowAt, protectedDenyAt, policyDenyAt)
	}
}

func TestBoundaryDeniesUngrantedAgentPolicyWrites(t *testing.T) {
	self := requireLiveBoundary(t)
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	proj := t.TempDir()
	ly := filepath.Join(proj, settingsoverlay.DirName())
	testutil.FailErr(t, "mkdir overlay", os.MkdirAll(ly, 0o700))
	sink := filepath.Join(ly, settingsoverlay.BasenameApprovals)
	nestedAgents := filepath.Join(proj, "pkg", "deep", "AGENTS.md")
	testutil.FailErr(t, "mkdir nested agents parent", os.MkdirAll(filepath.Dir(nestedAgents), 0o700))
	sibling := filepath.Join(proj, "ok.txt")

	run := func(name string, args ...string) int {
		return confinedExit(t, self, confine.Confinement{Roots: []string{proj}}, name, args...)
	}

	if code := run("/usr/bin/touch", sibling); code != 0 {
		t.Fatalf("sibling project write should succeed, exit=%d", code)
	}
	if code := run("/usr/bin/touch", sink); code == 0 {
		_ = os.Remove(sink)
		t.Fatal("touch of .paintedwolf/approvals.yaml should be denied")
	}
	if code := run("/usr/bin/touch", nestedAgents); code == 0 {
		_ = os.Remove(nestedAgents)
		t.Fatal("touch of nested AGENTS.md should be denied")
	}
	hostSink := filepath.Join(cfg, "approvals.yaml")
	if code := run("/usr/bin/touch", hostSink); code == 0 {
		_ = os.Remove(hostSink)
		t.Fatal("touch of UserConfigDir/approvals.yaml should be denied")
	}
}

// The launcher remains immutable inside its writable parent. Each refused
// mutation is paired with the same program succeeding on a sibling, so a
// refusal cannot come from the program failing to run at all.
func TestBoundaryLauncherUntamperable(t *testing.T) {
	self := requireLiveBoundary(t)
	exe, err := os.Executable()
	testutil.FailErr(t, "Executable", err)
	if rp, err := filepath.EvalSymlinks(exe); err == nil {
		exe = rp
	}
	original, err := os.ReadFile(exe)
	testutil.FailErr(t, "read launcher", err)
	launcherDir := filepath.Dir(exe)
	c := confine.Confinement{Roots: []string{launcherDir}}

	sibling := filepath.Join(launcherDir, "lycaon_launcher_sibling_ok.txt")
	renameSrc := filepath.Join(launcherDir, "lycaon_launcher_rename_src")
	t.Cleanup(func() { _ = os.Remove(sibling); _ = os.Remove(renameSrc) })
	seed := func() {
		t.Helper()
		testutil.FailErr(t, "seed sibling", os.WriteFile(sibling, []byte("seed"), 0o600))
		testutil.FailErr(t, "seed rename source", os.WriteFile(renameSrc, []byte("x"), 0o600))
	}
	mutations := []struct {
		name string
		argv func(target string) []string
	}{
		{"write", func(target string) []string {
			return []string{"/bin/bash", "-c", "printf x >" + shellSingleQuote(target)}
		}},
		{"truncate", func(target string) []string { return []string{"/bin/bash", "-c", ": >" + shellSingleQuote(target)} }},
		{"overwrite", func(target string) []string {
			return []string{"/bin/dd", "if=/dev/zero", "of=" + target, "bs=1", "count=1"}
		}},
		{"rename-over", func(target string) []string { return []string{"/bin/mv", "-f", renameSrc, target} }},
		{"unlink", func(target string) []string { return []string{"/bin/rm", "-f", target} }},
	}
	for _, m := range mutations {
		seed()
		argv := m.argv(sibling)
		if code := confinedExit(t, self, c, argv[0], argv[1:]...); code != 0 {
			t.Fatalf("positive control: %s of a sibling must succeed, exit=%d", m.name, code)
		}
		if got, _ := os.ReadFile(sibling); string(got) == "seed" {
			t.Fatalf("positive control: %s left the sibling unchanged", m.name)
		}
		seed()
		argv = m.argv(exe)
		if code := confinedExit(t, self, c, argv[0], argv[1:]...); code == 0 {
			t.Errorf("%s of the launcher must be refused", m.name)
		}
		if got, err := os.ReadFile(exe); err != nil || !bytes.Equal(got, original) {
			t.Fatalf("launcher changed after the refused %s (err %v)", m.name, err)
		}
	}
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func TestBuildProfileOmitsPrivateVarFoldersWriteRoot(t *testing.T) {
	p, err := confine.BuildSeatbeltProfile(confine.Confinement{Roots: []string{"/proj"}})
	testutil.FailErr(t, "BuildSeatbeltProfile", err)
	// The path must not appear in the write-root allow block.
	allowWrite, _ := profileBlock(t, p, blockAllowWrite, 0)
	assertBlockOmitsPath(t, allowWrite, "/private/var/folders", "write-root allow")
	exe, err := os.Executable()
	testutil.FailErr(t, "Executable", err)
	if rp, err := filepath.EvalSymlinks(exe); err == nil {
		exe = rp
	}
	// The launcher must appear in the deny block.
	denyWrite, _ := profileBlockContaining(t, p, blockDenyWrite, exe)
	assertBlockMentions(t, denyWrite, exe, "write hard-deny (launcher)")
}
