//go:build linux

package confine

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Denials inside a grant become mounts; denials Landlock already refuses do not.
func TestCompileFilesystemPlanMasksDenialsInsideGrants(t *testing.T) {
	proj := t.TempDir()
	ro := filepath.Join(proj, "vendor")
	testutil.FailErr(t, "mkdir vendor", os.Mkdir(ro, 0o755))
	cfg := t.TempDir()
	ws := filepath.Join(cfg, "workspaces", "w1")
	testutil.FailErr(t, "mkdir workspace", os.MkdirAll(ws, 0o755))

	var rules FilesystemRules
	rules.add(fsRule{op: opRead, allow: true, everywhere: true})
	rules.add(fsRule{op: opRead, matches: subpathMatches([]string{cfg}, FloorReadControlPlane)})
	rules.add(fsRule{op: opRead, allow: true, matches: subpathMatches([]string{ws}, "")})
	rules.add(fsRule{op: opReadMetadata, allow: true, matches: literalMatches([]string{cfg}, "")})
	rules.add(fsRule{op: opWrite, allow: true, matches: append(subpathMatches([]string{proj}, ""),
		fsMatch{literal: "/dev/null"}, fsMatch{subpath: "/dev/fd"}, fsMatch{regex: "^/dev/ttys"})})
	rules.add(fsRule{op: opWrite, matches: []fsMatch{
		{subpath: ro, layer: FloorBaseline},
		{subpath: "/nowhere/protected", layer: FloorProtected},
		{subpath: cfg, layer: FloorControlPlane, except: []string{ws}},
	}})
	rules.add(fsRule{op: opWrite, allow: true, matches: subpathMatches([]string{ws}, "")})
	rules.add(fsRule{op: opWrite, matches: []fsMatch{{regex: "^/dev/disk", layer: FloorControlPlane}}})

	fs, err := compileFilesystemPlan(rules)
	testutil.FailErr(t, "compile", err)
	want := linuxsandbox.Filesystem{
		WriteRoots: []string{proj, ws},
		ReadOnly:   []string{ro},
		Hidden:     []string{cfg},
		Restored:   []string{ws},
	}
	if got := sortedPlan(fs); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %+v\nwant %+v", got, want)
	}
}

// A later grant for a masked path lifts the mask instead of restoring beneath it.
func TestCompileFilesystemPlanLiftsMaskForLaterGrant(t *testing.T) {
	proj := t.TempDir()
	inner := filepath.Join(proj, "docs")
	testutil.FailErr(t, "mkdir docs", os.Mkdir(inner, 0o755))
	var rules FilesystemRules
	rules.add(fsRule{op: opWrite, allow: true, matches: subpathMatches([]string{proj}, "")})
	rules.add(fsRule{op: opWrite, matches: subpathMatches([]string{inner}, FloorBaseline)})
	rules.add(fsRule{op: opWrite, allow: true, matches: subpathMatches([]string{inner}, "")})
	fs, err := compileFilesystemPlan(rules)
	testutil.FailErr(t, "compile", err)
	if len(fs.ReadOnly) != 0 || len(fs.Restored) != 0 {
		t.Fatalf("lifted mask left mounts: %+v", fs)
	}
	if got := sortedPlan(fs).WriteRoots; !reflect.DeepEqual(got, []string{proj, inner}) {
		t.Fatalf("write roots = %v", got)
	}
}

