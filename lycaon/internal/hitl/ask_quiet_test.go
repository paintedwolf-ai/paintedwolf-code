package hitl_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDetectionOnlyCardFacesTaskAcknowledgement(t *testing.T) {
	t.Parallel()
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Scope: hitl.ActionScope{
SessionID: "chat-1",
RootSessionID: "chat-1",
},
Presentation: hitl.ActionPresentation{
Command: "aws s3 rb s3://x --force",
},
}
	decision := &gate.Decision{
		Primary:   api.GateAuthorityMisuse,
		ReasonKey: "authority_misuse:aws-cli/s3-remove-bucket",
		Cited: []gate.Fact{{
			Gate: api.GateAuthorityMisuse, Key: "detection.rule",
			Value: "s3-remove-bucket", Source: "detection_pack",
		}},
	}
	options := append([]hitl.ApprovalOption{hitl.CurrentActionOption()},
		hitl.QuietOptions(action, decision, nil, nil)...)
	plan, err := hitl.NewApprovalPlan(
		action, hitl.ApprovalStagePreSpawn,
		hitl.ApprovalSubject{
			Kind: hitl.ApprovalSubjectAction, Title: "Approve command",
			Targets: []hitl.ApprovalTarget{{Kind: "action", Label: action.Presentation.Command}},
		},
		hitl.ApprovalPresentation{
			Action: "Run command", Impact: "Deletes a bucket.",
			Gate: api.GateAuthorityMisuse,
			Cited: []hitl.PresentedFact{{
				Gate: api.GateAuthorityMisuse, Key: "detection.rule",
				Value: "s3-remove-bucket", Source: "detection_pack",
			}},
		},
		[]api.ApprovalGate{api.GateAuthorityMisuse},
		options,
		hitl.FaceContext{})
	testutil.FailErr(t, "NewApprovalPlan", err)
	if plan.RecommendedOptionID == "" {
		t.Fatal("expected a face")
	}
	face, ok := plan.Option(plan.RecommendedOptionID)
	if !ok {
		t.Fatalf("face %q missing", plan.RecommendedOptionID)
	}
	if face.Kind != hitl.ApprovalOptionQuiet || face.Rung != hitl.ApprovalRungChat {
		t.Fatalf("detection-only face = %+v, want task acknowledgement", face)
	}
	var quietCount int
	for _, opt := range plan.Options {
		if opt.Kind == hitl.ApprovalOptionQuiet {
			quietCount++
			if !plan.OptionContinues(opt) {
				t.Fatalf("quiet option must approve the held action: %+v", opt)
			}
			if opt.Group != hitl.GroupQuiet {
				t.Fatalf("quiet group = %q", opt.Group)
			}
		}
	}
	if quietCount != 1 {
		t.Fatalf("quiet options = %d want one task row", quietCount)
	}
}

func TestDetectionAcknowledgementDoesNotFaceCoFiringGate(t *testing.T) {
	t.Parallel()
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Scope: hitl.ActionScope{
SessionID: "chat-1",
},
Presentation: hitl.ActionPresentation{
Command: "rm -rf /etc/example",
},
}
	decision := &gate.Decision{
		Primary: api.GateAuthorityMisuse,
		Also:    []api.ApprovalGate{api.GateOutsideRootsWrite},
		ReasonKey: "authority_misuse:command-destructive/recursive-delete|" +
			"outside_roots_write:write:/etc",
		Cited: []gate.Fact{
			{Gate: api.GateAuthorityMisuse, Key: "detection.rule", Value: "recursive delete", Source: "detection_pack"},
			{Gate: api.GateOutsideRootsWrite, Key: "file.path", Value: "/etc/example", Source: "confine"},
		},
	}
	options := append([]hitl.ApprovalOption{hitl.CurrentActionOption()},
		hitl.QuietOptions(action, decision, nil, nil)...)
	plan, err := hitl.NewApprovalPlan(
		action, hitl.ApprovalStagePreSpawn,
		hitl.ApprovalSubject{
			Kind: hitl.ApprovalSubjectAction, Title: "Approve command",
			Targets: []hitl.ApprovalTarget{{Kind: "action", Label: action.Presentation.Command}},
		},
		hitl.ApprovalPresentation{
			Action: "Run command", Impact: "Delete a path.", Gate: api.GateAuthorityMisuse,
			Cited: []hitl.PresentedFact{
				{Gate: api.GateAuthorityMisuse, Key: "detection.rule", Value: "recursive delete", Source: "detection_pack"},
				{Gate: api.GateOutsideRootsWrite, Key: "file.path", Value: "/etc/example", Source: "confine"},
			},
		},
		[]api.ApprovalGate{api.GateAuthorityMisuse, api.GateOutsideRootsWrite}, options,
		hitl.FaceContext{})
	testutil.FailErr(t, "NewApprovalPlan", err)
	face, ok := plan.Option(plan.RecommendedOptionID)
	if !ok || face.Kind == hitl.ApprovalOptionQuiet {
		t.Fatalf("co-firing gate faced acknowledgement: %+v", face)
	}
}

