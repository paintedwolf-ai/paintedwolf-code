package settings_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/sensitivepath"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOrdinaryOutsideReadGrantsTheContainingFolder(t *testing.T) {
	dir := filepath.Join(filepath.VolumeName(os.TempDir())+string(filepath.Separator), "unattached", t.Name())
	file := filepath.Join(dir, "release.go")

	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:  "read",
			Files: []string{file},
		},
		Scope: hitl.ActionScope{
			ProjectDir: t.TempDir(),
			SessionID:  "chat-1",
			ProjectID:  "proj-1",
		},
	}
	_, offers := filesystemCard(t, action)
	for _, offer := range offers {
		gp := offer.Grant.GrantedPath
		if gp == nil || !gp.Tree || !grantedpath.CoversPath(gp.Path, true, file) || gp.Write {
			t.Fatalf("%s: ordinary outside read must grant the folder tree, got %+v", offer.Scope, gp)
		}
		if offer.Coverage != hitl.CoverageReadsOfTree(gp.Path) &&
			offer.Coverage != hitl.CoverageReadsOfTree(gp.Path)+hitl.DeviceCoverageSuffix {
			t.Fatalf("%s coverage = %q", offer.Scope, offer.Coverage)
		}
	}
}

func TestDirectoryReadGrantsThatDirectory(t *testing.T) {
	dir := canonDir(t, t.TempDir())
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:  "grep",
			Files: []string{dir},
		},
		Scope: hitl.ActionScope{
			ProjectDir: t.TempDir(),
			SessionID:  "chat-1",
			ProjectID:  "proj-1",
		},
	}
	target := gate.FileTarget{Path: dir, Mode: gate.ModeRead}
	decision := &gate.Decision{Primary: api.GateOutsideRootsRead}
	offers := settings.GrantedPathOffers(action, target, decision, nil)
	if len(offers) == 0 {
		t.Fatal("expected granted-path offers")
	}
	gp := offers[0].Grant.GrantedPath
	if gp == nil || !gp.Tree || gp.Path != dir {
		t.Fatalf("grep of a directory must grant that directory, got %+v", gp)
	}
}

func TestHomeFileReadOffersHomeAndAncestors(t *testing.T) {
	file := filepath.Join(home(t), "granted-path-exact.txt")
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:  "read",
			Files: []string{file},
		},
		Scope: hitl.ActionScope{
			ProjectDir: t.TempDir(),
			SessionID:  "chat-1",
			ProjectID:  "proj-1",
		},
	}
	target := gate.FileTarget{Path: file, Mode: gate.ModeRead}
	decision := &gate.Decision{Primary: api.GateOutsideRootsRead}
	offers := settings.GrantedPathOffers(action, target, decision, nil)
	if len(offers) == 0 {
		t.Fatal("expected granted-path offers")
	}
	for _, offer := range offers {
		gp := offer.Grant.GrantedPath
		if gp == nil || !gp.Tree || !grantedpath.CoversPath(gp.Path, true, file) {
			t.Fatalf("%s: home scope must cover the file as a tree, got %+v", offer.Scope, gp)
		}
	}
}