// Loader files named at any depth are masked where they exist at launch.
func TestCompileFilesystemPlanNamesLoaderFilesAtAnyDepth(t *testing.T) {
	proj := t.TempDir()
	top := filepath.Join(proj, protectedpath.AgentsMDFileName)
	nested := filepath.Join(proj, "pkg", "agents.md")
	secondName := "SECOND.md"
	second := filepath.Join(proj, "pkg", secondName)
	gitIgnored := filepath.Join(proj, ".git", protectedpath.AgentsMDFileName)
	other := filepath.Join(proj, "pkg", "README.md")
	testutil.FailErr(t, "mkdir pkg", os.Mkdir(filepath.Dir(nested), 0o755))
	testutil.FailErr(t, "mkdir .git", os.Mkdir(filepath.Dir(gitIgnored), 0o755))
	for _, p := range []string{top, nested, second, gitIgnored, other} {
		testutil.FailErr(t, "write "+p, os.WriteFile(p, []byte("x"), 0o644))
	}
	if got := namedFilesBelow(proj, protectedpath.AgentsMDFileName, secondName); !reflect.DeepEqual(got, []string{top, second, nested}) {
		t.Fatalf("namedFilesBelow multi = %v, want %v", got, []string{top, second, nested})
	}
	fullInv := namedFilesBelowInventory(proj, agentPolicyWalkBudget, protectedpath.AgentsMDFileName)
	if !fullInv.Complete || fullInv.ReadDirs == 0 {
		t.Fatalf("full namedFilesBelowInventory = %+v, want Complete=true and ReadDirs>0", fullInv)
	}
	budgetInv := namedFilesBelowInventory(proj, 2, protectedpath.AgentsMDFileName)
	if budgetInv.Complete || budgetInv.ReadDirs == 0 {
		t.Fatalf("budget-exhausted namedFilesBelowInventory = %+v, want Complete=false and ReadDirs>0", budgetInv)
	}
	if os.Geteuid() != 0 {
		unreadable := filepath.Join(proj, "locked")
		testutil.FailErr(t, "mkdir locked", os.Mkdir(unreadable, 0o755))
		testutil.FailErr(t, "chmod locked", os.Chmod(unreadable, 0o000))
		t.Cleanup(func() { _ = os.Chmod(unreadable, 0o755) })
		partialInv := namedFilesBelowInventory(proj, agentPolicyWalkBudget, protectedpath.AgentsMDFileName)
		if partialInv.Complete || partialInv.ReadDirs == 0 {
			t.Fatalf("unreadable-dir namedFilesBelowInventory = %+v, want Complete=false and ReadDirs>0", partialInv)
		}
		if !reflect.DeepEqual(partialInv.Files, []string{top, nested}) {
			t.Fatalf("partialInv.Files = %v, want accessible files %v", partialInv.Files, []string{top, nested})
		}
		testutil.FailErr(t, "restore chmod locked", os.Chmod(unreadable, 0o755))
	}
	var location protectedpath.AgentPolicyLocation
	for _, l := range protectedpath.AgentPolicyLocations() {
		if l.Name == protectedpath.AgentsMDFileName {
			location = l
		}
	}
	var rules FilesystemRules
	rules.add(fsRule{op: opWrite, allow: true, matches: subpathMatches([]string{proj}, "")})
	rules.add(fsRule{op: opWrite, matches: []fsMatch{{subpath: proj, regex: location.NameRegex(), layer: FloorAgentPolicy}}})
	fs, err := compileFilesystemPlan(rules)
	testutil.FailErr(t, "compile", err)
	if got := sortedPlan(fs).ReadOnly; !reflect.DeepEqual(got, []string{top, nested}) {
		t.Fatalf("read-only = %v, want both loader files outside .git", got)
	}

	rules = FilesystemRules{}
	rules.add(fsRule{op: opWrite, allow: true, matches: subpathMatches([]string{proj}, "")})
	rules.add(fsRule{op: opWrite, matches: []fsMatch{{subpath: proj, regex: "^.*\\.secret$", layer: FloorBaseline}}})
	_, err = compileFilesystemPlan(rules)
	var unsupported *linuxsandbox.Unsupported
	if !errors.As(err, &unsupported) || unsupported.Code != linuxsandbox.UnsupportedFilesystemRule {
		t.Fatalf("unknown pattern: %v", err)
	}
}