func TestQuietOptionCopyIsDurationPlusSubject(t *testing.T) {
	t.Parallel()
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Scope: hitl.ActionScope{
SessionID: "chat-1",
RootSessionID: "chat-1",
},
Presentation: hitl.ActionPresentation{
Command: "aws s3 rb s3://x --force",
},
}
	decision := &gate.Decision{
		Primary:   api.GateAuthorityMisuse,
		ReasonKey: "authority_misuse:aws-cli/s3-remove-bucket",
		Cited: []gate.Fact{{
			Gate: api.GateAuthorityMisuse, Key: "detection.rule",
			Value: "s3-remove-bucket", Source: "detection_pack",
		}},
	}
	got := hitl.QuietOptions(action, decision, nil, nil)
	if len(got) != 1 {
		t.Fatalf("quiet options = %d want the one task row", len(got))
	}
	if got[0].Title != hitl.TitleQuietForThisChat || got[0].Coverage != "aws-cli / s3-remove-bucket" || got[0].Rung != hitl.ApprovalRungChat {
		t.Fatalf("task quiet = %+v", got[0])
	}
}

func TestQuietInstallsMatchingGrant(t *testing.T) {
	t.Parallel()
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Scope: hitl.ActionScope{
SessionID: "chat-1",
RootSessionID: "chat-1",
},
Presentation: hitl.ActionPresentation{
Command: "curl https://example.com",
},
}
	decision := &gate.Decision{
		Primary:   api.GateUnobservedChannel,
		ReasonKey: "unobserved_channel:direct_ip",
		Cited:     []gate.Fact{{Gate: api.GateUnobservedChannel, Key: "k", Value: "v", Source: "t"}},
	}
	dayGrant := hitl.ApprovalGrant{ID: "grant_day", Scope: hitl.ApprovalGrantScopeChat, Title: hitl.TitleAllowFor1Day}
	chatGrant := hitl.ApprovalGrant{ID: "grant_task", Scope: hitl.ApprovalGrantScopeChat, Title: hitl.TitleAllowForThisChat}
	options := []hitl.ApprovalOption{
		hitl.CurrentActionOption(),
		{
			ID: "day", Kind: hitl.ApprovalOptionLease, Rung: hitl.ApprovalRungDay, Scope: hitl.ApprovalGrantScopeChat,
			Title: hitl.TitleAllowFor1Day, Coverage: "c", ExpiresWhen: hitl.ExpiresIn1DayOrChatDeleted, ReaskWhen: "r",
			DecisionAction: hitl.ApprovalOptionApprove,
			Authority:      []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityGenericGrant, Grant: &dayGrant}},
		},
	}
	options = append(options, hitl.QuietOptions(action, decision, nil, nil)...)
	plan, err := hitl.NewApprovalPlan(
		action, hitl.ApprovalStagePreSpawn,
		hitl.ApprovalSubject{
			Kind: hitl.ApprovalSubjectAction, Title: "Approve command",
			Targets: []hitl.ApprovalTarget{{Kind: "action", Label: action.Presentation.Command}},
		},
		hitl.ApprovalPresentation{
			Action: "Run command", Impact: "Reach the network.",
			Gate: api.GateUnobservedChannel,
			Cited: []hitl.PresentedFact{{
				Gate: api.GateUnobservedChannel, Key: "k", Value: "v", Source: "t",
			}},
		},
		[]api.ApprovalGate{api.GateUnobservedChannel},
		options,
		hitl.FaceContext{})
	testutil.FailErr(t, "NewApprovalPlan", err)
	var taskQuiet hitl.ApprovalOption
	quiets := 0
	for _, opt := range plan.Options {
		if opt.Kind != hitl.ApprovalOptionQuiet {
			continue
		}
		quiets++
		if !plan.OptionContinues(opt) {
			t.Fatalf("quiet %q must continue: %+v", opt.ID, opt)
		}
		if opt.Rung == hitl.ApprovalRungChat {
			taskQuiet = opt
		}
	}
	if quiets != 1 || taskQuiet.ID == "" {
		t.Fatalf("expected exactly one task quiet option, got %d", quiets)
	}
	if !quietCarriesGrant(taskQuiet, "grant_day") {
		t.Fatalf("task quiet missing bounded day grant: %+v", taskQuiet.Authority)
	}
	if !quietCarriesAskQuiet(taskQuiet) {
		t.Fatal("quiet option must install ask suppression")
	}

	taskOption := hitl.ApprovalOption{
		ID: "task", Kind: hitl.ApprovalOptionLease, Rung: hitl.ApprovalRungChat, Scope: hitl.ApprovalGrantScopeChat,
		Title: hitl.TitleAllowForThisChat, Coverage: "c", ExpiresWhen: hitl.ExpiresWhenChatDeleted, ReaskWhen: "r",
		DecisionAction: hitl.ApprovalOptionApprove,
		Authority:      []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityGenericGrant, Grant: &chatGrant}},
	}
	planWithTask, err := hitl.NewApprovalPlan(
		action, hitl.ApprovalStagePreSpawn,
		hitl.ApprovalSubject{
			Kind: hitl.ApprovalSubjectAction, Title: "Approve command",
			Targets: []hitl.ApprovalTarget{{Kind: "action", Label: action.Presentation.Command}},
		},
		hitl.ApprovalPresentation{
			Action: "Run command", Impact: "Reach the network.",
			Gate: api.GateUnobservedChannel,
			Cited: []hitl.PresentedFact{{
				Gate: api.GateUnobservedChannel, Key: "k", Value: "v", Source: "t",
			}},
		},
		[]api.ApprovalGate{api.GateUnobservedChannel},
		append(options, taskOption),
		hitl.FaceContext{})
	testutil.FailErr(t, "NewApprovalPlan with chat lease", err)
	for _, opt := range planWithTask.Options {
		if opt.Kind == hitl.ApprovalOptionQuiet {
			t.Fatalf("quiet option was retained despite enabled chat lease: %+v", opt)
		}
	}
}

