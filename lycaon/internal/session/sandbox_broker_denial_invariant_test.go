package session

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

// Invariant: a denied capability ask leaves no authority, leases, or cached grants across broker entry points.
const denialInvariantChat = "chat-denial-invariant"

// denialInvariantCheckpoints denies every ask and counts how many were raised.
type denialInvariantCheckpoints struct {
	*cannedWriteRootCheckpoints
	asks int
}

func (c *denialInvariantCheckpoints) RequestCheckpoint(ctx context.Context, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	c.asks++
	return c.cannedWriteRootCheckpoints.RequestCheckpoint(ctx, req)
}

// denialInvariantProvenance reports every port as served by the chat itself,
// the fact most likely to tempt a broker into recording a lease early.
type denialInvariantProvenance struct{}

func (denialInvariantProvenance) IsSessionOwned(context.Context, string, string, uint16) (bool, ProvenanceEvidence) {
	return true, ProvenanceEvidence{}
}
func (denialInvariantProvenance) HeldByOther(context.Context, string, string, uint16) bool {
	return false
}
func (denialInvariantProvenance) RecordSessionContainer(string, string, string) {}
func (denialInvariantProvenance) ForgetSession(string)                          {}

// denialInvariantStores holds every capability store a sandbox broker writes.
type denialInvariantStores struct {
	listen, loopback *approvalstate.SandboxPortGrantRuntime
	write, read      *approvalstate.SandboxPathGrantRuntime
	checkpoints      *denialInvariantCheckpoints
}

func newDenialInvariantStores() *denialInvariantStores {
	return &denialInvariantStores{
		listen:      approvalstate.NewSandboxPortGrantRuntime(),
		loopback:    approvalstate.NewSandboxPortGrantRuntime(),
		write:       approvalstate.NewSandboxPathGrantRuntime(),
		read:        approvalstate.NewSandboxPathGrantRuntime(),
		checkpoints: &denialInvariantCheckpoints{cannedWriteRootCheckpoints: &cannedWriteRootCheckpoints{approve: false}},
	}
}

// authority lists every lease or grant any store holds for the chat.
func (s *denialInvariantStores) authority() []string {
	var held []string
	for _, port := range []struct {
		name string
		rt   *approvalstate.SandboxPortGrantRuntime
	}{{"listen", s.listen}, {"loopback connect", s.loopback}} {
		if granted, ports := port.rt.SessionPorts(denialInvariantChat); granted {
			held = append(held, fmt.Sprintf("%s session port lease %v", port.name, ports))
		}
		if grants := port.rt.ListChatGrants(denialInvariantChat); len(grants) > 0 {
			held = append(held, fmt.Sprintf("%d %s chat grant(s)", len(grants), port.name))
		}
	}
	for _, path := range []struct {
		name string
		rt   *approvalstate.SandboxPathGrantRuntime
	}{{"write root", s.write}, {"read path", s.read}} {
		if roots := path.rt.SessionWriteRoots(denialInvariantChat); len(roots) > 0 {
			held = append(held, fmt.Sprintf("%s session lease %v", path.name, roots))
		}
		if grants := path.rt.ListChatGrants(denialInvariantChat); len(grants) > 0 {
			held = append(held, fmt.Sprintf("%d %s chat grant(s)", len(grants), path.name))
		}
	}
	return held
}

// userTurn is the boundary after which a denied capability may be asked again.
func (s *denialInvariantStores) userTurn() {
	s.listen.NoteUserIntentBoundary(denialInvariantChat)
	s.loopback.NoteUserIntentBoundary(denialInvariantChat)
	s.write.NoteUserIntentBoundary(denialInvariantChat)
	s.read.NoteUserIntentBoundary(denialInvariantChat)
}

type denialInvariantOutcome struct {
	raised, authorized, denied bool
}

// denialInvariantCase drives one broker entry point; the name is the
// "<BrokerType>.<Method>" the structural check matches against.
type denialInvariantCase struct {
	name    string
	request func(ctx context.Context, s *denialInvariantStores, toolCallID string) (denialInvariantOutcome, error)
}

