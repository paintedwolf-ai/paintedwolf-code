package hitl_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompilePackagePlanShowsResolvedIdentityAndRemotePackageBoundary(t *testing.T) {
	age := 7
	published := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	execution := &packageexec.Execution{
		Manager: "npm", Operation: packageexec.OperationRemoteExecute,
		ApprovedReadPaths: []string{"/fixture/cacert.pem"},
		Packages: []packageexec.Package{{
			System: "NPM", Name: "create-vite", RequestedVersion: "latest",
			ResolvedVersion: "6.1.0", PublishedAt: &published, AgeDays: &age,
			Status: packageexec.IdentityResolved, Registry: "https://registry.npmjs.org",
			SourceRepository: "https://github.com/vitejs/vite", VerifiedAttestation: true,
		}},
	}
	facts := gate.Facts{
		Stage: gate.StagePreSpawn,
		Ran: gate.ProducerContainment | gate.ProducerDetection | gate.ProducerPayload |
			gate.ProducerDestination | gate.ProducerFilePath | gate.ProducerConsent |
			gate.ProducerLease | gate.ProducerRule | gate.ProducerExposure |
			gate.ProducerIngestion | gate.ProducerApprovalRequest | gate.ProducerPackageExecution,
		Containment: gate.Containment{SpawnsProcess: true, FSJailed: true, Egress: gate.EgressProxy},
		PackageExecution: &gate.PackageExecution{
			Manager: "npm", Operation: string(packageexec.OperationRemoteExecute),
			Packages: []gate.PackageIdentity{{
				System: "NPM", Name: "create-vite", Version: "6.1.0", AgeDays: &age,
				Status: "resolved", SourceRepository: "https://github.com/vitejs/vite",
				VerifiedAttestation: true,
			}},
		},
	}
	if v, _ := gate.Evaluate(facts, gate.PostureLight); v != gate.Silent {
		t.Fatalf("light posture must not gate resolved package: %v", v)
	}
	verdict, decision := gate.Evaluate(facts, gate.PostureBalanced)
	if verdict != gate.Ask || decision.Primary != api.GateRemotePackageExecutionKnown {
		t.Fatalf("balanced posture must ask GateRemotePackageExecutionKnown: %v, %+v", verdict, decision)
	}
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "npx create-vite@latest demo"},
},
Presentation: hitl.ActionPresentation{
Command: "npx create-vite@latest demo",
},
Execution: hitl.ActionExecution{
PackageExecution: execution,
},
}
	plan, err := hitl.CompileCheckpointApprovalPlan(hitl.CheckpointRequest{
		ProposedAction: &action, Decision: decision, Title: "Run downloaded package code",
	})
	testutil.FailErr(t, "compile package approval plan", err)
	if plan.Subject.Kind != hitl.ApprovalSubjectPackageSet || len(plan.Subject.Targets) != 1 {
		t.Fatalf("package subject = %#v", plan.Subject)
	}
	target := plan.Subject.Targets[0]
	if target.Label != "create-vite@6.1.0" {
		t.Fatalf("package label = %q", target.Label)
	}
	if got, ok := target.Details["age_days"].(float64); !ok || got != 7 {
		t.Fatalf("age_days = %#v (%T)", target.Details["age_days"], target.Details["age_days"])
	}
	for _, key := range []string{"verified_attestation", "ambient_credentials_removed", "protected_reads_denied"} {
		if got, ok := target.Details[key].(bool); !ok || !got {
			t.Fatalf("%s = %#v (%T)", key, target.Details[key], target.Details[key])
		}
	}
	if got := target.Details["network_scope"]; got != confine.RemotePackageNetworkScopeRegistryOnly {
		t.Fatalf("network_scope = %#v", got)
	}
	if paths, ok := target.Details["approved_read_paths"].([]any); !ok || len(paths) != 1 || paths[0] != "/fixture/cacert.pem" {
		t.Fatalf("approved_read_paths = %#v", target.Details["approved_read_paths"])
	}
	if plan.Presentation.Gate != api.GateRemotePackageExecution && plan.Presentation.Gate != api.GateRemotePackageExecutionKnown {
		t.Fatalf("gate = %q", plan.Presentation.Gate)
	}
}

func TestCompileApprovalPlanRedactsCredentialArguments(t *testing.T) {
	secret := "cargo-token-that-must-not-be-persisted"
	command := "cargo publish --token " + secret
	_, decision := gate.Evaluate(gate.Facts{
		Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest,
		ApprovalRequest: &gate.ApprovalRequest{Count: 1},
	}, gate.DefaultPosture)
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": command, "token": secret},
},
Presentation: hitl.ActionPresentation{
Command: command,
},
}
	plan, err := hitl.CompileCheckpointApprovalPlan(hitl.CheckpointRequest{
		ProposedAction: &action, Decision: decision,
	})
	testutil.FailErr(t, "compile redacted approval plan", err)
	raw := strings.Join([]string{
		plan.ActionDigest,
		plan.Presentation.Command,
		plan.Subject.Targets[0].Label,
		fmt.Sprint(plan.Subject.Targets[0].Details),
	}, "\n")
	if strings.Contains(raw, secret) || !strings.Contains(raw, "[REDACTED]") {
		t.Fatalf("approval plan did not redact credential:\n%s", raw)
	}
}

