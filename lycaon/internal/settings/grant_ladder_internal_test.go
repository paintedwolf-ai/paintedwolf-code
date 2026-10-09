package settings

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// planFaceForOffers returns the plan's recommended option.
func planFaceForOffers(t *testing.T, action hitl.ProposedAction, offers []hitl.ApprovalGrantOffer) string {
	t.Helper()
	decision := askDecision(api.GateUserRule)
	primary, cited, reasons := hitl.PresentDecision(decision)
	options := []hitl.ApprovalOption{hitl.CurrentActionOption()}
	for _, offer := range offers {
		options = append(options, hitl.GrantOption(offer))
	}
	plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectAction, Title: "Test",
		Targets: []hitl.ApprovalTarget{{Kind: "action", Label: "test"}},
	}, hitl.ApprovalPresentation{
		Action: "Test", Impact: "Test.", Gate: primary, Cited: cited,
	}, reasons, options, hitl.FaceContext{})
	testutil.FailErr(t, "NewApprovalPlan", err)
	return plan.RecommendedOptionID
}

func primaryTaskRungID(offers []hitl.ApprovalGrantOffer) string {
	for _, offer := range offers {
		if offer.Rung == hitl.ApprovalRungChat && offer.Group == "" {
			return offer.ID
		}
	}
	return ""
}

func ladderGate(t *testing.T) *RuleApprovalGate {
	t.Helper()
	store, err := NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	return &RuleApprovalGate{store: store, grants: newSessionGrants(), quiets: newAskQuiets(), sources: NoSources()}
}

func ladderAction() hitl.ProposedAction {
	return hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "network",
Args: map[string]any{"host": "api.example.com"},
},
Scope: hitl.ActionScope{
SessionID: "sess-ladder",
ProjectID: "proj-ladder",
ProjectDir: "/tmp/proj",
},
}
}