// Mounted views resolve per-root policy files without cross-mapping between roots,
// misclassifying sources containing journal/work/base, or missing parent/private mutations.
func TestCompileFilesystemPlanResolvesMountedViewLoaderFilesFromSourceRoots(t *testing.T) {
	// 1. Multi-root mounted view with shared and root-specific relative paths and a legitimate
	// source tree containing journal/, work/, and base/ subdirectories.
	srcA := t.TempDir()
	srcB := t.TempDir()
	for _, dir := range []string{
		filepath.Join(srcA, "shared"),
		filepath.Join(srcA, "only-a"),
		filepath.Join(srcA, "journal"),
		filepath.Join(srcA, "work"),
		filepath.Join(srcA, "base"),
		filepath.Join(srcA, ".git"),
		filepath.Join(srcB, "shared"),
		filepath.Join(srcB, "only-b"),
	} {
		testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(dir, 0o755))
	}
	for _, f := range []string{
		filepath.Join(srcA, "shared", protectedpath.AgentsMDFileName),
		filepath.Join(srcA, "only-a", protectedpath.AgentsMDFileName),
		filepath.Join(srcA, "work", protectedpath.AgentsMDFileName),
		filepath.Join(srcA, ".git", protectedpath.AgentsMDFileName),
		filepath.Join(srcB, "shared", protectedpath.AgentsMDFileName),
		filepath.Join(srcB, "only-b", protectedpath.AgentsMDFileName),
	} {
		testutil.FailErr(t, "write "+f, os.WriteFile(f, []byte("policy"), 0o644))
	}

	branchesBase := t.TempDir()
	branchRoot := filepath.Join(branchesBase, "wb-multi")
	mountA := filepath.Join(branchRoot, "mounts", "root-a")
	mountB := filepath.Join(branchRoot, "mounts", "root-b")
	backingDir := filepath.Join(branchRoot, ".meta", "backing")
	testutil.FailErr(t, "mkdir mountA", os.MkdirAll(filepath.Join(mountA, "mount-only"), 0o755))
	testutil.FailErr(t, "mkdir mountB", os.MkdirAll(mountB, 0o755))
	testutil.FailErr(t, "mkdir backing", os.MkdirAll(backingDir, 0o755))
	decoy := filepath.Join(mountA, "mount-only", protectedpath.AgentsMDFileName)
	testutil.FailErr(t, "write mount decoy", os.WriteFile(decoy, []byte("decoy"), 0o644))

	rules, err := filesystemRules(Confinement{
		Roots: []string{branchRoot},
		View: ViewBoundary{
			DeniedPaths:   []string{branchesBase, srcA, srcB, backingDir},
			ReopenedPaths: []string{mountA, mountB, branchRoot},
			Roots: []ViewRootBoundary{
				{RootID: "root-a", WorkspacePath: mountA, SourcePath: srcA},
				{RootID: "root-b", WorkspacePath: mountB, SourcePath: srcB},
			},
		},
	})
	testutil.FailErr(t, "multi-root filesystemRules", err)
	plan, err := compileFilesystemPlan(rules)
	testutil.FailErr(t, "multi-root compileFilesystemPlan", err)

	for _, want := range []string{
		filepath.Join(mountA, "shared", protectedpath.AgentsMDFileName),
		filepath.Join(mountA, "only-a", protectedpath.AgentsMDFileName),
		filepath.Join(mountA, "work", protectedpath.AgentsMDFileName),
		filepath.Join(mountB, "shared", protectedpath.AgentsMDFileName),
		filepath.Join(mountB, "only-b", protectedpath.AgentsMDFileName),
	} {
		if !containsPath(plan.ReadOnly, want) {
			t.Fatalf("plan.ReadOnly = %v, missing expected policy file %q", plan.ReadOnly, want)
		}
	}
	for _, unwanted := range []string{
		filepath.Join(mountB, "only-a", protectedpath.AgentsMDFileName),
		filepath.Join(mountA, "only-b", protectedpath.AgentsMDFileName),
		filepath.Join(mountA, ".git", protectedpath.AgentsMDFileName),
		decoy,
	} {
		if containsPath(plan.ReadOnly, unwanted) {
			t.Fatalf("plan.ReadOnly = %v, unexpectedly contains %q", plan.ReadOnly, unwanted)
		}
	}

	// 2. Effective view inventory via layeredfs.Core: parent layer additions/deletions,
	// view-private creations/renames/unlinks, and single trunk scan bound across launches.
	trunkDir := t.TempDir()
	for _, d := range []string{"trunk-keep", "trunk-parent-del", "trunk-priv-unlink", "renamed"} {
		testutil.FailErr(t, "mkdir "+d, os.MkdirAll(filepath.Join(trunkDir, d), 0o755))
	}
	testutil.FailErr(t, "write trunk-keep", os.WriteFile(filepath.Join(trunkDir, "trunk-keep", protectedpath.AgentsMDFileName), []byte("keep"), 0o644))
	testutil.FailErr(t, "write trunk-parent-del", os.WriteFile(filepath.Join(trunkDir, "trunk-parent-del", protectedpath.AgentsMDFileName), []byte("del"), 0o644))
	testutil.FailErr(t, "write trunk-priv-unlink", os.WriteFile(filepath.Join(trunkDir, "trunk-priv-unlink", protectedpath.AgentsMDFileName), []byte("unlink"), 0o644))
	testutil.FailErr(t, "write draft.txt", os.WriteFile(filepath.Join(trunkDir, "draft.txt"), []byte("draft"), 0o644))

	parentLayer := layeredfs.NewMemoryParentLayer("parent-1")
	parentBacking := filepath.Join(t.TempDir(), "parent-agents.md")
	testutil.FailErr(t, "write parent backing", os.WriteFile(parentBacking, []byte("parent"), 0o644))
	parentLayer.SetEntry(layeredfs.QualifiedPath{RootID: "proj", Rel: "parent-dir"}, layeredfs.ParentEntry{Kind: layeredfs.EntryDirectory, Mode: 0o755})
	parentLayer.SetEntry(layeredfs.QualifiedPath{RootID: "proj", Rel: "parent-dir/" + protectedpath.AgentsMDFileName}, layeredfs.ParentEntry{
		Kind: layeredfs.EntryRegular, Mode: 0o644, Size: 6, BackingAbs: parentBacking,
	})
	parentLayer.SetEntry(layeredfs.QualifiedPath{RootID: "proj", Rel: "trunk-parent-del/" + protectedpath.AgentsMDFileName}, layeredfs.ParentEntry{
		Kind: layeredfs.EntryAbsent,
	})

	core, err := layeredfs.New(layeredfs.Config{
		ViewID:       "view-effective-policy",
		BackingDir:   t.TempDir(),
		Roots:        []layeredfs.RootSpec{{ID: "proj", Label: "proj", TrunkPath: trunkDir, IsPrimary: true}},
		ParentLayers: []layeredfs.ParentLayer{parentLayer},
	})
	testutil.FailErr(t, "layeredfs.New", err)
	defer func() { _ = core.ReleaseView() }()

	_, err = core.Mkdir(layeredfs.QualifiedPath{RootID: "proj", Rel: "private-dir"}, 0o755)
	testutil.FailErr(t, "Mkdir private-dir", err)
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "proj", Rel: "private-dir/" + protectedpath.AgentsMDFileName}, []byte("priv"), 0o644)
	testutil.FailErr(t, "WriteFile private AGENTS.md", err)
	testutil.FailErr(t, "Rename draft -> renamed/AGENTS.md", core.Rename(
		layeredfs.QualifiedPath{RootID: "proj", Rel: "draft.txt"},
		layeredfs.QualifiedPath{RootID: "proj", Rel: "renamed/" + protectedpath.AgentsMDFileName},
	))
	testutil.FailErr(t, "Unlink trunk-priv-unlink/AGENTS.md", core.Unlink(layeredfs.QualifiedPath{RootID: "proj", Rel: "trunk-priv-unlink/" + protectedpath.AgentsMDFileName}, false))
	core.ResetStepStats()

	mountSingle := filepath.Join(branchesBase, "wb-single", "mount")
	testutil.FailErr(t, "mkdir mountSingle", os.MkdirAll(mountSingle, 0o755))
	compileWithInventory := func() (linuxsandbox.Filesystem, layeredfs.PolicyFileInventory) {
		inv := core.PolicyInventory("proj", protectedpath.AgentsMDFileName)
		if inv.ReadDirs != 0 || inv.Complete || inv.Generation == 0 {
			t.Fatalf("core.PolicyInventory = %+v, want ReadDirs=0, Complete=false, Generation>0", inv)
		}
		r, err := filesystemRules(Confinement{
			Roots: []string{mountSingle},
			View: ViewBoundary{
				DeniedPaths:   []string{branchesBase, trunkDir},
				ReopenedPaths: []string{mountSingle},
				Roots: []ViewRootBoundary{{
					RootID:        "proj",
					WorkspacePath: mountSingle,
					SourcePath:    trunkDir,
					Inventory: PolicyFileInventory{
						Files:      inv.Files,
						Removed:    inv.Removed,
						Complete:   inv.Complete,
						Generation: inv.Generation,
						ReadDirs:   inv.ReadDirs,
					},
				}},
			},
		})
		testutil.FailErr(t, "effective filesystemRules", err)
		p, err := compileFilesystemPlan(r)
		testutil.FailErr(t, "effective compileFilesystemPlan", err)
		if got := lastPlanReadDirs.Load(); got != 0 {
			t.Fatalf("compileFilesystemPlan performed %d OS ReadDir calls on mounted view, want 0", got)
		}
		return p, inv
	}

	effPlan, inv1 := compileWithInventory()
	if st := core.Stats(); st.LowerDirReads != 0 || st.LowerLookups != 0 {
		t.Fatalf("PolicyInventory touched lower provider: %+v", st)
	}
	for _, rel := range []string{
		"parent-dir/" + protectedpath.AgentsMDFileName,
		"private-dir/" + protectedpath.AgentsMDFileName,
		"renamed/" + protectedpath.AgentsMDFileName,
	} {
		want := filepath.Join(mountSingle, filepath.FromSlash(rel))
		if !containsPath(effPlan.ReadOnly, want) {
			t.Fatalf("effPlan.ReadOnly = %v, missing %q", effPlan.ReadOnly, want)
		}
	}
	for _, rel := range []string{
		"trunk-parent-del/" + protectedpath.AgentsMDFileName,
		"trunk-priv-unlink/" + protectedpath.AgentsMDFileName,
	} {
		if !containsPath(inv1.Removed, rel) {
			t.Fatalf("inv1.Removed = %v, missing deleted/unlinked %q", inv1.Removed, rel)
		}
		unwanted := filepath.Join(mountSingle, filepath.FromSlash(rel))
		if containsPath(effPlan.ReadOnly, unwanted) {
			t.Fatalf("effPlan.ReadOnly = %v, still contains deleted/unlinked %q", effPlan.ReadOnly, unwanted)
		}
	}
	wantFloor := linuxsandbox.NamedFloor{Below: mountSingle, Name: protectedpath.AgentsMDFileName}
	hasFloor := false
	for _, nf := range effPlan.ReadOnlyNames {
		if nf == wantFloor {
			hasFloor = true
			break
		}
	}
	if !hasFloor {
		t.Fatalf("effPlan.ReadOnlyNames = %v, missing enforcement-time floor %+v for trunk/future policy files", effPlan.ReadOnlyNames, wantFloor)
	}

	// 3. First-launch and subsequent-launch bounds: neither preparation nor launch walks the host
	// trunk (ReadDirs == 0), Generation advances on view-private mutation, and new view-private
	// policy files are immediately included while late trunk policy files are protected by ReadOnlyNames.
	lateTrunkFile := filepath.Join(trunkDir, "late-trunk", protectedpath.AgentsMDFileName)
	testutil.FailErr(t, "mkdir late-trunk", os.MkdirAll(filepath.Dir(lateTrunkFile), 0o755))
	testutil.FailErr(t, "write late-trunk AGENTS.md", os.WriteFile(lateTrunkFile, []byte("late"), 0o644))
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "proj", Rel: protectedpath.AgentsMDFileName}, []byte("root-priv"), 0o644)
	testutil.FailErr(t, "WriteFile private-2", err)

	secondPlan, inv2 := compileWithInventory()
	if inv2.Generation <= inv1.Generation {
		t.Fatalf("inv2.Generation = %d, want > inv1.Generation (%d)", inv2.Generation, inv1.Generation)
	}
	if !containsPath(secondPlan.ReadOnly, filepath.Join(mountSingle, protectedpath.AgentsMDFileName)) {
		t.Fatalf("secondPlan.ReadOnly = %v, missing new view-private root AGENTS.md", secondPlan.ReadOnly)
	}
	if containsPath(secondPlan.ReadOnly, filepath.Join(mountSingle, "late-trunk", protectedpath.AgentsMDFileName)) {
		t.Fatalf("secondPlan.ReadOnly = %v, unexpectedly walked trunk on launch", secondPlan.ReadOnly)
	}
}