// Quiet duration is bounded by the available approval options.
func TestQuietNeverCarriesDurableAuthority(t *testing.T) {
	t.Parallel()
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "read",
Files: []string{"/etc/hosts"},
},
Scope: hitl.ActionScope{
SessionID: "chat-1",
RootSessionID: "chat-1",
ProjectID: "proj-1",
ProjectDir: "/proj",
},
}
	decision := &gate.Decision{
		Primary:   api.GateSensitiveLocation,
		ReasonKey: "sensitive_location:hosts",
		Cited:     []gate.Fact{{Gate: api.GateSensitiveLocation, Key: "k", Value: "v", Source: "t"}},
	}
	deviceGrant := hitl.ApprovalGrant{
		ID: "grant_device", Scope: hitl.ApprovalGrantScopeDevice,
		Title: hitl.TitleAllowOnThisDevice, ProjectID: "proj-1",
	}
	projectGrant := hitl.ApprovalGrant{
		ID: "grant_project", Scope: hitl.ApprovalGrantScopeProject,
		Title: hitl.TitleAllowForThisProject, ProjectID: "proj-1",
	}
	// A ladder with no once, day, or chat rung: nothing here is chat-bounded.
	options := []hitl.ApprovalOption{
		{
			ID: "project", Kind: hitl.ApprovalOptionLease, Rung: hitl.ApprovalRungProject,
			Scope: hitl.ApprovalGrantScopeProject, Title: hitl.TitleAllowForThisProject,
			Coverage: "c", ExpiresWhen: hitl.ExpiresIn7DaysOrRevoked, ReaskWhen: "r",
			DecisionAction: hitl.ApprovalOptionApprove,
			Authority:      []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityGenericGrant, Grant: &projectGrant}},
		},
		{
			ID: "device", Kind: hitl.ApprovalOptionLease, Rung: hitl.ApprovalRungDevice,
			Scope: hitl.ApprovalGrantScopeDevice, Title: hitl.TitleAllowOnThisDevice,
			Coverage: "c", ExpiresWhen: hitl.ExpiresIn30DaysOrRevoked, ReaskWhen: "r",
			DecisionAction: hitl.ApprovalOptionApprove,
			Authority:      []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityGenericGrant, Grant: &deviceGrant}},
		},
	}
	options = append(options, hitl.QuietOptions(action, decision, nil, nil)...)
	plan, err := hitl.NewApprovalPlan(
		action, hitl.ApprovalStagePreSpawn,
		hitl.ApprovalSubject{
			Kind: hitl.ApprovalSubjectAction, Title: "Approve read",
			Targets: []hitl.ApprovalTarget{{Kind: "action", Label: "/etc/hosts"}},
		},
		hitl.ApprovalPresentation{
			Action: "Use read", Impact: "Read a catalogued location.",
			Gate: api.GateSensitiveLocation,
			Cited: []hitl.PresentedFact{{
				Gate: api.GateSensitiveLocation, Key: "k", Value: "v", Source: "t",
			}},
		},
		[]api.ApprovalGate{api.GateSensitiveLocation},
		options,
		hitl.FaceContext{})
	testutil.FailErr(t, "NewApprovalPlan", err)
	for _, opt := range plan.Options {
		if opt.Kind == hitl.ApprovalOptionQuiet {
			t.Fatalf("quiet survived a ladder with no chat-bounded rung: %+v", opt.Authority)
		}
	}
}