func TestGrantOfferLadderShape(t *testing.T) {
	approvals := ladderGate(t)
	cases := []struct {
		name     string
		rule     ApprovalRule
		reuse    gate.Reuse
		scopes   []hitl.ApprovalGrantScope
		ttlFirst bool
	}{
		{
			name:  "host gets the full ladder",
			rule:  ApprovalRule{Category: ApprovalCategoryHost, Pattern: "api.example.com"},
			reuse: gate.Reuse{Shape: gate.ReusePredicate, Scope: gate.ScopeDevice},
			scopes: []hitl.ApprovalGrantScope{
				hitl.ApprovalGrantScopeProject, // time rung
				hitl.ApprovalGrantScopeChat,
				hitl.ApprovalGrantScopeDevice, // the one durable slot
			},
			ttlFirst: true,
		},
		{
			name:  "tool has no device rung",
			rule:  ApprovalRule{Category: ApprovalCategoryTool, Pattern: "fetch_url"},
			reuse: gate.Reuse{Shape: gate.ReusePredicate, Scope: gate.ScopeProject},
			scopes: []hitl.ApprovalGrantScope{
				hitl.ApprovalGrantScopeProject,
				hitl.ApprovalGrantScopeChat,
				hitl.ApprovalGrantScopeProject,
			},
			ttlFirst: true,
		},
		{
			name:  "write root carries the full ladder",
			rule:  ApprovalRule{Category: ApprovalCategoryWriteRoot, Pattern: "/tmp/cache"},
			reuse: gate.Reuse{Shape: gate.ReusePredicate, Scope: gate.ScopeDevice},
			scopes: []hitl.ApprovalGrantScope{
				hitl.ApprovalGrantScopeProject,
				hitl.ApprovalGrantScopeChat,
				hitl.ApprovalGrantScopeDevice,
			},
			ttlFirst: true,
		},
		{
			name:  "chat ceiling carries the day at chat scope",
			rule:  ApprovalRule{Category: ApprovalCategoryHost, Pattern: "api.example.com"},
			reuse: gate.Reuse{Shape: gate.ReusePredicate, Scope: gate.ScopeChat},
			scopes: []hitl.ApprovalGrantScope{
				hitl.ApprovalGrantScopeChat, // time rung
				hitl.ApprovalGrantScopeChat,
				hitl.ApprovalGrantScopeProject, // durable slot, disabled in place
			},
			ttlFirst: true,
		},
		{
			name:  "task ceiling stops at task",
			rule:  ApprovalRule{Category: ApprovalCategoryTool, Pattern: "fetch_url"},
			reuse: gate.Reuse{Shape: gate.ReusePredicate, Scope: gate.ScopeChat},
			scopes: []hitl.ApprovalGrantScope{
				hitl.ApprovalGrantScopeChat, // time rung
				hitl.ApprovalGrantScopeChat,
				hitl.ApprovalGrantScopeProject, // durable slot, disabled in place
			},
			ttlFirst: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			offers := approvals.grantOffersForPredicate(ladderAction(), tc.rule, true, tc.reuse, "")
			if len(offers) != len(tc.scopes) {
				t.Fatalf("offers = %d want %d: %+v", len(offers), len(tc.scopes), offers)
			}
			ttlCount := 0
			for i, offer := range offers {
				if offer.Scope != tc.scopes[i] {
					t.Fatalf("offer %d scope = %s want %s", i, offer.Scope, tc.scopes[i])
				}
				if offer.TTLSeconds > 0 {
					ttlCount++
					if i != 0 {
						t.Fatalf("time rung at position %d — the ladder puts it first", i)
					}
					if offer.Title != hitl.TitleAllowFor1Day {
						t.Fatalf("time rung title = %q", offer.Title)
					}
					if offer.Grant.TTLSeconds != offer.TTLSeconds {
						t.Fatal("offer and grant TTL disagree")
					}
				}
			}
			wantTTL := 0
			if tc.ttlFirst {
				wantTTL = 1
			}
			if ttlCount != wantTTL {
				t.Fatalf("time rungs = %d want %d (exactly one, counted)", ttlCount, wantTTL)
			}
			action := ladderAction()
			if face := planFaceForOffers(t, action, offers); face != primaryTaskRungID(offers) {
				t.Fatalf("recommended face = %q want task rung %q", face, primaryTaskRungID(offers))
			}
		})
	}
}

func TestStructuredMCPGrantIdentityGetsMCPDeviceLadder(t *testing.T) {
	approvals := ladderGate(t)
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "mcp_docs_search",
},
Resources: hitl.ActionResources{
ApprovalCategory: "mcp",
ApprovalSubject: "docs.search",
},
Scope: hitl.ActionScope{
SessionID: "sess-mcp",
ProjectID: "proj-mcp",
ProjectDir: "/tmp/proj",
},
}
	predicate := GrantPredicateForAction(action)
	if predicate.Category != ApprovalCategoryMCP || predicate.Pattern != "docs.search" {
		t.Fatalf("predicate = %+v, want structured MCP identity", predicate)
	}
	offers := approvals.grantOffersForPredicate(action, predicate, true, gate.Reuse{Shape: gate.ReusePredicate, Scope: gate.ScopeDevice}, "")
	foundDevice := false
	for _, offer := range offers {
		if offer.Scope == hitl.ApprovalGrantScopeDevice {
			foundDevice = true
			if offer.Title != hitl.TitleAllowOnThisDevice {
				t.Fatalf("device title = %q", offer.Title)
			}
		}
	}
	if !foundDevice {
		t.Fatal("structured MCP action must receive the durable device-duration rung")
	}
}

func TestGrantOfferLadderRefusesCommandAndEmptyPatterns(t *testing.T) {
	approvals := ladderGate(t)
	action := ladderAction()
	if offers := approvals.grantOffersForPredicate(action, ApprovalRule{Category: ApprovalCategoryCommand, Pattern: "git push"}, true, gate.Reuse{Shape: gate.ReusePredicate, Scope: gate.ScopeDevice}, ""); offers != nil {
		t.Fatalf("command minted offers: %+v", offers)
	}
	if offers := approvals.grantOffersForPredicate(action, ApprovalRule{Category: ApprovalCategoryTool, Pattern: ""}, true, gate.Reuse{Shape: gate.ReusePredicate, Scope: gate.ScopeDevice}, ""); offers != nil {
		t.Fatalf("empty pattern minted offers: %+v", offers)
	}
}

