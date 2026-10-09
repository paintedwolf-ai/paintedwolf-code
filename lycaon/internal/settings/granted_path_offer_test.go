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
			Tool:  "write",
			Files: []string{"/etc/hosts"},
		},
		Scope: hitl.ActionScope{
			ProjectDir: t.TempDir(),
			SessionID:  "chat-1",
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
			Tool:  "write",
			Files: []string{"/etc/hosts"},
		},
		Scope: hitl.ActionScope{
			ProjectID:  "project-stable",
			ProjectDir: t.TempDir(),
			SessionID:  "chat-1",
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
			Tool:  "read",
			Files: []string{filepath.Join(home(t), "Documents", "taxes.pdf")},
		},
		Scope: hitl.ActionScope{
			ProjectDir: t.TempDir(),
			SessionID:  "chat-1",
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
			Tool:  "write",
			Files: []string{"/etc/hosts"},
		},
		Scope: hitl.ActionScope{
			ProjectDir: t.TempDir(),
			SessionID:  "chat-1",
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
			Tool:  "read",
			Files: []string{covered, uncovered},
		},
		Scope: hitl.ActionScope{
			ProjectID:     "project-1",
			ProjectDir:    tmp,
			SessionID:     "chat-1",
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
			Tool:  "write",
			Files: []string{"/etc/hosts"},
		},
		Scope: hitl.ActionScope{
			ProjectID:  "project-1",
			ProjectDir: t.TempDir(),
			SessionID:  "chat-1",
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
			Tool:  "write",
			Files: []string{"/etc/hosts"},
		},
		Scope: hitl.ActionScope{
			ProjectDir: t.TempDir(),
			SessionID:  "chat-1",
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
			Tool:  "write",
			Files: []string{"/etc/hosts"},
		},
		Scope: hitl.ActionScope{
			ProjectDir: t.TempDir(),
			SessionID:  "chat-1",
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
			Tool:  "write",
			Files: []string{"/etc/hosts"},
		},
		Scope: hitl.ActionScope{
			ProjectDir: t.TempDir(),
			SessionID:  "chat-1",
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
				Tool:  "read",
				Files: files,
			},
			Scope: hitl.ActionScope{
				ProjectDir: t.TempDir(),
				SessionID:  "chat-1",
				ProjectID:  "proj-1",
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

func TestDirectoryHierarchyMintsDistinctOptionsAndDefaultsToContainingFolder(t *testing.T) {
	dir := canonDir(t, t.TempDir())
	folder := filepath.Join(dir, "src", "pkg")
	file := filepath.Join(folder, "file.go")
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{Tool: "read", Files: []string{file}},
		Scope:      hitl.ActionScope{ProjectID: "project", ProjectDir: t.TempDir(), SessionID: "chat"},
	}
	target := gate.FileTarget{Path: file, Mode: gate.ModeRead, OutsideRoots: true}
	decision := &gate.Decision{Primary: api.GateOutsideRootsRead}
	offers := settings.GrantedPathOffers(action, target, decision, nil)
	ids := map[string]bool{}
	scopes := []string{}
	options := []hitl.ApprovalOption{}
	for _, offer := range offers {
		if ids[offer.ID] {
			t.Fatalf("duplicate opaque id %s", offer.ID)
		}
		ids[offer.ID] = true
		if len(scopes) == 0 || scopes[len(scopes)-1] != offer.DirectoryScope {
			scopes = append(scopes, offer.DirectoryScope)
		}
		options = append(options, hitl.GrantOption(offer))
	}
	if len(scopes) < 3 || scopes[0] != folder || scopes[1] != filepath.Dir(folder) || scopes[len(scopes)-1] != string(filepath.Separator) {
		t.Fatalf("candidate scopes %v", scopes)
	}
	plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{Kind: hitl.ApprovalSubjectAction, Title: "Read file", Targets: []hitl.ApprovalTarget{{Kind: "file", Label: file}}}, hitl.ApprovalPresentation{Action: "Read file", Impact: "Read the selected directory", Gate: api.GateOutsideRootsRead, Cited: []hitl.PresentedFact{{Gate: api.GateOutsideRootsRead, Key: "path", Value: file, Source: "action"}}}, []api.ApprovalGate{api.GateOutsideRootsRead}, options, hitl.FaceContext{})
	testutil.FailErr(t, "compile directory plan", err)
	option, ok := plan.Option(plan.RecommendedOptionID)
	if !ok || option.DirectoryScope != folder || len(plan.DirectoryScopes) != len(scopes) {
		t.Fatalf("wrong default: %+v scopes=%v", option, plan.DirectoryScopes)
	}
	plan.DirectoryScopes[0] += string(filepath.Separator) + "."
	if err := plan.Validate(); err == nil {
		t.Fatal("noncanonical directory scope was admitted")
	}
}

func TestCredentialStoreReadAsksInsideAnAttachedHome(t *testing.T) {
	tmp := t.TempDir()
	stageBundledApprovals(t, nil)
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "approvals.yaml"))
	testutil.FailErr(t, "open approvals", err)
	testutil.FailErr(t, "set balanced", store.PutGlobal(settings.ApprovalConfig{Posture: gate.PostureBalanced}))
	locations, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "load locations", err)
	sources := settings.NoSources()
	sources.Locations = locations
	g := settings.NewRuleApprovalGate(store, sources)
	path := filepath.Join(home(t), ".aws", "credentials")
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{Tool: "read", Files: []string{path}},
		Scope:      hitl.ActionScope{ProjectID: "project", ProjectDir: home(t), SessionID: "chat"},
	}
	res, err := g.Evaluate(context.Background(), action)
	testutil.FailErr(t, "review credential read", err)
	if !res.Required() || res.Decision.Primary != api.GateSensitiveLocation {
		t.Fatalf("credential read inside home did not ask: %+v", res)
	}
	for _, offer := range g.GrantOffers(action, res) {
		if offer.Grant.GrantedPath == nil || offer.Grant.GrantedPath.Tree || offer.Grant.GrantedPath.Path != path {
			t.Fatalf("credential read widened: %+v", offer)
		}
	}
}

func TestMultipleReadTargetsOfferOneCrossingHierarchy(t *testing.T) {
	base := filepath.Join(filepath.VolumeName(os.TempDir())+string(filepath.Separator), "unattached", t.Name())
	first := filepath.Join(base, "first", "file.go")
	second := filepath.Join(base, "second", "file.go")
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{Tool: "read", Files: []string{first, second}},
		Scope:      hitl.ActionScope{ProjectID: "project", ProjectDir: t.TempDir(), SessionID: "chat"},
	}
	_, offers := filesystemCard(t, action)
	if len(offers) == 0 || offers[0].DirectoryScope != filepath.Dir(first) {
		t.Fatalf("wrong selected crossing hierarchy: %+v", offers)
	}
	if grantedpath.CoversPath(offers[0].DirectoryScope, true, second) {
		t.Fatal("nearest directory grant silently covered a sibling crossing")
	}
	previous := offers[0].DirectoryScope
	for _, offer := range offers {
		path := offer.DirectoryScope
		if path == "" || filepath.Clean(path) != path || !grantedpath.CoversPath(path, true, first) {
			t.Fatalf("option did not belong to selected crossing: %+v", offer)
		}
		if path != previous && !grantedpath.CoversPath(path, true, previous) {
			t.Fatalf("unrelated crossing added to hierarchy: %q after %q", path, previous)
		}
		previous = path
	}
}
