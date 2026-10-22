package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestWriteInterfacesShareNativeOwnerAndReview(t *testing.T) {
	for _, structured := range []bool{true, false} {
		t.Run(map[bool]string{true: "structured", false: "typed-command"}[structured], func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "AGENTS.md")
			if err := os.WriteFile(path, []byte("before\n"), 0600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			tctx := nativefixture.Context(dir)
			tctx.Identity.ProjectID = "project"
			refusal := errors.New("fixture review declined")
			reviews := 0
			tctx.Files.FileChangeReview = func(context.Context, []tools.FileChange) error { reviews++; return refusal }
			write := &WriteTool{Boundary: nativefixture.Boundary(t)}
			registry := tools.NewDefaultRegistry()
			if err := registry.Register("write", write.Run); err != nil {
				t.Fatalf("register: %v", err)
			}
			definition, _ := registry.Definition("write")
			if definition.Operation.Facts().Contract.Owner != "filesystem" {
				t.Fatal("write changed owner")
			}
			var err error
			if structured {
				_, err = registry.Run(t.Context(), "write", map[string]any{"path": "AGENTS.md", "content": "after\n"}, tctx)
			} else {
				binding := commandprogram.Binding[WriteInput, WriteResult]{Definition: definition.Operation, InvokeOwner: func(ctx context.Context, _ toolcontract.OperationInvocation, input WriteInput) (WriteResult, error) {
					return write.Apply(ctx, input, tctx)
				}}
				invocation := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "node", ExecutionID: "child"}, ActorID: "actor", SessionID: "test-session", Scope: toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "project"}, SourceUnitID: "source", Span: toolcontract.SourceSpan{Line: 1, Column: 1}, CatalogDigest: toolcontract.LanguageCatalogDigest()}
				_, err = commandprogram.Invoke(t.Context(), commandprogram.Offer(definition.Operation), binding, invocation, WriteInput{Path: "AGENTS.md", Content: "after\n"})
			}
			if !errors.Is(err, refusal) || reviews != 1 {
				t.Fatalf("review=%d err=%v", reviews, err)
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil || string(raw) != "before\n" {
				t.Fatalf("refused mutation changed bytes: %q %v", raw, readErr)
			}
		})
	}
}

func TestExecutionPinnedWriteNeverUsesConversationalRetry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("person revision\n"), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	write := &WriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := write.Apply(t.Context(), WriteInput{Path: "a.txt", Content: "stale command\n", Admission: WriteExecutionPinned, ExecutionPin: toolcontract.SourcePin{Revision: "older"}}, nativefixture.Context(dir))
	rejection := toolrejection.AsToolReject(err)
	if rejection == nil || rejection.Code != "COMMAND_NATIVE_FORM_UNSUPPORTED" {
		t.Fatalf("execution-pin unavailable outcome: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "person revision\n" {
		t.Fatalf("execution pin refreshed/published: %q %v", raw, err)
	}
}