func denialInvariantCases(t *testing.T) []denialInvariantCase {
	t.Helper()
	strict := func(string) gate.Posture { return gate.PostureStrict }
	projectDir := t.TempDir()
	outsideWriteRoot := denialInvariantOutsideWriteRoot(t, projectDir)
	outsidePath := filepath.Join(t.TempDir(), "outside")
	ports := []uint16{8000}
	return []denialInvariantCase{
		{name: "ListenCheckpointBroker.Await", request: func(ctx context.Context, s *denialInvariantStores, call string) (denialInvariantOutcome, error) {
			b := &ListenCheckpointBroker{Checkpoints: s.checkpoints, Runtime: s.listen, Loopback: s.loopback,
				Provenance: denialInvariantProvenance{}, Posture: strict}
			res, err := b.Await(ctx, tools.LocalListenAsk{SessionID: denialInvariantChat, ToolCallID: call, ProjectDir: projectDir, Ports: ports})
			return denialInvariantOutcome{res.Raised, res.Authorized, res.Denied}, err
		}},
		{name: "LoopbackCheckpointBroker.Await", request: func(ctx context.Context, s *denialInvariantStores, call string) (denialInvariantOutcome, error) {
			b := &LoopbackCheckpointBroker{Checkpoints: s.checkpoints, Runtime: s.loopback,
				Provenance: denialInvariantProvenance{}, Posture: strict}
			res, err := b.Await(ctx, tools.LoopbackConnectAsk{SessionID: denialInvariantChat, ToolCallID: call, ProjectDir: projectDir, Ports: ports})
			return denialInvariantOutcome{res.Raised, res.Authorized, res.Denied}, err
		}},
		{name: "LocalNetworkCheckpointBroker.AwaitCombined", request: func(ctx context.Context, s *denialInvariantStores, call string) (denialInvariantOutcome, error) {
			b := &LocalNetworkCheckpointBroker{Checkpoints: s.checkpoints, Listen: s.listen, Loopback: s.loopback,
				Provenance: denialInvariantProvenance{}, Posture: strict}
			res, err := b.AwaitCombined(ctx, tools.LocalNetworkAsk{SessionID: denialInvariantChat, ToolCallID: call, ProjectDir: projectDir,
				ListenPorts: ports, ConnectPorts: ports})
			return denialInvariantOutcome{res.Raised, res.Authorized, res.Denied}, err
		}},
		{name: "WriteRootCheckpointBroker.Authorize", request: func(ctx context.Context, s *denialInvariantStores, call string) (denialInvariantOutcome, error) {
			b := &WriteRootCheckpointBroker{Checkpoints: s.checkpoints, Runtime: s.write, ReadRuntime: s.read, Posture: strict}
			res, err := b.Authorize(ctx, native.SandboxWriteRootAsk{SessionID: denialInvariantChat, ToolCallID: call, ProjectDir: projectDir,
				ProposedWriteRoot: outsideWriteRoot})
			return denialInvariantOutcome{res.Raised, res.Authorized, res.Denied}, err
		}},
		{name: "WriteRootCheckpointBroker.AuthorizeRead", request: func(ctx context.Context, s *denialInvariantStores, call string) (denialInvariantOutcome, error) {
			b := &WriteRootCheckpointBroker{Checkpoints: s.checkpoints, Runtime: s.write, ReadRuntime: s.read, Posture: strict}
			res, err := b.AuthorizeRead(ctx, native.SandboxReadPathAsk{SessionID: denialInvariantChat, ToolCallID: call, ProjectDir: projectDir,
				ProposedReadPath: outsidePath, ReadDenyPaths: []string{outsidePath}})
			return denialInvariantOutcome{res.Raised, res.Authorized, res.Denied}, err
		}},
	}
}

// denialInvariantOutsideWriteRoot is a path no default write root covers
// (temporary, cache, and home-relative roots all are); nothing is created there.
func denialInvariantOutsideWriteRoot(t *testing.T, projectDir string) string {
	t.Helper()
	roots := confine.WriteRootsForProject("", []string{projectDir})
	for _, base := range []string{"/opt", "/srv", "/usr/local", "/Users/Shared"} {
		candidate := filepath.Join(base, "paintedwolf-denial-invariant-probe")
		if !confine.PathWithinWriteRoots(candidate, roots) {
			return candidate
		}
	}
	t.Fatalf("every write-root probe candidate is inside a default write root %v; add a candidate outside them", roots)
	return ""
}