// Validation refuses the shape directly, so no future mint site can construct it.
func TestQuietCarryingDurableGrantIsRejected(t *testing.T) {
	t.Parallel()
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Scope: hitl.ActionScope{
SessionID: "chat-1",
RootSessionID: "chat-1",
},
Presentation: hitl.ActionPresentation{
Command: "curl https://example.com",
},
}
	durable := hitl.ApprovalGrant{
		ID: "grant_device", Scope: hitl.ApprovalGrantScopeDevice, Title: hitl.TitleAllowOnThisDevice,
	}
	quiet := hitl.ApprovalOption{
		ID: "quiet_bad", Kind: hitl.ApprovalOptionQuiet, Rung: hitl.ApprovalRungChat,
		Group: hitl.GroupQuiet, Title: hitl.TitleQuietForThisChat, Coverage: "this reason",
		ExpiresWhen: hitl.ExpiresWhenChatDeletedOrRevoked, ReaskWhen: hitl.ReaskWhenQuietRevoked,
		DecisionAction: hitl.ApprovalOptionApprove,
		Authority: []hitl.ApprovalAuthorityDelta{
			{Kind: hitl.AuthorityGenericGrant, Grant: &durable},
			{
				Kind: hitl.AuthorityAskQuiet, ChatSessionID: "chat-1",
				AskQuiet: &hitl.AskQuietDelta{ID: "q1", Key: "k", Label: "l"},
			},
		},
	}
	_, err := hitl.NewApprovalPlan(
		action, hitl.ApprovalStagePreSpawn,
		hitl.ApprovalSubject{
			Kind: hitl.ApprovalSubjectAction, Title: "Approve command",
			Targets: []hitl.ApprovalTarget{{Kind: "action", Label: action.Presentation.Command}},
		},
		hitl.ApprovalPresentation{
			Action: "Run command", Impact: "Reach the network.",
			Gate: api.GateUnobservedChannel,
			Cited: []hitl.PresentedFact{{
				Gate: api.GateUnobservedChannel, Key: "k", Value: "v", Source: "t",
			}},
		},
		[]api.ApprovalGate{api.GateUnobservedChannel},
		[]hitl.ApprovalOption{hitl.CurrentActionOption(), quiet},
		hitl.FaceContext{})
	if err == nil {
		t.Fatal("plan accepted a quiet option carrying a device grant")
	}
	if !strings.Contains(err.Error(), "chat-bounded") {
		t.Fatalf("error %q does not name the invariant", err)
	}
}

