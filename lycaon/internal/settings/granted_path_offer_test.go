package settings_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/sensitivepath"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func home(t *testing.T) string {
	t.Helper()
	dir, err := os.UserHomeDir()
	testutil.FailErr(t, "os.UserHomeDir failed", err)
	return dir
}

// filesystemCard returns the ladder produced by the bundled location rules.
func filesystemCard(t *testing.T, action hitl.ProposedAction) (*hitl.ApprovalResult, []hitl.ApprovalGrantOffer) {
	t.Helper()
	if action.Scope.ProjectID == "" {
		action.Scope.ProjectID = "project-1"
	}
	tmp := t.TempDir()
	stageBundledApprovals(t, nil)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	testutil.FailErr(t, "PutGlobal strict", store.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureStrict}))
	locations, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "sensitivepath.Load failed", err)
	sources := settings.NoSources()
	sources.Locations = locations
	g := settings.NewRuleApprovalGate(store, sources)
	res, err := g.Evaluate(context.Background(), action)
	testutil.FailErr(t, "gate.Evaluate failed", err)
	if !res.Required() {
		t.Fatalf("expected a card, got %+v", res)
	}
	offers := g.GrantOffers(action, res)
	if len(offers) == 0 {
		t.Fatal("filesystem card must offer a ladder")
	}
	return res, offers
}

func TestFilesystemLadderInstallsTheGrantedPath(t *testing.T) {
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write",
Files: []string{"/etc/hosts"},
},
Scope: hitl.ActionScope{
ProjectDir: t.TempDir(),
SessionID: "chat-1",
},
}
	_, offers := filesystemCard(t, action)

	for _, offer := range offers {
		if offer.Grant.ProjectID != "project-1" {
			t.Fatalf("%s: project id = %q", offer.Scope, offer.Grant.ProjectID)
		}
		delta := offer.Authority[0]
		if delta.Kind != hitl.AuthorityGrantedPath {
			t.Fatalf("%s: kind = %s, want granted_path", offer.Scope, delta.Kind)
		}
		// Both authority records share one revocation identity.
		if len(offer.Authority) != 2 {
			t.Fatalf("%s: authority deltas = %d", offer.Scope, len(offer.Authority))
		}
		if offer.Authority[1].Kind != hitl.AuthorityGenericGrant {
			t.Fatalf("%s: second delta = %s, want the lease record", offer.Scope, offer.Authority[1].Kind)
		}
		if offer.Authority[1].Grant.GrantedPath == nil {
			t.Fatalf("%s: a path lease must carry the access it grants", offer.Scope)
		}
		if delta.GrantedPath == nil || delta.GrantedPath.Path != "/etc/hosts" {
			t.Fatalf("%s: granted path = %+v", offer.Scope, delta.GrantedPath)
		}
		if !delta.GrantedPath.Write {
			t.Fatalf("%s: a write tool must grant write", offer.Scope)
		}
		if delta.ChatSessionID != "chat-1" {
			t.Fatalf("%s: chat session = %q", offer.Scope, delta.ChatSessionID)
		}
	}
}

func TestFilesystemGrantIdentitySurvivesRootRelocation(t *testing.T) {
	base := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write",
Files: []string{"/etc/hosts"},
},
Scope: hitl.ActionScope{
ProjectID: "project-stable",
ProjectDir: t.TempDir(),
SessionID: "chat-1",
},
}
	_, before := filesystemCard(t, base)
	base.Scope.ProjectDir = t.TempDir()
	_, after := filesystemCard(t, base)
	if len(before) != len(after) {
		t.Fatalf("offer counts differ: %d vs %d", len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID {
			t.Fatalf("%s grant id changed with root path: %q vs %q", before[i].Scope, before[i].ID, after[i].ID)
		}
	}
}