func TestTimeRungIdentityDistinctFromScopeRungs(t *testing.T) {
	approvals := ladderGate(t)
	offers := approvals.grantOffersForPredicate(
		ladderAction(),
		ApprovalRule{Category: ApprovalCategoryHost, Pattern: "api.example.com"},
		true,
		gate.Reuse{Shape: gate.ReusePredicate, Scope: gate.ScopeDevice},
		"",
	)
	seen := map[string]int{}
	for i, offer := range offers {
		if prev, dup := seen[offer.ID]; dup {
			t.Fatalf("offers %d and %d share id %s — the day and scope rungs must never collide", prev, i, offer.ID)
		}
		seen[offer.ID] = i
	}
}

// A card that carries both an ordinary predicate and a host-resource predicate
// still shows exactly one time rung and one recommended face — the ladder is
// per-card, not per-predicate.
func TestCombinedPredicatesMintOneTimeRungAndOneRecommended(t *testing.T) {
	approvals := ladderGate(t)
	action := ladderAction()
	action.Resources.HostResources = []string{"camera"}
	offers := approvals.GrantOffers(action, &hitl.ApprovalResult{
		Decision: askDecision(api.GateUserRule), HostResourceApproval: true,
	})
	if len(offers) < 5 {
		t.Fatalf("combined card lost its second predicate ladder: %d offers", len(offers))
	}
	ttlCount := 0
	for _, offer := range offers {
		if offer.TTLSeconds > 0 {
			ttlCount++
		}
	}
	if ttlCount != 1 {
		t.Fatalf("time rungs = %d want exactly 1 per card", ttlCount)
	}
	if face := planFaceForOffers(t, action, offers); face != primaryTaskRungID(offers) {
		t.Fatalf("recommended face = %q want task rung %q", face, primaryTaskRungID(offers))
	}
}

func TestDetectionCardsNeverMintAFamilyPredicate(t *testing.T) {
	approvals := ladderGate(t)
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "aws s3 rb s3://bucket --force"},
},
Scope: hitl.ActionScope{
SessionID: "sess-detect",
ProjectDir: "/tmp/proj",
},
Execution: hitl.ActionExecution{
Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{"/tmp/proj"}},
},
}
	offers := approvals.GrantOffers(action, &hitl.ApprovalResult{
		Decision: askDecision(api.GateAuthorityMisuse),
	})
	if len(offers) == 0 {
		t.Fatal("detection card minted no exact-action ladder")
	}
	for _, offer := range offers {
		if offer.Subject != gate.ReuseExactAction {
			t.Fatalf("offer subject = %q, want exact_action: %+v", offer.Subject, offer)
		}
		if len(offer.Grant.ExactActionSet) == 0 {
			t.Fatalf("detection offer missing ExactActionSet: %+v", offer)
		}
	}
}