// The rendered plan carries the engine's namespaces and refuses modes the
// helper cannot express.
func TestRenderProfileBuildsDenyProxyAndBrowserPlans(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	proj := t.TempDir()
	raw, err := renderProfile(Confinement{Roots: []string{proj}, Network: NetworkDeny, EgressOff: EgressOffOverride})
	testutil.FailErr(t, "render deny plan", err)
	plan, err := linuxsandbox.DecodePlan([]byte(raw))
	testutil.FailErr(t, "decode", err)
	for _, kind := range linuxsandbox.NamespaceKinds() {
		if plan.Attest.HostNamespaces[kind] == "" {
			t.Fatalf("plan lacks host namespace %s: %+v", kind, plan.Attest)
		}
	}
	if !containsPath(plan.Filesystem.WriteRoots, proj) {
		t.Fatalf("plan write roots %v lack the project", plan.Filesystem.WriteRoots)
	}
	if plan.Network.Mode != linuxsandbox.NetworkDeny {
		t.Fatalf("network mode %q", plan.Network.Mode)
	}

	raw, err = renderProfile(Confinement{Roots: []string{proj}, Network: NetworkProxyOnly, ProxyAddr: "127.0.0.1:1"})
	testutil.FailErr(t, "proxy-only plan", err)
	proxied, err := linuxsandbox.DecodePlan([]byte(raw))
	testutil.FailErr(t, "decode proxy-only plan", err)
	if proxied.Network.Mode != linuxsandbox.NetworkProxyOnly || proxied.Network.ProxyAddr != "127.0.0.1:1" {
		t.Fatalf("proxy-only plan network %+v", proxied.Network)
	}
	raw, err = renderProfile(Confinement{Roots: []string{proj}, Network: NetworkDeny, Browser: true, LoopbackConnect: true})
	testutil.FailErr(t, "browser plan", err)
	browser, err := linuxsandbox.DecodePlan([]byte(raw))
	testutil.FailErr(t, "decode browser plan", err)
	if !browser.Network.Browser || !browser.Network.Listen || !browser.Network.LoopbackConnect {
		t.Fatalf("browser plan network %+v", browser.Network)
	}
}