func TestReadCrossingGrantsOnlyRead(t *testing.T) {
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "read",
Files: []string{filepath.Join(home(t), "Documents", "taxes.pdf")},
},
Scope: hitl.ActionScope{
ProjectDir: t.TempDir(),
SessionID: "chat-1",
},
}
	_, offers := filesystemCard(t, action)
	for _, offer := range offers {
		if offer.Authority[0].GrantedPath.Write {
			t.Fatalf("%s: a read crossing must not grant write", offer.Scope)
		}
		if offer.Grant.GrantedPath != nil && offer.Grant.GrantedPath.Tree {
			t.Fatalf("%s: Documents is catalogued sensitive; grant must stay exact, got %+v",
				offer.Scope, offer.Grant.GrantedPath)
		}
		if !strings.Contains(offer.Coverage, "reads of") {
			t.Fatalf("%s: coverage should say what it covers, got %q", offer.Scope, offer.Coverage)
		}
	}
}

func TestEveryFilesystemRungIsTimeBounded(t *testing.T) {
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write",
Files: []string{"/etc/hosts"},
},
Scope: hitl.ActionScope{
ProjectDir: t.TempDir(),
SessionID: "chat-1",
},
}
	_, offers := filesystemCard(t, action)
	for _, offer := range offers {
		// Task rungs end with the task; they carry copy, not a resolved timestamp.
		if offer.Rung == hitl.ApprovalRungChat && offer.TTLSeconds == 0 {
			if strings.TrimSpace(offer.ExpiresWhen) == "" {
				t.Fatalf("%s rung does not say when it ends", offer.Scope)
			}
			continue
		}
		// The installer resolves relative TTLs at approval time.
		if offer.Grant.ResolveExpiry(time.Now()) == nil {
			t.Fatalf("%s rung has no expiry", offer.Scope)
		}
		if strings.TrimSpace(offer.ExpiresWhen) == "" {
			t.Fatalf("%s rung does not say when it ends", offer.Scope)
		}
	}
}

// Grant offers name the uncovered path so the resulting lease covers it.
func TestCardNamesTheUncoveredCrossing(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovals(t, nil)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	testutil.FailErr(t, "PutGlobal strict", store.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureStrict}))
	locations, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "sensitivepath.Load failed", err)
	sources := settings.NoSources()
	sources.Locations = locations
	g := settings.NewRuleApprovalGate(store, sources)

	covered := filepath.Join(home(t), ".ssh", "config")
	uncovered := "/etc/hosts"
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "read",
Files: []string{covered, uncovered},
},
Scope: hitl.ActionScope{
ProjectID: "project-1",
ProjectDir: tmp,
SessionID: "chat-1",
RootSessionID: "chat-1",
},
}

	// An exact device grant over the first crossing only.
	grant := settings.ApprovalGrant{
		GrantedByPersonID: testutil.HostOwner().ID,
		ID:                "grant-covered",
		Scope:             hitl.ApprovalGrantScopeDevice,
		Category:          settings.ApprovalCategoryPath,
		Pattern:           covered,
		ProjectID:         "project-1",
		Title:             hitl.TitleAllowOnThisDevice,
		Coverage:          hitl.CoverageReadsOf(covered),
		GrantedPath:       &hitl.GrantedPathDelta{Path: covered},
	}
	_, err = store.UpsertGlobalGrant(grant)
	testutil.FailErr(t, "UpsertGlobalGrant failed", err)

	res, err := g.Evaluate(context.Background(), action)
	testutil.FailErr(t, "gate.Evaluate failed", err)
	if !res.Required() {
		t.Fatalf("one uncovered crossing must still ask, got %+v", res)
	}
	var citedPath string
	for _, fact := range res.Decision.Cited {
		if fact.Key == "file.path" {
			citedPath = fact.Value
		}
	}
	if citedPath != uncovered {
		t.Fatalf("card cites %q, want the uncovered crossing %q", citedPath, uncovered)
	}
	// Outside-root read offers cover the parent folder of the uncovered path.
	for _, offer := range g.GrantOffers(action, res) {
		p := offer.Grant.GrantedPath
		if p == nil {
			continue
		}
		if !grantedpath.CoversPath(p.Path, p.Tree, uncovered) {
			t.Fatalf("offer %s mints a grant over %q, which does not cover %q", offer.ID, p.Path, uncovered)
		}
	}
}

