package tools

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func socketPlanAction() hitl.ProposedAction {
	return hitl.ProposedAction{
		Tool: "command", Command: "docker version", SessionID: "sess-docker",
		ProjectDir: "/tmp/gitea", ProjectID: "proj-gitea",
	}
}

func socketPlanPresentation() (hitl.ApprovalPresentation, []api.ApprovalGate) {
	_, decision := gate.Evaluate(gate.Facts{
		Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest,
		ApprovalRequest: &gate.ApprovalRequest{Count: 1},
	}, gate.DefaultPosture)
	primary, cited, reasons := hitl.PresentDecision(decision)
	return hitl.ApprovalPresentation{
		Action: "Connect to local services", Impact: "Connect to 1 local service target(s).",
		Gate: primary, Cited: cited,
	}, reasons
}

func TestSocketProjectDayPlanContinuesHeldSubject(t *testing.T) {
	action := socketPlanAction()
	grant := confine.SocketGrant{ApprovedPath: "/var/run/docker.sock", ResolvedPath: "/var/run/docker.sock"}
	offers := SocketExecutionGrantOffers(action, []confine.SocketGrant{grant})
	if len(offers) != 3 || offers[0].Rung != hitl.ApprovalRungDay || offers[0].Scope != hitl.ApprovalGrantScopeProject || offers[2].Rung != hitl.ApprovalRungProject {
		t.Fatalf("project-backed socket offers = %+v", offers)
	}
	permit := socketPermitDelta(action, "digest", ToolContext{ToolCallID: "call-1"}, []hitl.ApprovalSocketTarget{
		{ApprovedPath: grant.ApprovedPath, ResolvedPath: grant.ResolvedPath},
	})
	options := socketCapabilityOptions(permit, offers, nil)
	presentation, reasons := socketPlanPresentation()
	plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectSocketSet, Title: "Allow local service: docker.sock",
		Targets: []hitl.ApprovalTarget{{Kind: "socket", Label: grant.ApprovedPath}},
	}, presentation, reasons, options, hitl.FaceContext{})
	testutil.FailErr(t, "NewApprovalPlan", err)

	var day hitl.ApprovalOption
	for _, option := range plan.Options {
		if option.Rung == hitl.ApprovalRungDay && option.Group == "" {
			day = option
		}
	}
	if day.ID == "" {
		t.Fatal("composed plan has no primary day option")
	}
	if !plan.OptionContinues(day) {
		t.Fatalf("project-day option does not continue socket_set: %+v", day.Authority)
	}
	if len(day.Authority) < 2 || day.Authority[0].Kind != hitl.AuthoritySocketPermit {
		t.Fatalf("project-day authority = %+v, want current-call permit then durable grant", day.Authority)
	}
	sawGrant := false
	for _, delta := range day.Authority {
		if delta.Kind == hitl.AuthorityGenericGrant {
			sawGrant = true
		}
	}
	if !sawGrant {
		t.Fatalf("project-day option dropped the reusable grant: %+v", day.Authority)
	}
}

func TestSocketGenericGrantOnlyDoesNotContinue(t *testing.T) {
	action := socketPlanAction()
	offers := SocketExecutionGrantOffers(action, []confine.SocketGrant{{
		ApprovedPath: "/var/run/docker.sock", ResolvedPath: "/var/run/docker.sock",
	}})
	day := offers[0]
	if len(day.Authority) != 1 || day.Authority[0].Kind != hitl.AuthorityGenericGrant {
		t.Fatalf("fixture changed: project-day offer authority = %+v", day.Authority)
	}
	presentation, reasons := socketPlanPresentation()
	_, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectSocketSet, Title: "Allow local services",
		Targets: []hitl.ApprovalTarget{{Kind: "socket", Label: "/var/run/docker.sock"}},
	}, presentation, reasons, []hitl.ApprovalOption{hitl.GrantOption(day)}, hitl.FaceContext{})
	if err == nil || !strings.Contains(err.Error(), "cannot continue its held subject") {
		t.Fatalf("generic-grant-only day option should fail Validate, got %v", err)
	}
}