func TestNetworkPlanMirrorsBoundaryDerivations(t *testing.T) {
	t.Parallel()
	direct := networkPlan(Confinement{Network: NetworkDirectIP, DirectIPPermits: []DirectIPPermit{{Protocol: "tcp", Port: 443}}})
	if !direct.Listen || !direct.LoopbackConnect || len(direct.DirectIPPermits) != 1 || direct.Mode != linuxsandbox.NetworkDirectIP {
		t.Fatalf("direct plan: %+v", direct)
	}
	deny := networkPlan(Confinement{Network: NetworkDeny, LocalListen: true, LocalListenPorts: []uint16{8080},
		SocketGrants: []SocketGrant{{ApprovedPath: "/run/a", ResolvedPath: "/run/a"}}})
	if deny.LoopbackConnect || !deny.Listen || !reflect.DeepEqual(deny.ListenPorts, []uint16{8080}) || !reflect.DeepEqual(deny.SocketGrants, []linuxsandbox.SocketGrant{{Path: "/run/a", FD: linuxsandbox.SocketReferenceFD}}) {
		t.Fatalf("deny plan: %+v", deny)
	}
}

func sortedPlan(fs linuxsandbox.Filesystem) linuxsandbox.Filesystem {
	plan := linuxsandbox.Plan{Version: linuxsandbox.PlanVersion, Filesystem: fs}
	raw, _ := plan.Encode()
	decoded, _ := linuxsandbox.DecodePlan(raw)
	return decoded.Filesystem
}