// A project identity makes the day grant project-scoped.
func TestPathDayRungRidesTheProjectCarrier(t *testing.T) {
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write",
Files: []string{"/etc/hosts"},
},
Scope: hitl.ActionScope{
ProjectID: "project-1",
ProjectDir: t.TempDir(),
SessionID: "chat-1",
},
}
	_, offers := filesystemCard(t, action)
	for _, offer := range offers {
		if offer.Rung != hitl.ApprovalRungDay {
			continue
		}
		if offer.Scope != hitl.ApprovalGrantScopeProject {
			t.Fatalf("day rung scope = %q, want project when the action has a project identity", offer.Scope)
		}
		return
	}
	t.Fatal("no day rung offered")
}

func TestDayRungCountsFromApproval(t *testing.T) {
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write",
Files: []string{"/etc/hosts"},
},
Scope: hitl.ActionScope{
ProjectDir: t.TempDir(),
SessionID: "chat-1",
},
}
	_, offers := filesystemCard(t, action)
	for _, offer := range offers {
		if offer.Rung != hitl.ApprovalRungDay {
			continue
		}
		late := time.Now().Add(50 * time.Minute)
		got := offer.Grant.ResolveExpiry(late)
		if got == nil {
			t.Fatal("day rung resolved to no expiry")
		}
		if remaining := got.Sub(late); remaining < 23*time.Hour || remaining > 25*time.Hour {
			t.Fatalf("answering late granted %s, want about one day", remaining)
		}
		return
	}
	t.Fatal("no day rung offered")
}

func TestPathLadderFacesTheTaskRung(t *testing.T) {
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write",
Files: []string{"/etc/hosts"},
},
Scope: hitl.ActionScope{
ProjectDir: t.TempDir(),
SessionID: "chat-1",
},
}
	res, offers := filesystemCard(t, action)
	plan, err := hitl.CompileCheckpointApprovalPlan(hitl.CheckpointRequest{
		ProposedAction: &action, Decision: res.Decision, GrantOffers: offers,
	})
	testutil.FailErr(t, "hitl.CompileCheckpointApprovalPlan failed", err)
	var taskID string
	for _, offer := range offers {
		if offer.Rung == hitl.ApprovalRungChat {
			taskID = offer.ID
			break
		}
	}
	if taskID == "" {
		t.Fatal("path ladder must offer a task rung")
	}
	if plan.RecommendedOptionID != taskID {
		t.Fatalf("recommended_option_id = %q want task rung %q", plan.RecommendedOptionID, taskID)
	}
}

// Secret outbound uses fingerprint release.
func TestSecretOutboundOffersNoPathLadder(t *testing.T) {
	target := gate.FileTarget{Path: "/etc/hosts", Mode: gate.ModeWrite}
	decision := &gate.Decision{Primary: api.GateSecretOutbound}
	if offers := settings.GrantedPathOffers(hitl.ProposedAction{
Scope: hitl.ActionScope{
SessionID: "chat-1",
},
}, target, decision, nil); offers != nil {
		t.Fatalf("secret_outbound must offer no path rungs, got %d", len(offers))
	}
}

// A relative path is workspace-relative; the attached roots already answer for it.
func TestRelativeTargetOffersNoLadder(t *testing.T) {
	target := gate.FileTarget{Path: "notes/todo.md", Mode: gate.ModeWrite}
	decision := &gate.Decision{Primary: api.GateSensitiveLocation}
	if offers := settings.GrantedPathOffers(hitl.ProposedAction{
Scope: hitl.ActionScope{
SessionID: "chat-1",
},
}, target, decision, nil); offers != nil {
		t.Fatalf("a relative target must offer no rungs, got %d", len(offers))
	}
}

