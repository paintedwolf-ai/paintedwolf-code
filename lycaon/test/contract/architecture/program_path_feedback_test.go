package contract

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestProgramPathFeedbackPreservesTypedRefusalAndCatalogHelp(t *testing.T) {
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load path feedback", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	formatter := guidance.NewStaticRejectFormatter(cfg)
	root := t.TempDir()
	tc := tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "fixture-root", Label: "fixture", Path: root, IsPrimary: true}}, ActiveRootID: "fixture-root"}, Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID}}
	paths, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	contractcheck.FailErr(t, "bind path invocation", err)
	invocation := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, ActorID: "fixture-actor", SessionID: "fixture-session", Scope: toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "fixture-project", RootID: "fixture-root"}, Identity: toolcontract.ExecutionIdentity{ProgramID: "fixture-program", NodeID: "fixture-node", ExecutionID: "fixture-execution"}, SourceUnitID: "fixture-source", Span: toolcontract.SourceSpan{StartByte: 7, EndByte: 20, Line: 2, Column: 3}, CatalogDigest: toolcontract.LanguageCatalogDigest()}
	contractcheck.FailErr(t, "validate fixture host invocation", invocation.Validate())
	for _, fixture := range []struct {
		command    string
		args       []string
		code, want string
	}{
		{"basename", []string{"--unsupported"}, "COMMAND_OPTION_UNSUPPORTED", "basename [--] PATH [SUFFIX]"},
		{"native_path", []string{"--", "/dev/null"}, "COMMAND_OPERAND_INVALID", "null device"},
		{"native_path", []string{"--", strings.Repeat("x", 131073)}, "COMMAND_LIMIT_EXCEEDED", "argument_bytes"},
	} {
		var stdout bytes.Buffer
		result, err := paths.Invoke(t.Context(), commandprogram.OperationRequest{Invocation: invocation, OperationID: fixture.command, Arguments: fixture.args}, commandprogram.Streams{Output: &stdout})
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != fixture.code {
			t.Fatalf("expected typed %s refusal, got %v", fixture.code, err)
		}
		contractcheck.FailErr(t, "validate refused producer", result.Validate())
		if result.PublicationEligible || result.Complete || result.Terminal != toolcontract.ProducerRefused || stdout.Len() != 0 {
			t.Fatalf("refusal published or lost terminal: %+v", result)
		}
		if reject.Data["source_start_byte"] != 7 || reject.Data["source_end_byte"] != 20 || reject.Data["source_line"] != 2 || reject.Data["source_column"] != 3 {
			t.Fatalf("source coordinates lost: %+v", reject.Data)
		}
		if fixture.code == "COMMAND_LIMIT_EXCEEDED" && (reject.Data["resource_unit"] != "bytes" || reject.Data["maximum"] != int64(131072) || reject.Data["observed"] != int64(131073)) {
			t.Fatalf("typed resource bound lost: %+v", reject.Data)
		}
		reject.Data["tool"] = "command"
		rendered, err := formatter.Format(reject.Code, reject.Data)
		contractcheck.FailErr(t, "render actual path refusal", err)
		if !strings.Contains(rendered, "Code: "+fixture.code) || !strings.Contains(rendered, fixture.want) {
			t.Fatalf("missing precise path guidance: %s", rendered)
		}
	}
}