func TestSandboxBrokerDenialLeavesNoAuthority(t *testing.T) {
	t.Parallel()

	for _, tc := range denialInvariantCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newDenialInvariantStores()
			ctx := t.Context()

			first, err := tc.request(ctx, s, "call-1")
			testutil.FailErr(t, tc.name+" first request", err)
			if first.authorized {
				t.Fatalf("%s authorized a strict-posture request without an approving decision (asks=%d, outcome %+v); "+
					"strict posture must ask the human before granting, or, if the request is already covered, change this case to one strict posture asks about", tc.name, s.checkpoints.asks, first)
			}
			if s.checkpoints.asks != 1 || !first.raised {
				t.Fatalf("%s raised %d asks (outcome %+v), want exactly one denied ask; the fixture no longer reaches the human ask, so update this case to a request strict posture asks about", tc.name, s.checkpoints.asks, first)
			}
			if held := s.authority(); len(held) > 0 {
				t.Fatalf("%s recorded authority before or without human approval: %v survives a denied ask. "+
					"Record leases and grants only after an approving decision (evaluate the gate first; never grant ahead of gate.Evaluate or the checkpoint)", tc.name, held)
			}

			retry, err := tc.request(ctx, s, "call-2")
			testutil.FailErr(t, tc.name+" identical retry", err)
			if retry.authorized {
				t.Fatalf("%s authorized an identical retry after the human denied it (asks=%d, outcome %+v); a denial must not leave authority a retry can ride", tc.name, s.checkpoints.asks, retry)
			}
			if !retry.denied && s.checkpoints.asks != 2 {
				t.Fatalf("%s neither re-asked nor refused an identical retry after a denial (asks=%d, outcome %+v); a retry must reach the human or the recorded denial", tc.name, s.checkpoints.asks, retry)
			}
			if held := s.authority(); len(held) > 0 {
				t.Fatalf("%s recorded authority on a retry of a denied ask: %v; record authority only after an approving decision", tc.name, held)
			}

			s.userTurn()
			asksBefore := s.checkpoints.asks
			again, err := tc.request(ctx, s, "call-3")
			testutil.FailErr(t, tc.name+" retry after a new user turn", err)
			if again.authorized || s.checkpoints.asks != asksBefore+1 || !again.raised {
				t.Fatalf("%s did not ask the human again for an identical request after a new user turn (asks %d -> %d, outcome %+v); "+
					"a denial must be remembered as a denial, never as a lease that silently covers the next attempt", tc.name, asksBefore, s.checkpoints.asks, again)
			}
			if held := s.authority(); len(held) > 0 {
				t.Fatalf("%s recorded authority on a denied re-ask: %v; record authority only after an approving decision", tc.name, held)
			}
		})
	}
}

// TestSandboxBrokerDenialInvariantCoversEveryAskEntryPoint fails when a broker
// gains an ask entry point, or a new type raises sandbox asks, without a case
// in denialInvariantCases.
func TestSandboxBrokerDenialInvariantCoversEveryAskEntryPoint(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*.go")
	testutil.FailErr(t, "glob session package sources", err)
	fset := token.NewFileSet()
	entryPoints := map[string]bool{}
	askRaisers := map[string]bool{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		testutil.FailErr(t, "parse "+path, err)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			recv := denialInvariantReceiverName(fn.Recv.List[0].Type)
			if recv == "" {
				continue
			}
			if denialInvariantCallsAwaitSandboxAsk(fn) {
				askRaisers[recv] = true
			}
			if strings.HasSuffix(recv, "CheckpointBroker") && fn.Name.IsExported() && denialInvariantIsAskEntryPoint(fn) {
				entryPoints[recv+"."+fn.Name.Name] = true
			}
		}
	}

	covered := map[string]bool{}
	coveredTypes := map[string]bool{}
	for _, tc := range denialInvariantCases(t) {
		covered[tc.name] = true
		coveredTypes[strings.SplitN(tc.name, ".", 2)[0]] = true
	}
	var missing, stale, uncoveredRaisers []string
	for name := range entryPoints {
		if !covered[name] {
			missing = append(missing, name)
		}
	}
	for name := range covered {
		if !entryPoints[name] {
			stale = append(stale, name)
		}
	}
	for recv := range askRaisers {
		if !coveredTypes[recv] {
			uncoveredRaisers = append(uncoveredRaisers, recv)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	sort.Strings(uncoveredRaisers)
	if len(missing) > 0 {
		t.Errorf("broker ask entry points without a denial-invariant case: %v; add a case to denialInvariantCases that drives it in strict posture against a denying checkpoint", missing)
	}
	if len(stale) > 0 {
		t.Errorf("denial-invariant cases name entry points that no longer exist: %v; rename or remove them", stale)
	}
	if len(uncoveredRaisers) > 0 {
		t.Errorf("types raising sandbox asks without a denial-invariant case: %v; add a case for each exported ask entry point", uncoveredRaisers)
	}
	if !askRaisers["LoopbackCheckpointBroker"] {
		t.Error("the structural scan found no ask raised by LoopbackCheckpointBroker; the scan no longer recognizes sandbox asks, so fix denialInvariantCallsAwaitSandboxAsk")
	}
}

func denialInvariantReceiverName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// denialInvariantIsAskEntryPoint matches func(ctx, <pkg>.<Name>Ask) (..., error).
func denialInvariantIsAskEntryPoint(fn *ast.FuncDecl) bool {
	params := fn.Type.Params.List
	if len(params) != 2 || len(params[0].Names) > 1 || len(params[1].Names) > 1 {
		return false
	}
	sel, ok := params[1].Type.(*ast.SelectorExpr)
	if !ok || !strings.HasSuffix(sel.Sel.Name, "Ask") {
		return false
	}
	results := fn.Type.Results
	if results == nil || len(results.List) == 0 {
		return false
	}
	last, ok := results.List[len(results.List)-1].Type.(*ast.Ident)
	return ok && last.Name == "error"
}

func denialInvariantCallsAwaitSandboxAsk(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "awaitSandboxAsk" {
				found = true
			}
		}
		return !found
	})
	return found
}