// Each ladder offer keeps one granted_path. Quiet copies the matching grant.
func TestFilesystemLadderCompilesIntoAPlan(t *testing.T) {
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write",
Files: []string{"/etc/hosts"},
},
Scope: hitl.ActionScope{
ProjectDir: t.TempDir(),
SessionID: "chat-1",
},
}
	res, offers := filesystemCard(t, action)
	plan, err := hitl.CompileCheckpointApprovalPlan(hitl.CheckpointRequest{
		ProposedAction: &action, Decision: res.Decision, GrantOffers: offers,
	})
	testutil.FailErr(t, "hitl.CompileCheckpointApprovalPlan failed", err)
	byID := make(map[string]hitl.ApprovalOption, len(plan.Options))
	var taskQuiet hitl.ApprovalOption
	for _, option := range plan.Options {
		byID[option.ID] = option
		if option.Kind == hitl.ApprovalOptionQuiet && option.Rung == hitl.ApprovalRungChat {
			taskQuiet = option
		}
	}
	var dayOffer, chatOffer hitl.ApprovalGrantOffer
	for _, offer := range offers {
		option, ok := byID[offer.ID]
		if !ok {
			t.Fatalf("offer %s missing from plan", offer.ID)
		}
		if n := grantedPathCount(option); n != 1 {
			t.Fatalf("%s granted_path count = %d want 1", offer.ID, n)
		}
		switch offer.Rung {
		case hitl.ApprovalRungDay:
			dayOffer = offer
		case hitl.ApprovalRungChat:
			chatOffer = offer
		case hitl.ApprovalRungRedacted, hitl.ApprovalRungTracked, hitl.ApprovalRungUnchanged,
			hitl.ApprovalRungOnce, hitl.ApprovalRungProject, hitl.ApprovalRungDevice:
		}
	}
	if dayOffer.ID == "" || chatOffer.ID == "" {
		t.Fatal("expected day and task ladder offers")
	}
	if once := byID["approve_current_action"]; grantedPathCount(once) != 0 {
		t.Fatalf("Allow once must not install a granted path: %+v", once.Authority)
	}
	if taskQuiet.ID != "" {
		t.Fatalf("did not expect task quiet option when enabled task lease is present: %+v", taskQuiet)
	}
}

func grantedPathCount(option hitl.ApprovalOption) int {
	n := 0
	for _, delta := range option.Authority {
		if delta.Kind == hitl.AuthorityGrantedPath {
			n++
		}
	}
	return n
}

func TestOrdinaryOutsideReadGrantsTheContainingFolder(t *testing.T) {
	dir := filepath.Join(filepath.VolumeName(os.TempDir())+string(filepath.Separator), "unattached", t.Name())
	file := filepath.Join(dir, "release.go")

	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "read",
Files: []string{file},
},
Scope: hitl.ActionScope{
ProjectDir: t.TempDir(),
SessionID: "chat-1",
ProjectID: "proj-1",
},
}
	_, offers := filesystemCard(t, action)
	for _, offer := range offers {
		gp := offer.Grant.GrantedPath
		if gp == nil || !gp.Tree || gp.Path != dir || gp.Write {
			t.Fatalf("%s: ordinary outside read must grant the folder tree, got %+v", offer.Scope, gp)
		}
		if offer.Coverage != hitl.CoverageReadsOfTree(dir) &&
			offer.Coverage != hitl.CoverageReadsOfTree(dir)+hitl.DeviceCoverageSuffix {
			t.Fatalf("%s coverage = %q", offer.Scope, offer.Coverage)
		}
	}
}