// The narrowest gates keep the full ladder. What stays narrow is the *subject*:
// they may lease only the byte-identical action, never a family predicate. The
// duration is the person's to choose, as on every other card.
func TestNarrowGatesMintOnlyExactActionOffers(t *testing.T) {
	approvals := ladderGate(t)
	action := ladderAction()
	baseline := approvals.GrantOffers(action, &hitl.ApprovalResult{Decision: askDecision(api.GateUserRule)})
	if len(baseline) == 0 {
		t.Fatal("baseline ask minted no offers — the cases below would pass vacuously")
	}
	for _, g := range []api.ApprovalGate{api.GateConsentDrift, api.GateIncompleteFacts} {
		offers := approvals.GrantOffers(action, &hitl.ApprovalResult{Decision: askDecision(g)})
		if len(offers) == 0 {
			t.Fatalf("%s: no ladder — the only escape from a repeating ask is Advanced Off", g)
		}
		for _, offer := range offers {
			if offer.Subject != gate.ReuseExactAction {
				t.Errorf("%s: minted a %s offer — this gate may only lease the exact action", g, offer.Subject)
			}
			if offer.Scope == hitl.ApprovalGrantScopeDevice {
				t.Errorf("%s: minted a device rung — the exact-action ladder stops at project", g)
			}
		}
	}
	// Secret reuses through the fingerprint release, not this ladder.
	if offers := approvals.GrantOffers(action, &hitl.ApprovalResult{
		Decision: askDecision(api.GateSecretOutbound),
	}); offers != nil {
		t.Fatalf("secret minted ordinary ladder offers: %+v", offers)
	}
}

func TestApplyGrantMaterializesTTLAtApproval(t *testing.T) {
	approvals := ladderGate(t)
	offers := approvals.grantOffersForPredicate(
		ladderAction(),
		ApprovalRule{Category: ApprovalCategoryHost, Pattern: "api.example.com"},
		true,
		gate.Reuse{Shape: gate.ReusePredicate, Scope: gate.ScopeDevice},
		"",
	)
	day := offers[0]
	if day.TTLSeconds == 0 {
		t.Fatal("first rung is not the time rung")
	}
	// Relative TTLs start when approval is applied.
	before := time.Now().UTC()
	day.Grant.GrantedByPersonID = testutil.HostOwner().ID
	created, err := approvals.ApplyGrant(day.Grant)
	testutil.FailErr(t, "ApplyGrant day rung", err)
	if !created {
		t.Fatal("day lease not created")
	}
	stored := approvals.store.GlobalGrants()
	if len(stored) != 1 || stored[0].ExpiresAt == nil {
		t.Fatalf("stored grants = %+v", stored)
	}
	got := stored[0].ExpiresAt.Sub(before)
	if got < 23*time.Hour || got > 25*time.Hour {
		t.Fatalf("materialized expiry = %s from approval, want ~24h", got)
	}

	// An expiry already sooner than approval+TTL is never widened.
	capped := day.Grant
	sooner := before.Add(10 * time.Minute)
	capped.ExpiresAt = &sooner
	capped.ID += "-cap"
	created, err = approvals.ApplyGrant(capped)
	testutil.FailErr(t, "ApplyGrant capped", err)
	if !created {
		t.Fatal("capped lease not created")
	}
	for _, grant := range approvals.store.GlobalGrants() {
		if grant.ID == capped.ID && !grant.ExpiresAt.Equal(sooner) {
			t.Fatalf("TTL widened an earlier expiry: %s", grant.ExpiresAt)
		}
	}
}

// A declared-set ask carries no single host, so it must not mint a host-pattern
// grant. Both guards matter independently: the clamp reason refuses offers, and an
// empty predicate pattern refuses them again — a lease that could never match its
// own action would be a promise the ladder cannot keep.
func TestDeclaredEndpointSetMintsNoHostGrant(t *testing.T) {
	approvals := ladderGate(t)
	setAction := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "network",
Args: map[string]any{
			"hosts":                 []string{"crates.io", "pkg.go.dev"},
			"host_count":            2,
			"declared_hosts_digest": "set-digest",
		},
},
Scope: hitl.ActionScope{
SessionID: "sess-ladder",
ProjectDir: "/tmp/proj",
},
Execution: hitl.ActionExecution{
Contained: hitl.Contained{
			FSJailed:            true,
			Egress:              hitl.ContainedEgressProxy,
			Roots:               []string{"/tmp/proj"},
			DeclaredHostsDigest: "set-digest",
			DeclaredHostCount:   2,
		},
},
}
	if predicate := GrantPredicateForAction(setAction); predicate.Pattern != "" {
		t.Fatalf("a set action must yield no host pattern, got %q", predicate.Pattern)
	}
	if offers := approvals.grantOffersForPredicate(setAction, GrantPredicateForAction(setAction), true, gate.Reuse{Shape: gate.ReusePredicate, Scope: gate.ScopeDevice}, ""); offers != nil {
		t.Fatalf("empty predicate minted offers: %+v", offers)
	}
	result := &hitl.ApprovalResult{Decision: askDecision(api.GateSecretOutbound)}
	if offers := approvals.GrantOffers(setAction, result); offers != nil {
		t.Fatalf("untrusted-content set ask minted offers: %+v", offers)
	}
}