func TestCombinedDirectIPAuthorityMatchesPrimaryRung(t *testing.T) {
	action := hitl.ProposedAction{Tool: "command", SessionID: "worker", RootSessionID: "root"}
	direct := directIPApprovalReview{Action: action, Lease: hitl.DirectIPLease{
		ActionDigest: "action", RequestDigest: "request", ConfinementDigest: "confine",
	}}
	tc := ToolContext{ToolCallID: "call"}

	once := hitl.ApprovalOption{Kind: hitl.ApprovalOptionCurrentAction, Rung: hitl.ApprovalRungOnce}
	assertDirectAuthorityKinds(t, combinedDirectIPAuthority(once, direct, tc), hitl.AuthorityDirectIPPermit)

	day := hitl.ApprovalOption{Kind: hitl.ApprovalOptionLease, Rung: hitl.ApprovalRungDay, Group: ""}
	dayDeltas := combinedDirectIPAuthority(day, direct, tc)
	assertDirectAuthorityKinds(t, dayDeltas, hitl.AuthorityDirectIPChat, hitl.AuthorityDirectIPPermit)
	if dayDeltas[0].TTLSeconds != hitl.DayRungTTLSeconds {
		t.Fatalf("day TTL = %d, want %d", dayDeltas[0].TTLSeconds, hitl.DayRungTTLSeconds)
	}

	task := hitl.ApprovalOption{Kind: hitl.ApprovalOptionLease, Rung: hitl.ApprovalRungChat, Group: ""}
	taskDeltas := combinedDirectIPAuthority(task, direct, tc)
	assertDirectAuthorityKinds(t, taskDeltas, hitl.AuthorityDirectIPChat, hitl.AuthorityDirectIPPermit)
	if taskDeltas[0].TTLSeconds != 0 {
		t.Fatalf("task TTL = %d, want 0", taskDeltas[0].TTLSeconds)
	}

	absorbed := hitl.ApprovalOption{
		Kind: hitl.ApprovalOptionLease, Rung: hitl.ApprovalRungDay, Group: hitl.GroupAlsoAllow,
	}
	assertDirectAuthorityKinds(t, combinedDirectIPAuthority(absorbed, direct, tc), hitl.AuthorityDirectIPPermit)
}

func TestAttachRealizationWriteRootsSkipsOnce(t *testing.T) {
	action := socketPlanAction()
	permit := socketPermitDelta(action, "digest", ToolContext{ToolCallID: "call-1"}, []hitl.ApprovalSocketTarget{
		{ApprovedPath: "/var/run/docker.sock", ResolvedPath: "/var/run/docker.sock"},
	})
	offers := SocketExecutionGrantOffers(action, []confine.SocketGrant{{
		ApprovedPath: "/var/run/docker.sock", ResolvedPath: "/var/run/docker.sock",
	}})
	options := attachRealizationWriteRoots(socketCapabilityOptions(permit, offers, nil), action, []string{"/Users/me/.docker/buildx"})
	sawLeaseWriteRoot := false
	for _, option := range options {
		hasWriteRoot := false
		for _, delta := range option.Authority {
			if delta.Kind == hitl.AuthorityWriteRootChat {
				hasWriteRoot = true
				if len(delta.WriteRoots) != 1 || delta.WriteRoots[0] != "/Users/me/.docker/buildx" {
					t.Fatalf("write roots = %v", delta.WriteRoots)
				}
			}
		}
		if option.Kind == hitl.ApprovalOptionCurrentAction && hasWriteRoot {
			t.Fatal("once must not carry reusable write-root authority")
		}
		if option.Kind == hitl.ApprovalOptionLease && hasWriteRoot {
			sawLeaseWriteRoot = true
		}
	}
	if !sawLeaseWriteRoot {
		t.Fatal("day/task leases must install realization write roots")
	}
}
