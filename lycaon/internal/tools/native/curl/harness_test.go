package curl

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type pinLog struct {
	mu   sync.Mutex
	pins map[string][]toolcontract.ExecutionRevision
}

func (l *pinLog) AppendRevisionPin(_ context.Context, execution string, pin toolcontract.ExecutionRevision) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pins == nil {
		l.pins = map[string][]toolcontract.ExecutionRevision{}
	}
	l.pins[execution] = append(l.pins[execution], pin)
	return nil
}

type harness struct {
	t       *testing.T
	root    string
	tc      tools.ToolContext
	owners  *Owners
	pins    *pinLog
	program mutation.Program
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve root", err)
	allowed := map[string]bool{}
	for _, name := range []string{"read", "write", "edit", "delete", "copy", "move", "mkdir", "chmod", "chown", "extract_archive", "http_request"} {
		allowed[name] = true
	}
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: toolprofiles.DefaultToolProfileID, Tools: allowed}})
	tc := nativefixture.Context(root)
	tc.Identity.ProjectID, tc.Identity.SessionID = "p1", "curl-session-"+uuid.NewString()
	tc.Local.LoopbackConnectGranted = true
	pins := &pinLog{}
	mutations, err := mutation.NewOwners(native.ExecutionRevisions{Boundary: boundary}, pins, mutation.Operations{
		Write: &native.WriteTool{Boundary: boundary}, Delete: &native.DeleteTool{Boundary: boundary}, Move: &native.MoveTool{Boundary: boundary},
		Copy: &native.CopyTool{Boundary: boundary}, Mkdir: &native.MkdirTool{Boundary: boundary}, Chmod: &native.ChmodTool{Boundary: boundary},
		Chown: &native.ChownTool{Boundary: boundary}, Times: &native.TimesTool{Boundary: boundary}, Extract: &native.ExtractArchiveTool{Boundary: boundary}})
	testutil.FailErr(t, "mutation owners", err)
	owners, err := NewOwners(httpaction.Deps{Boundary: boundary}, mutations)
	testutil.FailErr(t, "curl owners", err)
	paths, err := projectpaths.NewProgramPaths(t.Context(), boundary, tc, "")
	testutil.FailErr(t, "program paths", err)
	return &harness{t: t, root: root, tc: tc, owners: owners, pins: pins, program: mutation.Program{Paths: paths}}
}

type run struct {
	mode           string
	stdout, stderr string
	result         toolcontract.ProducerResult
	err            error
	execution      string
	transfer       Transfer
	review         commandprogram.ArgvReview
	evidence       toolcontract.OperationEvidence
}

func (h *harness) curl(ctx context.Context, words ...string) run {
	h.t.Helper()
	return h.curlStdin(ctx, "", words...)
}

// curlStdin runs curl with stdin connected to input.
func (h *harness) curlStdin(ctx context.Context, input string, words ...string) run {
	h.t.Helper()
	inv := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "n1", ExecutionID: uuid.NewString()},
		ActorID: "actor", SessionID: h.tc.Identity.SessionID, Scope: toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "p1", RootID: "r1"},
		SourceUnitID: "program", Span: toolcontract.SourceSpan{Line: 1, Column: 1}, CatalogDigest: toolcontract.LanguageCatalogDigest()}
	argv := commandprogram.Fields{{Value: "curl", Span: inv.Span}}
	for _, word := range words {
		argv = append(argv, commandprogram.Field{Value: word, Span: inv.Span})
	}
	statement := commandsurface.Statement{SyntaxLocation: commandsurface.SyntaxLocation{ID: commandsurface.NodeID(inv.Identity.NodeID), Span: inv.Span}}
	prepared, err := h.owners.Prepare(h.program, commandprogram.OperationRequest{Invocation: inv, OperationID: "curl", Statement: statement, Argv: argv})
	if err != nil {
		return run{err: err, execution: inv.Identity.ExecutionID}
	}
	var stdout, stderr bytes.Buffer
	result, err := prepared.InvokeOwner(ctx, h.tc, commandprogram.Streams{Input: io.NopCloser(strings.NewReader(input)), Output: &stdout, Error: &stderr})
	if validErr := result.Validate(); validErr != nil {
		h.t.Fatalf("curl %v produced an invalid result %+v: %v", words, result, validErr)
	}
	return run{mode: prepared.Mode, stdout: stdout.String(), stderr: stderr.String(), result: result, err: err, execution: inv.Identity.ExecutionID, transfer: prepared.Transfer(),
		review: prepared.Review, evidence: result.Evidence}
}

func (h *harness) path(rel string) string { return filepath.Join(h.root, filepath.FromSlash(rel)) }

func (h *harness) read(rel string) string {
	h.t.Helper()
	data, err := os.ReadFile(h.path(rel))
	testutil.FailErr(h.t, "read "+rel, err)
	return string(data)
}

func (h *harness) write(rel, content string) {
	h.t.Helper()
	testutil.FailErr(h.t, "write "+rel, os.WriteFile(h.path(rel), []byte(content), 0o644))
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