func quietCarriesGrant(opt hitl.ApprovalOption, grantID string) bool {
	for _, delta := range opt.Authority {
		if delta.Grant != nil && delta.Grant.ID == grantID {
			return true
		}
	}
	return false
}

func quietCarriesAskQuiet(opt hitl.ApprovalOption) bool {
	for _, delta := range opt.Authority {
		if delta.Kind == hitl.AuthorityAskQuiet {
			return true
		}
	}
	return false
}

func TestDiscretionaryGatesCarryAQuietRung(t *testing.T) {
	t.Parallel()
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Scope: hitl.ActionScope{
SessionID: "chat-1",
RootSessionID: "chat-1",
},
Presentation: hitl.ActionPresentation{
Command: "curl https://example.com",
},
}
	for _, g := range gate.All() {
		decision := &gate.Decision{
			Primary:   g,
			ReasonKey: string(g) + ":subject",
			Cited: []gate.Fact{{
				Gate: g, Key: "k", Value: "v", Source: "test",
			}},
		}
		got := hitl.QuietOptions(action, decision, nil, nil)
		if !decision.OffersQuiet() {
			if len(got) != 0 {
				t.Fatalf("%s offered a quiet", g)
			}
			continue
		}
		if len(got) == 0 {
			t.Errorf("%s: no quiet rung", g)
			continue
		}
		for _, opt := range got {
			if opt.Group != hitl.GroupQuiet {
				t.Errorf("%s: quiet option in group %q", g, opt.Group)
			}
		}
	}
}

func TestCarveOutGatesQuietOnlyTheExactAction(t *testing.T) {
	t.Parallel()
	for _, g := range []api.ApprovalGate{
		api.GateConsentDrift, api.GateIncompleteFacts, api.GateExplicitApprovalRequest,
	} {
		if !gate.ExactActionQuiet(g) {
			t.Errorf("%s must scope quiet to the exact action", g)
			continue
		}
		decision := &gate.Decision{Primary: g, ReasonKey: string(g) + ":constant"}
		subjects := hitl.QuietSubjectsFromDecision(decision, nil, "digest-abc")
		if len(subjects) != 1 {
			t.Fatalf("%s: subjects = %d want 1", g, len(subjects))
		}
		if !strings.Contains(subjects[0].Key, "digest-abc") {
			t.Errorf("%s: quiet key %q is not scoped to the action digest", g, subjects[0].Key)
		}
		if subjects[0].Label != hitl.QuietLabelThisExactAction {
			t.Errorf("%s: label = %q", g, subjects[0].Label)
		}
	}

	// A class-scoped gate must not pick up the digest.
	decision := &gate.Decision{Primary: api.GateFirstHost, ReasonKey: "first_host:example.com"}
	subjects := hitl.QuietSubjectsFromDecision(decision, nil, "digest-abc")
	if len(subjects) != 1 || strings.Contains(subjects[0].Key, "digest-abc") {
		t.Fatalf("first_host quiet must be keyed by host, got %+v", subjects)
	}
}

