package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestProgramPartialEffectsSurviveSupervisorLossWithoutReplay(t *testing.T) {
	dir := t.TempDir()
	ledger := bindLedgerForWrites(t, dir)
	database := ledger.LedgerDB()
	testdbseed.InsertSession(t, database, "s1", "p1")
	recorder := invocation.NewSQLRecorder(database)

	server, requests, received := partialEffectsHTTPServer(t)
	serverAddr := netip.MustParseAddrPort(server.Listener.Addr().(*net.TCPAddr).String())

	source := "tee new.txt\ncurl " + server.URL + "\nprintf never\n"
	programID, rootID, parentReceiptID, spans := beginPartialEffectsProgram(t, recorder, source, server.URL)

	tc := nativefixture.Context(dir)
	tc.Identity.ProjectID, tc.Identity.SessionID, tc.Identity.ToolCallID, tc.Identity.Agent, tc.Identity.UserTurn = "p1", "s1", "actual-model-call", "fixture", 1
	tc.Source.SourceLedger = ledger
	tc.Source.History = tools.SourceHistory{Files: ledger.History, Comparison: ledger.Comparisons, Git: ledger.Git, Authorship: ledger.Walk}
	tc.Source.Commands, tc.Source.Observations, tc.Source.GitMutations = ledger.Commands, ledger.Inventory, ledger.Git
	tc.Source.SourceMutations = projectsource.NewSourceMutationService(ledger.LedgerDB(), ledger)
	classes, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandNetworkEgress)
	testutil.FailErr(t, "compile fixture profile", err)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "fixture", CommandEffects: classes}})
	executor := toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), tools.NewDefaultRegistry(), "fixture")
	writeDefinition, httpDefinition := partialEffectsDefinitions(t)
	scope := toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "p1", RootID: "r1"}
	parent := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: programID, NodeID: "submitted", ExecutionID: rootID}, ActorID: "fixture-person", SessionID: "s1", Scope: scope, SourceUnitID: "submitted", Span: toolcontract.SourceSpan{Line: 1, Column: 1}, CatalogDigest: toolcontract.LanguageCatalogDigest()}
	frameMemory := programDoorMemory(t)
	frame, err := toolexecution.FreezeProgramFrame(t.Context(), parent, tc, toolexecution.ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": source}}, frameMemory)
	testutil.FailErr(t, "capture actual tree", err)
	t.Cleanup(frame.Close)
	surface, err := executor.Metadata.FreezeProgramSurface(t.Context(), platform.ToolFilter{ProfileID: "fixture"}, []toolcontract.OperationDefinition{writeDefinition, httpDefinition})
	testutil.FailErr(t, "capture actual profile", err)
	owner := progress.NewMemoryStore()
	testutil.FailErr(t, "set actual progress owner", owner.Set("s1", "- [ ] Create fixture"))
	retention := partialEffectsCapture(t, recorder, database, programID, "s1", "p1")
	observer := &invocation.ProgramAdmissionRecorder{Recorder: recorder, Inputs: retention}
	memory := programDoorMemory(t)

	prepare := func(definition toolcontract.OperationDefinition, node, command, canonical string, args map[string]any) toolcontract.BoundOperation {
		child := parent
		child.Identity = toolcontract.ExecutionIdentity{ProgramID: programID, NodeID: node, ExecutionID: uuid.NewString(), ParentExecutionID: rootID}
		child.Span = spans[node]
		expanded, releaseExpanded, err := toolcontract.NewReviewedOperationInputs(t.Context(), memory, args)
		testutil.FailErr(t, "freeze expanded arguments", err)
		defer releaseExpanded()
		testutil.FailErr(t, "prepare dynamic child", recorder.PrepareChild(t.Context(), toolcontract.ProgramChildStart{ParentReceiptID: parentReceiptID, Invocation: child, Definition: definition, ArgumentsReference: "output:expanded-" + node, ExpandedInputs: expanded, Canonical: canonical}))
		var mode string
		for _, c := range definition.Facts().Commands {
			if c.Name == command {
				mode = c.Effects[0].Mode
			}
		}
		bound, err := toolcontract.BindOperation(definition, child, command, mode, nil)
		testutil.FailErr(t, "bind catalogued operation", err)
		return bound
	}

	writeArgs := map[string]any{"path": "new.txt", "content": "created before the loss\n"}
	write := prepare(writeDefinition, "write-node", "tee", "tee new.txt", writeArgs)
	writeEffect := partialEffectsWriteEffect(boundary, database, write, writeArgs)
	written, err := executor.InvokeNative(t.Context(), frame, surface, memory, programDoorProgress{owner}, observer, writeEffect, tc)
	testutil.FailErr(t, "invoke actual mutation door", err)
	journal := written.Producer.Evidence.RecoveryRef
	testutil.FailErr(t, "settle completed write", recorder.SettleChild(t.Context(), write.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: written.Producer, OwnerInvoked: written.OwnerInvoked, OutputPublishable: written.OutputPublishable, DetailReference: "output:write-result", PublishedRevision: journal}))

	httpArgs := map[string]any{"url": server.URL, "method": "POST"}
	attempt := prepare(httpDefinition, "http-node", "curl", "curl "+server.URL, httpArgs)
	httpEffect := toolexecution.NativeEffect{Operation: attempt, Stage: toolcontract.ExecutionStage{OperationID: "http_request", ExecutionID: attempt.Invocation().Identity.ExecutionID, Argv: []string{"curl", server.URL}}, Arguments: httpArgs}
	httpEffect.PrepareReviewed = func(_ context.Context, args map[string]any, _ tools.ToolContext) (toolexecution.PreparedNativeEffect, error) {
		target := args["url"].(string)
		return toolexecution.PreparedNativeEffect{Operation: attempt, InvokeOwner: func(ctx context.Context, _ tools.ToolContext, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
			_, err := outboundhttp.Do(ctx, outboundhttp.Request{Class: egressclass.AgentHTTPRequest, Method: "POST", URL: target, Body: []byte("once"), Timeout: time.Minute,
				AllowAddress: func(addr netip.Addr, port uint16) bool { return addr == serverAddr.Addr() && port == serverAddr.Port() }})
			// A request that left the host has an unknown remote outcome.
			var exchange *outboundhttp.ExchangeError
			if errors.As(err, &exchange) && exchange.RequestsStarted > 0 && exchange.ResponsesReceived == 0 {
				return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerInterrupted, EffectCertainty: toolcontract.EffectUnknown}, err
			}
			return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}, err
		}}, nil
	}
	supervisor, loseSupervisor := context.WithCancel(t.Context())
	type outcome struct {
		result toolexecution.NativeInvocationResult
		err    error
	}
	attempted := make(chan outcome, 1)
	go func() {
		result, err := executor.InvokeNative(supervisor, frame, surface, memory, programDoorProgress{owner}, observer, httpEffect, tc)
		attempted <- outcome{result, err}
	}()
	select {
	case <-received:
	case <-time.After(30 * time.Second):
		t.Fatal("HTTP attempt never reached the remote side")
	}

	// The supervisor is lost while the attempt is outstanding. Recovery settles
	// the ledger without the live owner and without retrying anything.
	count, err := recorder.InterruptPrograms(t.Context(), "s1", "output:supervisor-loss")
	testutil.FailErr(t, "reconcile lost supervisor", err)
	if count != 1 {
		t.Fatalf("interrupted %d programs", count)
	}
	loseSupervisor()
	late := <-attempted
	if late.err == nil || !late.result.OwnerInvoked || late.result.Producer.EffectCertainty != toolcontract.EffectUnknown {
		t.Fatalf("lost attempt claimed a known outcome: %+v %v", late.result, late.err)
	}
	if err := recorder.SettleChild(t.Context(), attempt.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: late.result.Producer, OwnerInvoked: true, DetailReference: "output:late-result"}); err == nil {
		t.Fatal("late owner result rewrote reconciled host-loss facts")
	}
	if again, err := recorder.InterruptPrograms(t.Context(), "s1", "output:second-recovery"); err != nil || again != 0 {
		t.Fatalf("repeated recovery=%d %v", again, err)
	}

	page, err := recorder.ReadProgramPage(t.Context(), "s1", programID, "", 20)
	testutil.FailErr(t, "inspect reconciled program", err)
	if page.Program.State != "interrupted" || len(page.Executions) != 2 {
		t.Fatalf("program facts after loss: %+v", page)
	}
	completed, unknown := page.Executions[0], page.Executions[1]
	if completed.NodeID != "write-node" || completed.State != "completed" || completed.EffectCertainty != "completed" || completed.RecoveryRef != journal || journal == "" {
		t.Fatalf("completed write lost its journal: %+v", completed)
	}
	if unknown.NodeID != "http-node" || unknown.State != "interrupted" || unknown.EffectCertainty != "unknown" || unknown.StatusCode != nil {
		t.Fatalf("unknown attempt became known: %+v", unknown)
	}
	states := map[string]string{}
	for _, node := range page.Nodes {
		states[node.ID] = node.State
	}
	if states["write-node"] != "reached" || states["http-node"] != "reached" || states["untaken-node"] != "not_started" {
		t.Fatalf("node reachability after loss: %+v", states)
	}

	// The journal named by the receipt still resolves to the landed bytes.
	var path, op string
	testutil.FailErr(t, "resolve recorded journal", database.QueryRowContext(t.Context(), `SELECT path,op FROM source_effects WHERE id=?`, journal).Scan(&path, &op))
	data, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	testutil.FailErr(t, "read landed write", err)
	if path != "new.txt" || op != "create" || string(data) != "created before the loss\n" || ledgerRows(t, ledger) != 1 {
		t.Fatalf("journal %s %s does not match landed %q", path, op, data)
	}
	var reviewedCount int
	testutil.FailErr(t, "count retained reviewed inputs", database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_executions e JOIN invocation_program_outputs o ON o.program_id=e.program_id AND o.reference=e.reviewed_arguments_ref WHERE e.program_id=?`, programID).Scan(&reviewedCount))
	if requests.Load() != 1 || reviewedCount != 2 || memory.InUse().State != 0 {
		t.Fatalf("replayed or leaked: requests=%d retained=%d held=%d", requests.Load(), reviewedCount, memory.InUse().State)
	}
}

func beginPartialEffectsProgram(t *testing.T, recorder *invocation.SQLRecorder, source, address string) (programID, rootID, parentReceiptID string, spans map[string]toolcontract.SourceSpan) {
	t.Helper()
	digest := sha256.Sum256([]byte(source))
	sourceDigest := hex.EncodeToString(digest[:])
	spans = map[string]toolcontract.SourceSpan{
		"write-node":   {StartByte: 0, EndByte: 11, Line: 1, Column: 1},
		"http-node":    {StartByte: 12, EndByte: 17 + len(address), Line: 2, Column: 1},
		"untaken-node": {StartByte: 18 + len(address), EndByte: len(source) - 1, Line: 3, Column: 1},
	}
	commandContract, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("command contract missing")
	}
	parentReceipt, err := recorder.Begin(t.Context(), invocation.Start{ProjectID: "p1", SessionID: "s1", ToolCallID: "actual-model-call", ToolName: "command", Args: map[string]any{"command": source}, Contract: commandContract})
	testutil.FailErr(t, "begin actual model call", err)
	parentReceiptID = parentReceipt.ID
	programID, rootID = uuid.NewString(), uuid.NewString()
	start := toolcontract.ProgramStart{
		Version:       toolcontract.ProgramVersion{ProgramID: programID, ParentReceiptID: parentReceiptID, RootExecutionID: rootID, LanguageVersion: toolcontract.LanguageVersion, CatalogDigest: toolcontract.LanguageCatalogDigest(), SourceUnitID: "submitted", SourceDigest: sourceDigest, SourceReference: "output:exact-program"},
		PlanReference: "output:validated-plan", SourceExact: true, SyntaxValidated: true,
		Sources: []toolcontract.ProgramSource{{UnitID: "submitted", Pin: toolcontract.SourcePin{SourceID: "submitted-program", Revision: sourceDigest, Representation: toolcontract.UTF8SourceBytes, SHA256: sourceDigest, EndByte: int64(len(source))}, DetailReference: "output:exact-program"}},
	}
	for _, id := range []string{"write-node", "http-node", "untaken-node"} {
		start.Nodes = append(start.Nodes, toolcontract.ProgramNode{ID: id, SourceUnitID: "submitted", Span: spans[id]})
	}
	testutil.FailErr(t, "attach validated program", recorder.BeginProgram(t.Context(), start))

	return
}

func partialEffectsDefinitions(t *testing.T) (toolcontract.OperationDefinition, toolcontract.OperationDefinition) {
	t.Helper()
	schemas, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "read actual schemas", err)
	definitionFor := func(name string) toolcontract.OperationDefinition {
		meta, ok := schemas.ToolMeta(name)
		if !ok {
			t.Fatalf("actual %s schema missing", name)
		}
		encoded, err := json.Marshal(meta.ArgsSchema)
		testutil.FailErr(t, "freeze actual schema", err)
		contract, ok := toolcontract.Lookup(name)
		if !ok {
			t.Fatalf("%s contract missing", name)
		}
		definition, err := toolcontract.ToolOperation(name, encoded, contract)
		testutil.FailErr(t, "compile actual declaration", err)
		return definition
	}
	return definitionFor("write"), definitionFor("http_request")
}

func partialEffectsHTTPServer(t *testing.T) (*httptest.Server, *atomic.Int32, <-chan struct{}) {
	t.Helper()
	requests := &atomic.Int32{}
	received := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(received)
		}
		// The request reached the remote side; its response never returns.
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	return server, requests, received
}

func partialEffectsWriteEffect(boundary *sandbox.Boundary, database db.Handle, write toolcontract.BoundOperation, arguments map[string]any) toolexecution.NativeEffect {
	effect := toolexecution.NativeEffect{Operation: write, Stage: toolcontract.ExecutionStage{OperationID: "write", ExecutionID: write.Invocation().Identity.ExecutionID, Argv: []string{"tee", "new.txt"}}, Arguments: arguments}
	effect.PrepareReviewed = func(ctx context.Context, args map[string]any, actual tools.ToolContext) (toolexecution.PreparedNativeEffect, error) {
		paths, err := projectpaths.NewProgramPaths(ctx, boundary, actual, "")
		if err != nil {
			return toolexecution.PreparedNativeEffect{}, err
		}
		operand, err := paths.Bind(ctx, args["path"].(string), true)
		if err != nil {
			return toolexecution.PreparedNativeEffect{}, err
		}
		content := []byte(args["content"].(string))
		return toolexecution.PreparedNativeEffect{Operation: write, InvokeOwner: func(ctx context.Context, actual tools.ToolContext, _ commandprogram.Streams) (toolcontract.ProducerResult, error) {
			target, err := operand.MutationTarget()
			if err != nil {
				return toolcontract.ProducerResult{}, err
			}
			if err := applyAgentFile(ctx, actual, resolvedMutationTarget(target), content, nil, ""); err != nil {
				return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.EffectUnknown}, err
			}
			var journal string
			if err := database.QueryRowContext(ctx, `SELECT id FROM source_effects WHERE project_id='p1' AND path='new.txt'`).Scan(&journal); err != nil {
				return toolcontract.ProducerResult{}, err
			}
			return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectCompleted, Evidence: toolcontract.OperationEvidence{Owner: "filesystem", Kind: "journal", OwnerRef: journal, RecoveryRef: journal}}, nil
		}}, nil
	}
	return effect
}
