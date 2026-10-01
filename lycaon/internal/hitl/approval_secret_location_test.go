package hitl

import (
	"github.com/lycaon/lycaon/internal/secretmatch"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompileSecretLocationFileOrigin(t *testing.T) {
	loc := compileSecretLocation(&SecretScreen{
		DestinationLabel: "Fireworks",
		SourceKind:       "tool_result",
		SourcePath:       ".env.local",
		SourceLine:       7,
		OriginKind:       "file",
		SourceToolCallID: "call_read_1",
	})
	if loc == nil {
		t.Fatal("expected location")
	}
	if loc.Origin != ".env.local:7" || loc.Destination != "Fireworks" ||
		loc.OriginKind != api.ApprovalSecretOriginFile || loc.Path != ".env.local" ||
		loc.Line != 7 || loc.RevealToolCallID != "call_read_1" {
		t.Fatalf("location = %+v", loc)
	}
	if got := secretLocationLine(loc); got != ".env.local:7 → Fireworks" {
		t.Fatalf("line = %q", got)
	}
}

func TestCompileSecretLocationFieldOrigin(t *testing.T) {
	loc := compileSecretLocation(&SecretScreen{
		DestinationLabel: "web search providers",
		SourceKind:       "tool_argument",
		SourcePath:       "query",
		OriginKind:       "field",
		ToolCallID:       "call_search_1",
	})
	if loc == nil || loc.Origin != "in query" || loc.OriginKind != api.ApprovalSecretOriginField ||
		loc.RevealToolCallID != "call_search_1" || loc.Path != "" {
		t.Fatalf("location = %+v", loc)
	}
}

// A command card still names where the value goes. The argv block shows the
// value's place in the command; it does not say who receives it, and that is
// the fact the person is deciding on.
func TestCompileSecretLocationNamesTheReceiverForACommand(t *testing.T) {
	loc := compileSecretLocation(&SecretScreen{
		DestinationLabel: "any host this command dials",
		DestinationKind:  secretmatch.DestinationProcess,
		SourceKind:       "tool_argument",
		SourcePath:       "arguments",
		OriginKind:       "field",
		CommandLine:      "curl -H Authorization: [REDACTED] https://api.github.com/user",
		ToolCallID:       "call_cmd",
	})
	if loc == nil {
		t.Fatal("command card dropped its destination line")
	}
	if loc.Destination != "any host this command dials" ||
		loc.DestinationKind != api.ApprovalSecretDestinationProcess {
		t.Fatalf("location = %+v, want the command's own receiver", loc)
	}
}

// With no addressable destination the card still names the receiver it has.
func TestCompileSecretLocationFallsBackToTheCommandProcess(t *testing.T) {
	loc := compileSecretLocation(&SecretScreen{
		DestinationKind: secretmatch.DestinationProcess,
		SourceKind:      "tool_argument",
		SourcePath:      "arguments",
		OriginKind:      "field",
		CommandLine:     "gitea admin user create --password [REDACTED]",
		ToolCallID:      "call_cmd",
	})
	if loc == nil || loc.Destination != destinationCommandProcess {
		t.Fatalf("location = %+v, want the command process face", loc)
	}
}

func TestCompileSecretLocationArgumentsWithoutArgv(t *testing.T) {
	loc := compileSecretLocation(&SecretScreen{
		DestinationLabel: "any host this command dials",
		SourceKind:       "tool_argument",
		SourcePath:       "arguments",
		OriginKind:       "field",
		ToolCallID:       "call_cmd",
	})
	if loc == nil || loc.Origin != "in arguments" {
		t.Fatalf("location = %+v", loc)
	}
}

func TestCompileSecretLocationRequiresOriginKind(t *testing.T) {
	loc := compileSecretLocation(&SecretScreen{
		DestinationLabel: "Fireworks",
		SourceKind:       "tool_result",
		SourcePath:       ".env.local",
		SourceLine:       7,
	})
	if loc != nil {
		t.Fatalf("location = %+v, want omitted", loc)
	}
}

func TestCompileSecretLocationOmitsWithoutDestination(t *testing.T) {
	loc := compileSecretLocation(&SecretScreen{
		SourceKind: "tool_result",
		SourcePath: ".env.local",
		OriginKind: "file",
	})
	if loc != nil {
		t.Fatalf("location = %+v, want omitted", loc)
	}
}

func TestValidateSecretLocationRejectsNonSecretSubject(t *testing.T) {
	p := ApprovalPlan{
		Subject: ApprovalSubject{Kind: ApprovalSubjectAction},
		Presentation: ApprovalPresentation{
			Location: &api.ApprovalSecretLocation{
				Origin: "in query", Destination: "Fireworks", OriginKind: api.ApprovalSecretOriginField,
			},
		},
	}
	if err := p.validateSecretLocation(); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateSecretLocationRejectsFileWithoutPath(t *testing.T) {
	p := ApprovalPlan{
		Subject: ApprovalSubject{Kind: ApprovalSubjectSecret},
		Presentation: ApprovalPresentation{
			Location: &api.ApprovalSecretLocation{
				Origin: ".env.local", Destination: "Fireworks", OriginKind: api.ApprovalSecretOriginFile,
			},
		},
	}
	if err := p.validateSecretLocation(); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateSecretLocationRejectsFieldWithPath(t *testing.T) {
	p := ApprovalPlan{
		Subject: ApprovalSubject{Kind: ApprovalSubjectSecret},
		Presentation: ApprovalPresentation{
			Location: &api.ApprovalSecretLocation{
				Origin: "in query", Destination: "Fireworks", OriginKind: api.ApprovalSecretOriginField,
				Path: ".env.local",
			},
		},
	}
	if err := p.validateSecretLocation(); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateSecretLocationAllowsFileDestination(t *testing.T) {
	p := ApprovalPlan{
		Subject: ApprovalSubject{Kind: ApprovalSubjectSecret},
		Presentation: ApprovalPresentation{
			Location: &api.ApprovalSecretLocation{
				Origin:          ".env.local:1",
				Destination:     "/tmp/out.txt",
				OriginKind:      api.ApprovalSecretOriginFile,
				Path:            ".env.local",
				DestinationKind: api.ApprovalSecretDestinationFile,
			},
		},
	}
	if err := p.validateSecretLocation(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