func TestApprovalWitnessIgnoresCapabilityOverlays(t *testing.T) {
	base := hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{"/tmp/proj"}}
	plain := hitl.BoundaryWitness(base)

	withSockets := base
	withSockets.SocketPathsDigest = "sock-a"
	withSockets.SocketCount = 1
	if !hitl.WitnessEqual(plain, hitl.BoundaryWitness(withSockets)) {
		t.Fatal("socket overlay changed the jail witness")
	}

	withSet := base
	withSet.DeclaredHostsDigest = "set-digest"
	withSet.DeclaredHostCount = 2
	if !hitl.WitnessEqual(plain, hitl.BoundaryWitness(withSet)) {
		t.Fatal("declared-host set changed the jail witness")
	}

	shrunk := base
	shrunk.Roots = []string{"/tmp/other"}
	if hitl.WitnessEqual(plain, hitl.BoundaryWitness(shrunk)) {
		t.Fatal("attached-root change left the witness unchanged")
	}
	denied := base
	denied.Egress = hitl.ContainedEgressDeny
	if hitl.WitnessEqual(plain, hitl.BoundaryWitness(denied)) {
		t.Fatal("egress-mode change left the witness unchanged")
	}
}

// Ladder fixtures cite only the gate that raised the card.
func askDecision(g api.ApprovalGate) *gate.Decision {
	return &gate.Decision{
		Primary: g,
		Cited:   []gate.Fact{{Gate: g, Key: "test", Value: "fixture", Source: "test"}},
	}
}

func TestDurableGrantWithoutAProjectIsRejected(t *testing.T) {
	store, err := NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	if err != nil {
		t.Fatalf("NewApprovalStoreAt: %v", err)
	}
	expires := time.Now().Add(time.Hour)
	_, err = store.UpsertGlobalGrant(ApprovalGrant{
		ID: "grant_no_project", Scope: hitl.ApprovalGrantScopeProject,
		Category: ApprovalCategoryTool, Pattern: "fetch_url",
		ProjectDir: "", ExpiresAt: &expires,
	})
	if err == nil {
		t.Fatal("a project-scoped grant with no project was accepted")
	}
}