func TestDecisionFullyQuietedNeedsEveryCoFiringReason(t *testing.T) {
	t.Parallel()
	decision := &gate.Decision{
		Primary:   api.GateAuthorityMisuse,
		Also:      []api.ApprovalGate{api.GateOutsideRootsWrite},
		ReasonKey: "authority_misuse:aws-cli/s3-remove-bucket|outside_roots_write:write:/etc",
		Cited: []gate.Fact{
			{Gate: api.GateAuthorityMisuse, Key: "detection.rule", Value: "x", Source: "pack"},
			{Gate: api.GateOutsideRootsWrite, Key: "file.mode", Value: "write", Source: "fs"},
		},
	}
	onlyDetection := func(k string) bool { return k == "authority_misuse:aws-cli/s3-remove-bucket" }
	if hitl.DecisionFullyQuieted(decision, nil, "digest", onlyDetection) {
		t.Fatal("quieting the detection must not silence the co-firing outside_roots ask")
	}
	if !hitl.DecisionFullyQuieted(decision, nil, "digest", func(string) bool { return true }) {
		t.Fatal("both reasons quieted must silence the card")
	}
}

// A gate that fired but produced no reason segment has no quiet subject. Covering
// its siblings must not silence it by omission.
func TestDecisionFullyQuietedFailsClosedOnUnrepresentedGate(t *testing.T) {
	t.Parallel()
	decision := &gate.Decision{
		Primary: api.GateAuthorityMisuse,
		Also:    []api.ApprovalGate{api.GateConsentDrift},
		// consent_drift fired but contributed no segment.
		ReasonKey: "authority_misuse:aws-cli/s3-remove-bucket",
		Cited: []gate.Fact{
			{Gate: api.GateAuthorityMisuse, Key: "detection.rule", Value: "x", Source: "pack"},
		},
	}
	if hitl.DecisionFullyQuieted(decision, nil, "digest", func(string) bool { return true }) {
		t.Fatal("a gate with no quiet subject must keep the card")
	}
}

func TestDecisionFullyQuietedSilencesOnlyThatSegment(t *testing.T) {
	t.Parallel()
	decision := &gate.Decision{
		Primary:   api.GateAuthorityMisuse,
		ReasonKey: "authority_misuse:aws-cli/s3-remove-bucket",
		Cited: []gate.Fact{{
			Gate: api.GateAuthorityMisuse, Key: "detection.rule", Value: "x", Source: "pack",
		}},
	}
	key := "authority_misuse:aws-cli/s3-remove-bucket"
	if !hitl.DecisionFullyQuieted(decision, nil, "digest", func(k string) bool { return k == key }) {
		t.Fatal("expected quieted")
	}
	if hitl.DecisionFullyQuieted(decision, nil, "digest", func(string) bool { return false }) {
		t.Fatal("expected not quieted")
	}
}

func TestOrdinaryCardHasNoOptionNote(t *testing.T) {
	t.Parallel()
	decision := &gate.Decision{
		Primary:   api.GateUnobservedChannel,
		ReasonKey: "unobserved_channel:direct_ip",
		Cited:     []gate.Fact{{Gate: api.GateUnobservedChannel, Key: "k", Value: "v", Source: "t"}},
	}
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Scope: hitl.ActionScope{
SessionID: "chat-1",
RootSessionID: "chat-1",
ProjectID: "proj-1",
ProjectDir: "/tmp/proj",
},
Presentation: hitl.ActionPresentation{
Command: "curl https://example.com",
},
}
	plan, err := hitl.CompileCheckpointApprovalPlan(hitl.CheckpointRequest{
		SessionID: "chat-1", Kind: api.CheckpointKindToolApproval,
		ProposedAction: &action, Decision: decision,
	})
	testutil.FailErr(t, "compile plan", err)
	if note := plan.Presentation.OptionNote; note != "" {
		t.Fatalf("ordinary card carries an option note: %q", note)
	}
}

func TestGateReuseCeilings(t *testing.T) {
	t.Parallel()
	for _, g := range gate.All() {
		scope := gate.ReuseFor(g).Scope
		if g == api.GateCapabilityWidening || g == api.GateAgentPolicyChange {
			if scope != gate.ScopeChat {
				t.Errorf("%s reuse ceiling %q, want task", g, scope)
			}
			continue
		}
		if !scope.Durable() {
			t.Errorf("%s reuse ceiling %q is not durable", g, scope)
		}
	}
}