func approvalPlanPresentation() (hitl.ApprovalPresentation, []api.ApprovalGate) {
	_, decision := gate.Evaluate(gate.Facts{
		Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest,
		ApprovalRequest: &gate.ApprovalRequest{Count: 1},
	}, gate.DefaultPosture)
	primary, cited, reasons := hitl.PresentDecision(decision)
	return hitl.ApprovalPresentation{
		Action: "Run command", Impact: "Run pwd.", Gate: primary, Cited: cited,
	}, reasons
}

func TestApprovalPlanRejectsSubjectAtWrongStage(t *testing.T) {
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "pwd"},
},
}
	presentation, reasons := approvalPlanPresentation()
	_, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSend, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectAction, Title: "Run command",
		Targets: []hitl.ApprovalTarget{{Kind: "action", Label: "pwd"}},
	}, presentation, reasons, []hitl.ApprovalOption{
		hitl.CurrentActionOption(),
	}, hitl.FaceContext{})
	if err == nil {
		t.Fatal("action subject at pre_send unexpectedly validated")
	}
}

func TestApprovalPlanRejectsProjectGrantWithoutProjectID(t *testing.T) {
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "pwd"},
},
}
	presentation, reasons := approvalPlanPresentation()
	grant := hitl.ApprovalGrant{
		ID: "grant_no_project", Scope: hitl.ApprovalGrantScopeProject,
		Predicate: hitl.ApprovalGrantPredicate{Category: "host", Pattern: "example.test"},
		Title:     hitl.TitleAllowForThisProject,
	}
	_, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectAction, Title: "Run command",
		Targets: []hitl.ApprovalTarget{{Kind: "action", Label: "pwd"}},
	}, presentation, reasons, []hitl.ApprovalOption{{
		ID: "day", Kind: hitl.ApprovalOptionLease, Scope: hitl.ApprovalGrantScopeProject,
		Title: hitl.TitleAllowFor1Day, Coverage: "pwd", ExpiresWhen: hitl.ExpiresIn1DayOrRevoked,
		ReaskWhen: "action changes", Rung: hitl.ApprovalRungDay, DecisionAction: hitl.ApprovalOptionApprove,
		Authority: []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityGenericGrant, Grant: &grant}},
	}}, hitl.FaceContext{})
	if err == nil {
		t.Fatal("project grant without project_id unexpectedly validated")
	}
	if !strings.Contains(err.Error(), "project grant requires project_id") {
		t.Fatalf("error = %v, want project grant requires project_id", err)
	}
}

func TestApprovalPlanRejectsLeaseWithoutReusableAuthority(t *testing.T) {
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "pwd"},
},
}
	presentation, reasons := approvalPlanPresentation()
	_, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectAction, Title: "Run command",
		Targets: []hitl.ApprovalTarget{{Kind: "action", Label: "pwd"}},
	}, presentation, reasons, []hitl.ApprovalOption{{
		ID: "invalid_lease", Kind: hitl.ApprovalOptionLease, Scope: hitl.ApprovalGrantScopeChat,
		Title: "Allow for task", Coverage: "pwd", ExpiresWhen: "task ends", ReaskWhen: "action changes",
		Rung: hitl.ApprovalRungChat, DecisionAction: hitl.ApprovalOptionApprove,
		Authority: []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityCurrentAction}},
	}}, hitl.FaceContext{})
	if err == nil {
		t.Fatal("lease without reusable authority unexpectedly validated")
	}
}

func TestApprovalPlanIdentityCoversEveryField(t *testing.T) {
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "pwd"},
},
}
	presentation, reasons := approvalPlanPresentation()
	plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectAction, Title: "Run command",
		Targets: []hitl.ApprovalTarget{{Kind: "action", Label: "pwd"}},
	}, presentation, reasons, []hitl.ApprovalOption{
		hitl.CurrentActionOption(),
	}, hitl.FaceContext{})
	testutil.FailErr(t, "NewApprovalPlan", err)
	plan.Options[0].Coverage = "a wider action"
	if err := plan.Validate(); err == nil {
		t.Fatal("mutated plan unexpectedly retained a valid identity")
	}
}

func TestApprovalPlanRejectsMissingGateProvenance(t *testing.T) {
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "pwd"},
},
}
	_, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectAction, Title: "Run command",
		Targets: []hitl.ApprovalTarget{{Kind: "action", Label: "pwd"}},
	}, hitl.ApprovalPresentation{Action: "Run command", Impact: "Run pwd."}, nil, []hitl.ApprovalOption{
		hitl.CurrentActionOption(),
	}, hitl.FaceContext{})
	if err == nil {
		t.Fatal("new approval plan without gate provenance unexpectedly validated")
	}
}