func TestTaskReadGrantSilencesLaterReadsInTheFolder(t *testing.T) {
	dir := filepath.Join(filepath.VolumeName(os.TempDir())+string(filepath.Separator), "unattached", t.Name())
	first := filepath.Join(dir, "release.go")
	sibling := filepath.Join(dir, "client.go")
	outside := filepath.Join(filepath.Dir(dir), "other.go")

	proj := t.TempDir()
	tmp := t.TempDir()
	stageBundledApprovals(t, nil)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	testutil.FailErr(t, "PutGlobal strict", store.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureStrict}))
	locations, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "sensitivepath.Load", err)
	sources := settings.NoSources()
	sources.Locations = locations
	g := settings.NewRuleApprovalGate(store, sources)

	read := func(path, tool string) hitl.ProposedAction {
		return hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{
				Tool:  tool,
				Files: []string{path},
			},
			Scope: hitl.ActionScope{
				ProjectDir: proj,
				SessionID:  "chat-1",
				ProjectID:  "proj-1",
			},
		}
	}
	firstAction := read(first, "read")
	res, err := g.Evaluate(context.Background(), firstAction)
	testutil.FailErr(t, "evaluate first read", err)
	if !res.Required() || res.Decision == nil || res.Decision.Primary != api.GateOutsideRootsRead {
		t.Fatalf("first outside read must ask outside_roots_read, got %+v", res)
	}
	offers := g.GrantOffers(firstAction, res)
	var task hitl.ApprovalGrant
	for _, offer := range offers {
		if offer.Rung == hitl.ApprovalRungChat {
			task = offer.Grant
			break
		}
	}
	if task.ID == "" || task.GrantedPath == nil || !task.GrantedPath.Tree {
		t.Fatalf("missing folder task grant: %+v", offers)
	}
	_, err = g.ApplyGrant(task)
	testutil.FailErr(t, "ApplyGrant", err)

	for _, tc := range []struct {
		name, tool, path string
	}{
		{"same file", "read", first},
		{"sibling", "read", sibling},
		{"directory grep", "grep", dir},
		{"directory find", "find", dir},
	} {
		got, err := g.Evaluate(context.Background(), read(tc.path, tc.tool))
		testutil.FailErr(t, "evaluate "+tc.name, err)
		if got.Required() {
			t.Fatalf("%s still asked after the folder grant: %+v", tc.name, got)
		}
	}

	outsideRes, err := g.Evaluate(context.Background(), read(outside, "read"))
	testutil.FailErr(t, "evaluate outside", err)
	if !outsideRes.Required() {
		t.Fatal("a path outside the granted folder must still ask")
	}

	writeRes, err := g.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:  "write",
			Files: []string{sibling},
		},
		Scope: hitl.ActionScope{
			ProjectDir: proj,
			SessionID:  "chat-1",
			ProjectID:  "proj-1",
		},
	})
	testutil.FailErr(t, "evaluate write", err)
	if !writeRes.Required() {
		t.Fatal("a read folder grant must not cover writes")
	}
}

// The resolver admits an outside path only through the asked action's file
// access, so a posture that left the read silent would strand it.
func TestOutsideReadAsksWithFileAccessAtEveryPosture(t *testing.T) {
	dir := filepath.Join(filepath.VolumeName(os.TempDir())+string(filepath.Separator), "unattached", t.Name())
	for _, posture := range []gate.Posture{gate.PostureLight, gate.PostureBalanced, gate.PostureStrict} {
		stageBundledApprovals(t, nil)
		store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
		testutil.FailErr(t, "NewApprovalStoreAt", err)
		testutil.FailErr(t, "PutGlobal "+string(posture), store.PutGlobal(settings.ApprovalConfig{Posture: posture}))
		g := settings.NewRuleApprovalGate(store, settings.NoSources())

		for _, tool := range []string{"list_dir", "read", "grep", "find"} {
			res, err := g.Evaluate(context.Background(), hitl.ProposedAction{
				Invocation: hitl.ActionInvocation{
					Tool:  tool,
					Files: []string{dir},
				},
				Scope: hitl.ActionScope{
					ProjectDir: t.TempDir(),
					SessionID:  "chat-1",
					ProjectID:  "proj-1",
				},
			})
			testutil.FailErr(t, string(posture)+" evaluate "+tool, err)
			if !res.Required() || res.Decision == nil || res.Decision.Primary != api.GateOutsideRootsRead {
				t.Fatalf("%s: %s outside the roots must ask outside_roots_read, got %+v", posture, tool, res)
			}
			if len(res.FileAccess) != 1 || res.FileAccess[0].Path != grantedpath.Normalize(dir) || res.FileAccess[0].Write {
				t.Fatalf("%s: %s must carry read access for %s, got %+v", posture, tool, dir, res.FileAccess)
			}
		}
	}
}

func TestTreeGrantDoesNotCoverSensitiveChildren(t *testing.T) {
	dir := filepath.Join(filepath.VolumeName(os.TempDir())+string(filepath.Separator), "unattached", t.Name())
	ordinary := filepath.Join(dir, "main.go")
	secret := filepath.Join(dir, ".env")

	proj := t.TempDir()
	tmp := t.TempDir()
	stageBundledApprovals(t, nil)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	testutil.FailErr(t, "PutGlobal strict", store.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureStrict}))
	locations, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "sensitivepath.Load", err)
	sources := settings.NoSources()
	sources.Locations = locations
	g := settings.NewRuleApprovalGate(store, sources)

	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:  "read",
			Files: []string{ordinary},
		},
		Scope: hitl.ActionScope{
			ProjectDir: proj,
			SessionID:  "chat-1",
			ProjectID:  "proj-1",
		},
	}
	res, err := g.Evaluate(context.Background(), action)
	testutil.FailErr(t, "evaluate ordinary", err)
	offers := g.GrantOffers(action, res)
	var task hitl.ApprovalGrant
	for _, offer := range offers {
		if offer.Rung == hitl.ApprovalRungChat {
			task = offer.Grant
			break
		}
	}
	if task.ID == "" {
		t.Fatal("missing task grant")
	}
	_, err = g.ApplyGrant(task)
	testutil.FailErr(t, "ApplyGrant", err)

	secretRes, err := g.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:  "read",
			Files: []string{secret},
		},
		Scope: hitl.ActionScope{
			ProjectDir: proj,
			SessionID:  "chat-1",
			ProjectID:  "proj-1",
		},
	})
	testutil.FailErr(t, "evaluate .env", err)
	if !secretRes.Required() {
		t.Fatal("a folder grant must not silence a sensitive file under it")
	}
}

