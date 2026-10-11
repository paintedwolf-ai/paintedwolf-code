package hitl_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// secretCard compiles a secret plan the way the outbound screen does.
func secretCard(t *testing.T, screen *hitl.SecretScreen, offers ...hitl.ApprovalGrantOffer) *hitl.ApprovalPlan {
	t.Helper()
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Presentation: hitl.ActionPresentation{
Command: "curl https://api.example.com",
},
Scope: hitl.ActionScope{
SessionID: "chat-1",
RootSessionID: "chat-1",
ProjectID: "proj",
ProjectDir: "/tmp/proj",
},
}
	decision := &gate.Decision{
		Primary: api.GateSecretOutbound, Posture: gate.PostureBalanced,
		Cited: []gate.Fact{{
			Gate: api.GateSecretOutbound, Key: "secret.rule",
			Value: screen.RuleTitle, Source: "payload_lens",
		}},
	}
	plan, err := hitl.CompileCheckpointApprovalPlan(hitl.CheckpointRequest{
		ProposedAction: &action, Decision: decision, SecretScreen: screen, GrantOffers: offers,
		Title: "Fixture",
	})
	testutil.FailErr(t, "CompileCheckpointApprovalPlan", err)
	return plan
}

func modelScreen() *hitl.SecretScreen {
	return &hitl.SecretScreen{
		Surface: string(secretmatch.SurfaceModel), SurfaceLabel: "model request",
		CanRedact: true, CanTrack: true,
		DestinationID: "fireworks-1", DestinationLabel: "Fireworks 1",
		DestinationKind: secretmatch.DestinationModelProvider,
		RuleID:          "gitleaks:aws-access-token", RuleTitle: "AWS access key ID",
		GenericShape: "a1b2 (4 characters)", Occurrences: 1,
		SourceKind: "tool_argument", SourcePath: "prompt", OriginKind: secretmatch.OriginField,
	}
}

func httpManagedScreen() *hitl.SecretScreen {
	return &hitl.SecretScreen{
		Surface: string(secretmatch.SurfaceHTTPRequest), SurfaceLabel: "HTTP request",
		CanRedact: true, RedactionBreaks: true, Managed: true,
		DestinationID: "http://localhost:3000", DestinationLabel: "http://localhost:3000",
		DestinationKind: secretmatch.DestinationService,
		RuleID:          secretmatch.ManagedRuleID, RuleTitle: secretmatch.ManagedRuleTitle,
		GenericShape: "Protected value", Occurrences: 1,
		SourceKind: "tool_argument", SourcePath: "body", OriginKind: secretmatch.OriginField,
	}
}

// Resolving a value the host already protects: protecting it again is a no-op
// and redacting it fails the call, so the face releases it.
func TestManagedSecretFacesTheSend(t *testing.T) {
	t.Parallel()
	plan := secretCard(t, httpManagedScreen())
	if plan.RecommendedOptionID != "send_unchanged" {
		t.Fatalf("managed face = %q, want the send", plan.RecommendedOptionID)
	}
	face, _ := plan.Option(plan.RecommendedOptionID)
	if face.Title != hitl.TitleSend {
		t.Fatalf("managed send title = %q, want %q", face.Title, hitl.TitleSend)
	}
}

// An unmanaged detection bound for the model faces Protect: the only choice
// that keeps the request working and the value off the wire.
func TestUnmanagedModelSecretFacesProtect(t *testing.T) {
	t.Parallel()
	plan := secretCard(t, modelScreen())
	if plan.RecommendedOptionID != "track_and_replace" {
		t.Fatalf("model face = %q, want Protect", plan.RecommendedOptionID)
	}
}

// Redaction is never the face, on any secret card, in any posture.
func TestRedactionIsNeverTheSecretFace(t *testing.T) {
	t.Parallel()
	for _, screen := range []*hitl.SecretScreen{modelScreen(), httpManagedScreen()} {
		plan := secretCard(t, screen)
		face, ok := plan.Option(plan.RecommendedOptionID)
		if !ok {
			t.Fatalf("face %q is not an option", plan.RecommendedOptionID)
		}
		if face.Kind == hitl.ApprovalOptionRedacted {
			t.Fatalf("%s faced redaction", screen.SurfaceLabel)
		}
		if face.Disabled {
			t.Fatalf("%s faced a disabled option", screen.SurfaceLabel)
		}
	}
}

// Where the value goes is on every secret card, with who receives it.
func TestSecretCardAlwaysNamesItsReceiver(t *testing.T) {
	t.Parallel()
	cases := []struct {
		screen *hitl.SecretScreen
		want   api.ApprovalSecretDestinationKind
	}{
		{modelScreen(), api.ApprovalSecretDestinationModelProvider},
		{httpManagedScreen(), api.ApprovalSecretDestinationService},
	}
	for _, tc := range cases {
		loc := secretCard(t, tc.screen).Presentation.Location
		if loc == nil {
			t.Fatalf("%s card has no destination line", tc.screen.SurfaceLabel)
		}
		if loc.DestinationKind != tc.want {
			t.Fatalf("%s destination kind = %q, want %q", tc.screen.SurfaceLabel, loc.DestinationKind, tc.want)
		}
		if loc.Destination == "" {
			t.Fatalf("%s destination is blank", tc.screen.SurfaceLabel)
		}
	}
}

// A seam where the value authenticates the call says so on the redaction row,
// rather than presenting it as a softer send.
func TestRedactionRowStatesThatItBreaksTheRequest(t *testing.T) {
	t.Parallel()
	plan := secretCard(t, httpManagedScreen())
	row, ok := plan.Option("send_redacted")
	if !ok {
		t.Fatal("redaction row missing")
	}
	if row.Coverage != hitl.CoverageRedactionBreaks {
		t.Fatalf("redaction coverage = %q, want the breaking note", row.Coverage)
	}
	if row.Group != hitl.GroupRedaction {
		t.Fatalf("redaction group = %q, want %q", row.Group, hitl.GroupRedaction)
	}
}

// A model request still completes without the value, so its redaction row is
// the ordinary one rather than the breaking note.
func TestModelRedactionRowDoesNotClaimToBreakTheRequest(t *testing.T) {
	t.Parallel()
	row, ok := secretCard(t, modelScreen()).Option("send_redacted")
	if !ok {
		t.Fatal("redaction row missing")
	}
	if row.Coverage != hitl.CoverageEveryCredentialReplaced {
		t.Fatalf("model redaction coverage = %q", row.Coverage)
	}
}