func TestWriteRootGrantOffersDespiteFilesystemPrimary(t *testing.T) {
	approvals := ladderGate(t)
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write_root",
Args: map[string]any{"proposed_write_root": "/tmp/cache"},
},
Scope: hitl.ActionScope{
SessionID: "sess-wr",
ProjectID: "proj-wr",
ProjectDir: "/tmp/proj",
},
}
	// Broker evaluates sensitive_location / outside_roots; authority is still a
	// write-root lease, not a granted path.
	offers := approvals.GrantOffers(action, &hitl.ApprovalResult{
		Decision: askDecision(api.GateSensitiveLocation),
	})
	if len(offers) != 3 {
		t.Fatalf("write-root offers = %d want day+task+device: %+v", len(offers), offers)
	}
	if offers[0].TTLSeconds == 0 || offers[0].Title != hitl.TitleAllowFor1Day {
		t.Fatalf("first offer = %+v, want the day rung", offers[0])
	}
	if offers[0].Grant.Predicate.Category != string(ApprovalCategoryWriteRoot) {
		t.Fatalf("day predicate = %+v, want write_root", offers[0].Grant.Predicate)
	}
	if offers[0].Grant.ProjectID != "proj-wr" {
		t.Fatalf("day grant project_id = %q, want proj-wr", offers[0].Grant.ProjectID)
	}
	var deviceID string
	for _, offer := range offers {
		if offer.Scope == hitl.ApprovalGrantScopeDevice {
			deviceID = offer.ID
			if strings.Contains(offer.Coverage, hitl.DeviceCoverageSuffix) {
				t.Fatalf("write-root device coverage is cross-project, got %q", offer.Coverage)
			}
			if offer.Grant.Witness != (hitl.ApprovalGrantWitness{}) {
				t.Fatalf("device write-root pinned a jail witness: %+v", offer.Grant.Witness)
			}
		}
	}
	if deviceID == "" {
		t.Fatal("write-root ladder omitted the device rung")
	}
	other := action
	other.Scope.SessionID = "sess-other"
	other.Scope.ProjectID = "proj-other"
	other.Scope.ProjectDir = "/tmp/other"
	other.Execution.Contained.Roots = []string{"/tmp/other"}
	otherOffers := approvals.GrantOffers(other, &hitl.ApprovalResult{
		Decision: askDecision(api.GateSensitiveLocation),
	})
	for _, offer := range otherOffers {
		if offer.Scope == hitl.ApprovalGrantScopeDevice && offer.ID != deviceID {
			t.Fatalf("device write-root id moved across projects: %s vs %s", deviceID, offer.ID)
		}
	}
	offers[0].Grant.GrantedByPersonID = testutil.HostOwner().ID
	if _, err := approvals.ApplyGrant(offers[0].Grant); err != nil {
		t.Fatalf("ApplyGrant day write_root: %v", err)
	}
}

func TestDayRungRidesProjectIdentityWithoutFolder(t *testing.T) {
	approvals := ladderGate(t)
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write_root",
Args: map[string]any{"proposed_write_root": "/tmp/cache"},
},
Scope: hitl.ActionScope{
SessionID: "sess-wr",
ProjectID: "proj-nofolder",
},
}
	offers := approvals.GrantOffers(action, &hitl.ApprovalResult{
		Decision: askDecision(api.GateSensitiveLocation),
	})
	if len(offers) != 3 {
		t.Fatalf("no-folder project offers = %d want day+task+device: %+v", len(offers), offers)
	}
	if offers[0].Scope != hitl.ApprovalGrantScopeProject || offers[0].Grant.ProjectID != "proj-nofolder" {
		t.Fatalf("day carrier = %+v, want project identity without a folder", offers[0])
	}
}

func TestFolderWithoutProjectIdentityKeepsTheDurableSlotDisabled(t *testing.T) {
	approvals := ladderGate(t)
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write_root",
Args: map[string]any{"proposed_write_root": "/tmp/cache"},
},
Scope: hitl.ActionScope{
SessionID: "sess-wr",
ProjectDir: "/tmp/proj",
},
}
	offers := approvals.GrantOffers(action, &hitl.ApprovalResult{
		Decision: askDecision(api.GateSensitiveLocation),
	})
	if len(offers) != 3 {
		t.Fatalf("folder-only offers = %d want day+task+disabled slot: %+v", len(offers), offers)
	}
	if offers[0].Scope != hitl.ApprovalGrantScopeChat || offers[1].Scope != hitl.ApprovalGrantScopeChat {
		t.Fatalf("scopes = %+v, want task", offers)
	}
	if !offers[2].Disabled || offers[2].Note != hitl.NoteNoProjectOpen {
		t.Fatalf("durable slot = %+v, want disabled with the no-project note", offers[2])
	}
	if _, err := approvals.ApplyGrant(offers[0].Grant); err != nil {
		t.Fatalf("ApplyGrant task-capped day: %v", err)
	}
}