func TestOrdinaryOutsideWriteStaysExact(t *testing.T) {
	dir := filepath.Join(filepath.VolumeName(os.TempDir())+string(filepath.Separator), "unattached", t.Name())
	file := filepath.Join(dir, "out.txt")
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:  "write",
			Files: []string{file},
		},
		Scope: hitl.ActionScope{
			ProjectDir: t.TempDir(),
			SessionID:  "chat-1",
			ProjectID:  "proj-1",
		},
	}
	_, offers := filesystemCard(t, action)
	for _, offer := range offers {
		gp := offer.Grant.GrantedPath
		if gp == nil || gp.Tree || gp.Path != file || !gp.Write {
			t.Fatalf("%s: an outside write must stay exact, got %+v", offer.Scope, gp)
		}
		if offer.Coverage != hitl.CoverageWritesTo(file) &&
			offer.Coverage != hitl.CoverageWritesTo(file)+hitl.DeviceCoverageSuffix {
			t.Fatalf("%s coverage = %q", offer.Scope, offer.Coverage)
		}
	}
}

func TestDurableTreeGrantSurvivesReloadAndSilencesReads(t *testing.T) {
	dir := filepath.Join(filepath.VolumeName(os.TempDir())+string(filepath.Separator), "unattached", t.Name())
	first := filepath.Join(dir, "release.go")
	sibling := filepath.Join(dir, "client.go")

	proj := t.TempDir()
	yamlPath := filepath.Join(t.TempDir(), "global.yaml")
	stageBundledApprovals(t, nil)
	store, err := settings.NewApprovalStoreAt(yamlPath)
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	testutil.FailErr(t, "PutGlobal strict", store.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureStrict}))
	locations, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "sensitivepath.Load", err)
	sources := settings.NoSources()
	sources.Locations = locations
	g := settings.NewRuleApprovalGate(store, sources)

	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:  "read",
			Files: []string{first},
		},
		Scope: hitl.ActionScope{
			ProjectDir: proj,
			SessionID:  "chat-1",
			ProjectID:  "proj-1",
		},
	}
	res, err := g.Evaluate(context.Background(), action)
	testutil.FailErr(t, "evaluate first read", err)
	offers := g.GrantOffers(action, res)
	var project hitl.ApprovalGrant
	for _, offer := range offers {
		// The durable slot is device-scoped for a filesystem crossing.
		if offer.Rung == hitl.ApprovalRungDevice && !offer.Disabled {
			project = offer.Grant
			break
		}
	}
	if project.ID == "" || project.GrantedPath == nil || !project.GrantedPath.Tree {
		t.Fatalf("missing folder durable grant: %+v", offers)
	}
	project.GrantedByPersonID = testutil.HostOwner().ID
	_, err = g.ApplyGrant(project)
	testutil.FailErr(t, "ApplyGrant", err)

	raw, err := os.ReadFile(yamlPath)
	testutil.FailErr(t, "read approvals.yaml", err)
	if !strings.Contains(string(raw), "tree: true") {
		t.Fatalf("durable grant must persist tree: true, got:\n%s", raw)
	}

	store2, err := settings.NewApprovalStoreAt(yamlPath)
	testutil.FailErr(t, "reload store", err)
	g2 := settings.NewRuleApprovalGate(store2, sources)
	got, err := g2.Evaluate(context.Background(), hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:  "read",
			Files: []string{sibling},
		},
		Scope: hitl.ActionScope{
			ProjectDir: proj,
			SessionID:  "chat-2",
			ProjectID:  "proj-1",
		},
	})
	testutil.FailErr(t, "evaluate sibling after reload", err)
	if got.Required() {
		t.Fatalf("durable tree grant must silence a later read in the folder: %+v", got)
	}
}

// canonDir resolves temp-dir aliases so expectations match minted canonical paths.
func canonDir(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	testutil.FailErr(t, "resolve temp dir", err)
	return resolved
}
