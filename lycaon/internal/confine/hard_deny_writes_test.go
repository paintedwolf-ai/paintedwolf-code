package confine_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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

	p, err := confine.BuildProfile(confine.Confinement{
		Roots:             []string{"/proj"},
		GrantedWriteRoots: []string{draft},
	})
	testutil.FailErr(t, "BuildProfile", err)
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
	p, err := confine.BuildProfile(confine.Confinement{Roots: []string{proj}})
	testutil.FailErr(t, "BuildProfile", err)
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
	p, err := confine.BuildProfile(confine.Confinement{
		Roots: []string{project}, ReadRoots: []string{readRoot},
		GrantedWriteRoots:    []string{readRoot},
		ProtectedWriteGrants: []confine.ProtectedPathGrant{{ResolvedPath: sink}},
	})
	testutil.FailErr(t, "BuildProfile", err)

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

func TestSeatbeltDeniesUngrantedAgentPolicyWrites(t *testing.T) {
	if runtime.GOOS != "darwin" || testing.Short() || !confine.Available() {
		t.Skip("darwin + sandbox required")
	}
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	self, err := os.Executable()
	testutil.FailErr(t, "Executable", err)
	proj := t.TempDir()
	ly := filepath.Join(proj, settingsoverlay.DirName())
	testutil.FailErr(t, "mkdir overlay", os.MkdirAll(ly, 0o700))
	sink := filepath.Join(ly, settingsoverlay.BasenameApprovals)
	nestedAgents := filepath.Join(proj, "pkg", "deep", "AGENTS.md")
	testutil.FailErr(t, "mkdir nested agents parent", os.MkdirAll(filepath.Dir(nestedAgents), 0o700))
	sibling := filepath.Join(proj, "ok.txt")

	run := func(name string, args ...string) int {
		cmd, cleanup, err := confine.Command(context.Background(), self, name, args,
			confine.Confinement{Roots: []string{proj}})
		testutil.FailErr(t, "Command", err)
		defer cleanup()
		_ = cmd.Run()
		return cmd.ProcessState.ExitCode()
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
	draft := filepath.Join(cfg, "drafts", "x.txt")
	testutil.FailErr(t, "mkdir drafts", os.MkdirAll(filepath.Dir(draft), 0o700))
	// The draft path may also be outside the project root.
	_ = draft
}

// The launcher remains immutable inside its writable parent.
func TestSeatbeltLauncherUntamperable(t *testing.T) {
	self := requireSeatbelt(t)
	exe := self
	if rp, err := filepath.EvalSymlinks(self); err == nil {
		exe = rp
	}
	launcherDir := filepath.Dir(exe)
	c := confine.Confinement{Roots: []string{launcherDir}}

	sibling := filepath.Join(launcherDir, "lycaon_launcher_sibling_ok.txt")
	_ = os.Remove(sibling)
	t.Cleanup(func() { _ = os.Remove(sibling) })
	if code := confinedExit(t, self, c, "/usr/bin/touch", sibling); code != 0 {
		t.Fatalf("sibling write in launcher dir must succeed, exit=%d", code)
	}

	if code := confinedExit(t, self, c, "/bin/bash", "-c", "printf x >"+shellSingleQuote(exe)); code == 0 {
		t.Fatal("write over launcher should be denied")
	}
	if code := confinedExit(t, self, c, "/bin/bash", "-c", ": >"+shellSingleQuote(exe)); code == 0 {
		t.Fatal("truncate of launcher should be denied")
	}
	if code := confinedExit(t, self, c, "/bin/rm", "-f", exe); code == 0 {
		t.Fatal("unlink of launcher should be denied")
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("launcher must still exist after denied unlink: %v", err)
	}
	if code := confinedExit(t, self, c, "/bin/dd", "if=/dev/zero", "of="+exe, "bs=1", "count=1"); code == 0 {
		t.Fatal("create-at/overwrite of launcher should be denied")
	}

	src := filepath.Join(launcherDir, "lycaon_launcher_rename_src")
	testutil.FailErr(t, "write rename src", os.WriteFile(src, []byte("x"), 0o600))
	t.Cleanup(func() { _ = os.Remove(src) })
	if code := confinedExit(t, self, c, "/bin/mv", "-f", src, exe); code == 0 {
		t.Fatal("rename-over launcher should be denied")
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("launcher must still exist after denied rename-over: %v", err)
	}
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func TestBuildProfileOmitsPrivateVarFoldersWriteRoot(t *testing.T) {
	p, err := confine.BuildProfile(confine.Confinement{Roots: []string{"/proj"}})
	testutil.FailErr(t, "BuildProfile", err)
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