func TestDirectoryReadGrantsThatDirectory(t *testing.T) {
	dir := canonDir(t, t.TempDir())
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "grep",
Files: []string{dir},
},
Scope: hitl.ActionScope{
ProjectDir: t.TempDir(),
SessionID: "chat-1",
ProjectID: "proj-1",
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

func TestHomeFileReadStaysExact(t *testing.T) {
	file := filepath.Join(home(t), "granted-path-exact.txt")
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "read",
Files: []string{file},
},
Scope: hitl.ActionScope{
ProjectDir: t.TempDir(),
SessionID: "chat-1",
ProjectID: "proj-1",
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
		if gp == nil || gp.Tree || gp.Path != file {
			t.Fatalf("%s: a file in $HOME must stay exact, got %+v", offer.Scope, gp)
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
Tool: tool,
Files: []string{path},
},
Scope: hitl.ActionScope{
ProjectDir: proj,
SessionID: "chat-1",
ProjectID: "proj-1",
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
Tool: "write",
Files: []string{sibling},
},
Scope: hitl.ActionScope{
ProjectDir: proj,
SessionID: "chat-1",
ProjectID: "proj-1",
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
Tool: tool,
Files: []string{dir},
},
Scope: hitl.ActionScope{
ProjectDir: t.TempDir(),
SessionID: "chat-1",
ProjectID: "proj-1",
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
Tool: "read",
Files: []string{ordinary},
},
Scope: hitl.ActionScope{
ProjectDir: proj,
SessionID: "chat-1",
ProjectID: "proj-1",
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
Tool: "read",
Files: []string{secret},
},
Scope: hitl.ActionScope{
ProjectDir: proj,
SessionID: "chat-1",
ProjectID: "proj-1",
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
Tool: "write",
Files: []string{file},
},
Scope: hitl.ActionScope{
ProjectDir: t.TempDir(),
SessionID: "chat-1",
ProjectID: "proj-1",
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
Tool: "read",
Files: []string{first},
},
Scope: hitl.ActionScope{
ProjectDir: proj,
SessionID: "chat-1",
ProjectID: "proj-1",
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
Tool: "read",
Files: []string{sibling},
},
Scope: hitl.ActionScope{
ProjectDir: proj,
SessionID: "chat-2",
ProjectID: "proj-1",
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

func TestALeaseOnOnePathDoesNotCoverAnother(t *testing.T) {
	covered := filepath.Join(filepath.VolumeName(os.TempDir())+string(filepath.Separator), "unattached", t.Name(), "covered")
	uncovered := filepath.Join(filepath.VolumeName(os.TempDir())+string(filepath.Separator), "unattached", t.Name(), "uncovered")
	tmp := t.TempDir()
	stageBundledApprovals(t, nil)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	testutil.FailErr(t, "PutGlobal strict", store.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureStrict}))
	locations, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "sensitivepath.Load failed", err)
	sources := settings.NoSources()
	sources.Locations = locations
	g := settings.NewRuleApprovalGate(store, sources)

	created, err := g.ApplyGrant(hitl.ApprovalGrant{
		ID:            "grant_tree",
		Scope:         hitl.ApprovalGrantScopeChat,
		Predicate:     hitl.ApprovalGrantPredicate{Category: string(settings.ApprovalCategoryPath), Pattern: covered},
		ChatSessionID: "chat-1",
		ProjectID:     "proj-1",
		Title:         hitl.TitleAllowForThisChat,
		Coverage:      hitl.CoverageReadsOfTree(covered),
		GrantedAt:     time.Now().UTC(),
		ExpiresWhen:   hitl.ExpiresWhenChatDeleted,
		ReaskWhen:     hitl.ReaskWhenOutsideFolder,
		GrantedPath:   &hitl.GrantedPathDelta{Path: covered, Tree: true},
	})
	testutil.FailErr(t, "ApplyGrant failed", err)
	if !created {
		t.Fatal("the tree lease was not installed")
	}

	action := func(files ...string) hitl.ProposedAction {
		return hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "read",
Files: files,
},
Scope: hitl.ActionScope{
ProjectDir: t.TempDir(),
SessionID: "chat-1",
ProjectID: "proj-1",
},
}
	}
	res, err := g.Evaluate(context.Background(), action(filepath.Join(covered, "a.go")))
	testutil.FailErr(t, "Evaluate covered failed", err)
	if res.Required() {
		t.Fatal("a path inside the granted tree must not ask again")
	}
	res, err = g.Evaluate(context.Background(),
		action(filepath.Join(covered, "a.go"), filepath.Join(uncovered, "b.go")))
	testutil.FailErr(t, "Evaluate mixed failed", err)
	if !res.Required() {
		t.Fatal("a second path outside the granted tree must still raise a card")
	}
}