func TestTaskCeilingKeepsDayRungOnCard(t *testing.T) {
	approvals := ladderGate(t)
	reuse := gate.Reuse{Shape: gate.ReusePredicate, Scope: gate.ScopeChat}
	offers := withinReuse(
		approvals.grantOffersForPredicate(
			ladderAction(),
			ApprovalRule{Category: ApprovalCategoryHost, Pattern: "api.example.com"},
			true,
			reuse,
			"",
		),
		reuse,
	)
	if len(offers) != 3 {
		t.Fatalf("task-ceiling offers = %d want day+task+disabled slot: %+v", len(offers), offers)
	}
	if offers[0].TTLSeconds == 0 || offers[0].Scope != hitl.ApprovalGrantScopeChat {
		t.Fatalf("day carrier = %+v, want chat-scoped time rung", offers[0])
	}
	if offers[1].TTLSeconds != 0 || offers[1].Scope != hitl.ApprovalGrantScopeChat {
		t.Fatalf("second offer = %+v, want plain task lease", offers[1])
	}
	if !offers[2].Disabled || offers[2].Note != hitl.NoteEndsWithChat {
		t.Fatalf("durable slot = %+v, want disabled with the ends-with-task note", offers[2])
	}
}

func TestAbsorbedHostResourceOffersDeviceOnUnobservedChannel(t *testing.T) {
	approvals := ladderGate(t)
	action := ladderAction()
	action.Resources.HostResources = []string{"docker"}
	offers := approvals.AbsorbedGrantOffers(action, &hitl.ApprovalResult{
		Decision: askDecision(api.GateUnobservedChannel),
	})
	var sawDevice, sawProject, sawTask bool
	for _, offer := range offers {
		if offer.Grant.Predicate.Category != string(ApprovalCategoryHostResource) {
			continue
		}
		if offer.Group != hitl.GroupHostResources {
			t.Fatalf("host-resource offer group = %q, want Host resources: %+v", offer.Group, offer)
		}
		if offer.TTLSeconds > 0 {
			t.Fatalf("absorbed host-resource ladder minted a time rung: %+v", offer)
		}
		switch offer.Scope {
		case hitl.ApprovalGrantScopeDevice:
			sawDevice = true
			if strings.Contains(offer.Coverage, hitl.DeviceCoverageSuffix) {
				t.Fatalf("host-resource device coverage is cross-project, got %q", offer.Coverage)
			}
		case hitl.ApprovalGrantScopeProject:
			sawProject = true
		case hitl.ApprovalGrantScopeChat:
			sawTask = true
		}
	}
	if !sawTask || !sawDevice || sawProject {
		t.Fatalf("absorbed host-resource ladder = %+v, want task and the device slot only", offers)
	}
}

func TestExactActionGrantOffersSpanTheLadder(t *testing.T) {
	approvals := ladderGate(t)
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "echo hi"},
},
Scope: hitl.ActionScope{
SessionID: "sess-cmd",
ProjectID: "proj-cmd",
ProjectDir: "/tmp/proj",
},
Execution: hitl.ActionExecution{
Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{"/tmp/proj"}},
},
}
	if got := GrantPredicateForAction(action); got.Category != ApprovalCategoryCommand {
		t.Fatalf("predicate = %+v, want command so the exact-action ladder applies", got)
	}
	offers := approvals.GrantOffers(action, &hitl.ApprovalResult{
		Decision: askDecision(api.GateUnobservedChannel),
	})
	if len(offers) != 3 {
		t.Fatalf("exact-action offers = %d want day+task+project: %+v", len(offers), offers)
	}
	if offers[0].TTLSeconds == 0 || offers[0].Title != hitl.TitleAllowFor1Day {
		t.Fatalf("first offer = %+v, want the day rung", offers[0])
	}
	if offers[1].Title != hitl.TitleAllowForThisChat || offers[1].TTLSeconds != 0 {
		t.Fatalf("second offer = %+v, want the exact task lease", offers[1])
	}
	if offers[2].Title != hitl.TitleAllowForThisProject {
		t.Fatalf("third offer = %+v, want the durable rung every card now carries", offers[2])
	}
	if face := planFaceForOffers(t, action, offers); face != offers[1].ID {
		t.Fatalf("recommended face = %q want exact task lease %q", face, offers[1].ID)
	}
}