func TestCompileCheckpointApprovalPlanCarriesApprovalRuleProvenance(t *testing.T) {
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "git push origin main"},
},
Presentation: hitl.ActionPresentation{
Command: "git push origin main",
},
}
	_, decision := gate.Evaluate(gate.Facts{
		Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest,
		ApprovalRequest: &gate.ApprovalRequest{Count: 1},
	}, gate.DefaultPosture)
	plan, err := hitl.CompileCheckpointApprovalPlan(hitl.CheckpointRequest{
		ProposedAction: &action, Decision: decision, Title: "Approve command",
		ApprovalMatches: []hitl.ApprovalRuleMatch{{
			Category: "command", Pattern: "git push*", Effect: "ask",
			UnitID: "approvals/rules/release", PackID: "acme/policy", Scope: "project",
		}},
	})
	testutil.FailErr(t, "CompileCheckpointApprovalPlan", err)
	if len(plan.Presentation.ApprovalRules) != 1 || plan.Presentation.ApprovalRules[0].PackID != "acme/policy" {
		t.Fatalf("approval rules = %#v", plan.Presentation.ApprovalRules)
	}
}

func TestCompileSecretPlanAuthorsFileLocation(t *testing.T) {
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "model_request",
},
}
	_, decision := gate.Evaluate(gate.Facts{
		Stage: gate.StagePreSend, Ran: gate.ProducerPayload,
		Payload: &gate.SecretHit{
			Surface: "model_request", RuleID: "gitleaks:github-pat",
			RuleTitle: "GitHub Personal Access Token", Occurrences: 1,
			SourceKind: "tool_result", SourceTool: "read",
		},
	}, gate.DefaultPosture)
	plan, err := hitl.CompileCheckpointApprovalPlan(hitl.CheckpointRequest{
		ProposedAction: &action, Decision: decision,
		SecretScreen: &hitl.SecretScreen{
			Surface: "model_request", SurfaceLabel: "model request", CanRedact: true,
			DestinationLabel: "Fireworks",
			RuleID:           "gitleaks:github-pat",
			RuleTitle:        "GitHub Personal Access Token",
			GenericShape:     "abc-a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3 (40 characters)",
			Occurrences:      1, SourceKind: "tool_result",
			SourcePath: ".env.local", SourceLine: 7, OriginKind: "file",
			SourceToolCallID: "call_read_1",
		},
	})
	testutil.FailErr(t, "CompileCheckpointApprovalPlan", err)
	loc := plan.Presentation.Location
	if loc == nil || loc.Origin != ".env.local:7" || loc.Destination != "Fireworks" ||
		loc.OriginKind != api.ApprovalSecretOriginFile || loc.RevealToolCallID != "call_read_1" {
		t.Fatalf("location = %+v", loc)
	}
	if _, ok := plan.Subject.Targets[0].Details["source_path"]; ok {
		t.Fatalf("source path leaked onto the target: %+v", plan.Subject.Targets[0].Details)
	}
}

func TestCompileSecretPlanNamesTheReceiverForACommand(t *testing.T) {
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Presentation: hitl.ActionPresentation{
Command: "curl https://api.github.com/user",
},
}
	_, decision := gate.Evaluate(gate.Facts{
		Stage: gate.StagePreSend, Ran: gate.ProducerPayload,
		Payload: &gate.SecretHit{
			Surface: "command", RuleID: "gitleaks:github-pat",
			RuleTitle: "GitHub Personal Access Token", Occurrences: 1,
			SourceKind: "tool_argument", SourceTool: "command",
		},
	}, gate.DefaultPosture)
	plan, err := hitl.CompileCheckpointApprovalPlan(hitl.CheckpointRequest{
		ProposedAction: &action, Decision: decision,
		SecretScreen: &hitl.SecretScreen{
			Surface: "command", SurfaceLabel: "command",
			RedactionNote:    "Redaction is not offered here: no reviewed rewrite exists.",
			DestinationLabel: "any host this command dials",
			RuleID:           "gitleaks:github-pat",
			RuleTitle:        "GitHub Personal Access Token",
			GenericShape:     "abc-a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3 (40 characters)",
			Occurrences:      1, SourceKind: "tool_argument",
			SourcePath: "arguments", OriginKind: "field",
			CommandLine: "curl -H Authorization: [REDACTED] https://api.github.com/user",
			ToolCallID:  "call_cmd",
		},
	})
	testutil.FailErr(t, "CompileCheckpointApprovalPlan", err)
	if plan.Presentation.Location == nil ||
		plan.Presentation.Location.Destination != "any host this command dials" {
		t.Fatalf("location = %+v, want the command's receiver named", plan.Presentation.Location)
	}
	if plan.Presentation.Command != "curl -H Authorization: [REDACTED] https://api.github.com/user" {
		t.Fatalf("command = %q", plan.Presentation.Command)
	}
	// Redaction cannot be applied to an argv, so it is never this card's face.
	if plan.RecommendedOptionID != "send_unchanged" {
		t.Fatalf("face = %q, want send_unchanged", plan.RecommendedOptionID)
	}
	redacted, ok := plan.Option("send_redacted")
	if !ok || !redacted.Disabled {
		t.Fatalf("send_redacted = %+v", redacted)
	}
	if _, ok := plan.Option("send_unchanged"); !ok {
		t.Fatal("send_unchanged missing")
	}
}