func containsPath(list []string, want string) bool {
	for _, p := range list {
		if PathEqual(p, want) {
			return true
		}
	}
	return false
}

// A draft is editor state; an ordinary Linux launch retains its declared roots.
func TestLinuxDraftFileKeepsOrdinaryWriteRoot(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "saved draft base", os.WriteFile(filepath.Join(root, "draft.txt"), []byte("one\ntwo\n"), 0600))
	rules, err := filesystemRules(Confinement{Roots: []string{root}})
	testutil.FailErr(t, "ordinary draft-path rules", err)
	plan, err := compileFilesystemPlan(rules)
	testutil.FailErr(t, "ordinary Linux launch plan", err)
	found := false
	for _, path := range plan.WriteRoots {
		if path == root {
			found = true
		}
	}
	if !found {
		t.Fatalf("project write root lost: %+v", plan)
	}
}

// UseDelegatedEndpointsForTest stands a fixture socket in for a delegated
// service endpoint, which tests cannot create at its operating-system path.
func UseDelegatedEndpointsForTest(t *testing.T, endpoints []linuxsandbox.DelegatedEndpoint) {
	t.Helper()
	previous := delegatedEndpoints
	delegatedEndpoints = endpoints
	t.Cleanup(func() { delegatedEndpoints = previous })
}
