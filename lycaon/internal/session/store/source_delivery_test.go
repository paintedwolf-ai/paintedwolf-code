package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDeliveredSourcePersistsExactWindowAndDoesNotQualifyUnseenBytes(t *testing.T) {
	dir := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(dir, "delivery.db"))
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, dir)
	owner := NewSQL(database)
	owner.SetDataDir(dir)
	session, err := owner.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create source delivery session", err)
	captured := []byte("first unseen\nsecond shown\nthird unseen\n")
	page := []byte("second shown\n")
	sum := sha256.Sum256(captured)
	pin := toolcontract.SourcePin{SourceID: "source:fixture", Revision: "revision:one", Representation: toolcontract.UTF8SourceBytes, SHA256: hex.EncodeToString(sum[:]), EndByte: int64(len(captured))}
	mapping := toolcontract.StreamSourceMapping{Source: pin, SourceStartByte: 13, SourceEndByte: 26, OutputEndByte: 13}
	observation, err := evidence.ConfirmDeliveredSource("src/fixture.txt", pin, captured, page, mapping, "response:one", "delivery:one")
	testutil.FailErr(t, "confirm actual page bytes", err)
	before, err := owner.LoadLedger(t.Context(), session.ID)
	testutil.FailErr(t, "read ledger before delivery commit", err)
	if len(before.Handles) != 0 {
		t.Fatal("capture alone populated observation ledger")
	}
	handle, err := owner.CommitDeliveredSource(t.Context(), session.ID, observation)
	testutil.FailErr(t, "commit exact delivered page", err)
	repeated, err := owner.CommitDeliveredSource(t.Context(), session.ID, observation)
	testutil.FailErr(t, "repeat acknowledged delivery", err)
	if repeated != handle {
		t.Fatal("delivery retry minted duplicate observation")
	}
	// A fresh recorder reads persisted metadata and captured page bodies; unseen
	// ranges remain unavailable after the original delivering component disappears.
	restored := NewSQL(database)
	restored.SetDataDir(dir)
	ledger, err := restored.LoadLedger(t.Context(), session.ID)
	testutil.FailErr(t, "restore delivered source ledger", err)
	record, ok := evidence.ResolveHandle(ledger, handle)
	if !ok || record.SourceDelivery == nil || record.SourceDelivery.Source != pin {
		t.Fatalf("delivery metadata missing: %+v", record)
	}
	for _, line := range []int{1, 3} {
		if yes, _ := evidence.VerifyFileRegion(record, evidence.Claim{Path: "src/fixture.txt", Line: line}); yes {
			t.Fatalf("unseen source line%d qualified", line)
		}
	}
	if yes, _ := evidence.VerifyFileRegion(record, evidence.Claim{Path: "src/fixture.txt", Line: 2, Excerpt: "second shown"}); !yes {
		t.Fatal("actual delivered exact window did not qualify")
	}
}

type deliveryQuota struct {
	state, disk, scratch int
	detector             int64
}

func (q *deliveryQuota) ReserveProgramState(context.Context, int64) (func(), error) {
	q.state++
	return func() { q.state-- }, nil
}
func (q *deliveryQuota) ReserveSpill(context.Context, int64) (func(), error) {
	q.disk++
	return func() { q.disk-- }, nil
}
func (q *deliveryQuota) Reserve(context.Context, int64) (func(), error) {
	q.scratch++
	return func() { q.scratch-- }, nil
}
func (q *deliveryQuota) ReserveParserScratch(ctx context.Context, n int64) (func(), error) {
	return q.Reserve(ctx, n)
}
func (q *deliveryQuota) ReserveBytes(_ context.Context, n int64) error { q.detector += n; return nil }
func (q *deliveryQuota) ReleaseBytes(n int64)                          { q.detector -= n }

type retainedDeliveryFixture struct {
	owner                                  *SQL
	recorder                               *invocation.SQLRecorder
	outputs                                *invocation.ProgramOutputRetention
	source                                 []byte
	pin                                    toolcontract.SourcePin
	mapping                                toolcontract.StreamSourceMapping
	session, program, execution, reference string
	quota                                  *deliveryQuota
}

func newRetainedDeliveryFixture(t *testing.T) retainedDeliveryFixture {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()
	dataDir := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(dataDir, "delivery.db"))
	rootID := testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, dir)
	owner := NewSQL(database)
	owner.SetDataDir(dataDir)
	session, err := owner.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create actual delivery task", err)
	testutil.FailErr(t, "create native source fixture", os.WriteFile(filepath.Join(dir, "fixture.txt"), []byte("first unseen\nsecond shown\nthird unseen\n"), 0600))
	recorder := invocation.NewSQLRecorder(database)
	commandContract, _ := toolcontract.Lookup("command")
	const command = "cat fixture.txt"
	parent, err := recorder.Begin(ctx, invocation.Start{ProjectID: testdbseed.DefaultProjectID, SessionID: session.ID, ToolCallID: "actual-source-call", ToolName: "command", Args: map[string]any{"command": command}, Contract: commandContract})
	testutil.FailErr(t, "begin actual model parent", err)
	quota := &deliveryQuota{}
	parser, err := commandsurface.NewProgramParser(ctx, quota)
	testutil.FailErr(t, "reserve real program parser", err)
	parsed, err := parser.Parse([]byte(command), commandsurface.ParseOptions{})
	testutil.FailErr(t, "validate actual source-inspection program", err)
	testutil.FailErr(t, "close parser scratch", parser.Close())
	snapshot, err := parsed.SnapshotWithMemory(ctx, quota)
	testutil.FailErr(t, "admit actual typed program snapshot", err)
	defer snapshot.Close()
	defer parsed.Close()
	program, rootExecution := uuid.NewString(), uuid.NewString()
	statement := snapshot.Plan.Statements[0]
	outputs, err := invocation.NewProgramOutputRetention(ctx, recorder, blobstore.Store{Root: dataDir}, session.ID, quota)
	testutil.FailErr(t, "admit actual session output lifecycle", err)
	testutil.FailErr(t, "restore actual retained refs", outputs.Restore(ctx))
	projector := captureprojection.New(secretmatch.NewInertMatcher(), nil)
	scope := captureprojection.Scope{ProjectID: testdbseed.DefaultProjectID, RootSessionID: session.ID, SessionID: session.ID}
	stream := captureprojection.StreamOptions{WorkingMemory: quota, Bounds: secretmatch.StreamBounds{InputBytes: 1 << 20, OutputBytes: 1 << 20, MemoryBytes: 32 << 20, DetectorWork: 1 << 30}}
	retain := func(claim string, content []byte, preview int) *tooloutput.StreamSpill {
		var screened bytes.Buffer
		_, err := projector.ScreenStream(ctx, scope, "capture.native", bytes.NewReader(content), &screened, stream)
		testutil.FailErr(t, "screen actual native bytes", err)
		spill, err := tooloutput.RetainScreenedStream(ctx, tooloutput.ScreenedReader(io.NopCloser(bytes.NewReader(screened.Bytes()))), tooloutput.StreamSpillOptions{Store: recorder.ProgramBlobStore(blobstore.Store{Root: dataDir}), ClaimID: claim, MaximumBytes: 1 << 20, PreviewBytes: preview, Admission: quota, WorkingMemory: quota, RetainedState: quota})
		testutil.FailErr(t, "retain complete actual screened stream", err)
		return spill
	}
	sourceRef := "output:submitted-source"
	start := invocation.ProgramStart{Version: invocation.ProgramVersion{ProgramID: program, ParentReceiptID: parent.ID, RootExecutionID: rootExecution, LanguageVersion: snapshot.Plan.Version, CatalogDigest: snapshot.Plan.CatalogDigest, SourceUnitID: "submitted", SourceDigest: snapshot.Plan.SourceDigest, SourceReference: sourceRef}, PlanReference: sourceRef, SourceExact: true, SyntaxValidated: true, Sources: []invocation.ProgramSource{{UnitID: "submitted", Pin: toolcontract.SourcePin{SourceID: "submitted", Revision: snapshot.Plan.SourceDigest, Representation: toolcontract.UTF8SourceBytes, SHA256: snapshot.Plan.SourceDigest, EndByte: int64(len(command))}, DetailReference: sourceRef}}, Nodes: []invocation.ProgramNode{{ID: string(statement.ID), SourceUnitID: "submitted", Span: statement.Span}}}
	testutil.FailErr(t, "attach actual source/program without synthetic child", outputs.BeginProgram(ctx, start, retain(program, []byte(command), 0), false))
	schemas, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "load actual native read schema", err)
	meta, ok := schemas.ToolMeta("read")
	if !ok {
		t.Fatal("read schema absent")
	}
	schema, err := json.Marshal(meta.ArgsSchema)
	testutil.FailErr(t, "freeze actual read schema", err)
	contract, _ := toolcontract.Lookup("read")
	definition, err := toolcontract.ToolOperation("read", schema, contract)
	testutil.FailErr(t, "freeze actual native read operation", err)
	in := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: program, NodeID: string(statement.ID), ExecutionID: uuid.NewString(), ParentExecutionID: rootExecution}, ActorID: "fixture-person", SessionID: session.ID, Scope: toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: testdbseed.DefaultProjectID, RootID: rootID}, SourceUnitID: "submitted", Span: statement.Span, CatalogDigest: snapshot.Plan.CatalogDigest}
	review, releaseReview, err := toolcontract.NewReviewedOperationInputs(ctx, quota, map[string]any{"path": "fixture.txt"})
	testutil.FailErr(t, "freeze actual expanded read operands", err)
	defer releaseReview()

	declaration := definition.Facts().Commands[0]
	bound, err := toolcontract.BindOperation(definition, in, declaration.Name, declaration.Effects[0].Mode, nil)
	testutil.FailErr(t, "bind actual catalog read form", err)
	capture := invocation.ProgramInputCapture{State: quota, Outputs: outputs, Projector: projector, ProgramID: program, Scope: scope, Screening: stream, MaximumNodes: 1024, MaximumDepth: 64}
	_, err = capture.PrepareChild(ctx, invocation.ChildStart{ParentReceiptID: parent.ID, Invocation: in, Definition: definition, ExpandedInputs: review, Canonical: command})
	testutil.FailErr(t, "atomically retain actual expanded input and dynamic read child", err)
	marker := invocation.ProgramAdmissionRecorder{Recorder: recorder, Inputs: capture}
	testutil.FailErr(t, "retain actual read input before owner", marker.BeforeNativeOwner(ctx, bound, "actual-source-call", review))
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{Agent: "fixture", ProjectID: testdbseed.DefaultProjectID, SessionID: session.ID, ToolCallID: "actual-source-call"}, Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: rootID, Label: "root", Path: dir, IsPrimary: true}}, ActiveRootID: rootID, SourceWorkspaceKind: api.SourceWorkspaceKindProject}}
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "fixture", Tools: map[string]bool{"read": true}}})
	paths, err := projectpaths.NewProgramPaths(ctx, boundary, tc, "")
	testutil.FailErr(t, "bind existing read authority", err)
	path, err := paths.Bind(ctx, "fixture.txt", false)
	testutil.FailErr(t, "resolve exact native operand", err)
	text, err := sourceview.LoadText(ctx, "read", sourceview.AccessRead, tc, path.Resolved)
	testutil.FailErr(t, "capture through existing native source owner", err)
	source := []byte(text.Content)
	sum := sha256.Sum256(source)
	pin := toolcontract.SourcePin{SourceID: rootID + "/" + path.Resolved.ScopeRel, Revision: text.SHA256(), Representation: toolcontract.LogicalUTF8LF, SHA256: hex.EncodeToString(sum[:]), EndByte: int64(len(source))}
	mapping := toolcontract.StreamSourceMapping{Source: pin, SourceEndByte: int64(len(source)), OutputEndByte: int64(len(source))}
	reference := "output:actual-native-source"
	spill := retain(in.Identity.ExecutionID, source, 13)
	testutil.FailErr(t, "retain actual native source mapping outside stdout", outputs.Attach(ctx, program, invocation.RetainedOutput{ExecutionID: in.Identity.ExecutionID, Stream: "stdout", Reference: reference, Complete: true, Mappings: []toolcontract.StreamSourceMapping{mapping}}, spill))
	result := toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, PublicationBasis: toolcontract.ZeroStatusPublication, EffectCertainty: toolcontract.NoEffect, Evidence: toolcontract.OperationEvidence{Owner: contract.Owner, Kind: "source_snapshot", EvidenceRef: pin.SourceID}}
	testutil.FailErr(t, "settle actual executed read independently of delivery", recorder.SettleChild(ctx, in.Identity.ExecutionID, invocation.ChildSettlement{Result: result, OwnerInvoked: true, OutputPublishable: true, DetailReference: reference}))
	return retainedDeliveryFixture{owner: owner, recorder: recorder, outputs: outputs, source: source, pin: pin, mapping: mapping, session: session.ID, program: program, execution: in.Identity.ExecutionID, reference: reference, quota: quota}
}

func TestProgramRetainedNativeSourceOnlyQualifiesActuallyDeliveredExactPages(t *testing.T) {
	f := newRetainedDeliveryFixture(t)
	ctx := t.Context()
	before, err := f.owner.LoadLedger(ctx, f.session)
	testutil.FailErr(t, "inspect before any delivery", err)
	if len(before.Handles) != 0 {
		t.Fatal("internal capture/full retained output became model observation")
	}
	deliver := func(start, end int64, response string) (string, evidence.DeliveredSourceObservation) {
		reader, err := f.outputs.Open(ctx, f.session, f.program, f.reference)
		testutil.FailErr(t, "fetch real retained source stream", err)
		_, err = io.CopyN(io.Discard, reader, start)
		testutil.FailErr(t, "skip actual retained prefix", err)
		page := make([]byte, end-start)
		_, err = io.ReadFull(reader, page)
		testutil.FailErr(t, "read actual retained page", err)
		testutil.FailErr(t, "close actual output reader", reader.Close())
		projection, err := commandprogram.ProjectStreamMappings(ctx, f.quota, []toolcontract.StreamSourceMapping{f.mapping}, start, end-start, commandprogram.MappingExactSlice)
		testutil.FailErr(t, "clip native mapping to emitted page", err)
		defer projection.Close()
		if len(projection.Mappings) != 1 {
			t.Fatal("exact native page lost candidate mapping")
		}
		observation, err := evidence.ConfirmDeliveredSource("fixture.txt", f.pin, f.source, page, projection.Mappings[0], response, "delivery:"+response)
		testutil.FailErr(t, "confirm actual emitted bytes against native capture", err)
		// The host delivery acknowledgement is explicit. Reading/fetching above has
		// not committed evidence; the existing source owner does so only here.
		result, err := f.recorder.CommitProgramSourceDelivery(ctx, invocation.ProgramSourceDelivery{SessionID: f.session, ProgramID: f.program, OutputReference: f.reference, OutputStartByte: start, OutputEndByte: end, Observation: observation}, f.owner, f.quota)
		testutil.FailErr(t, "commit exact native delivery through current evidence owner", err)
		if !result.ReceiptRecorded || result.SourceOwnerReference == "" {
			t.Fatal("delivery lost independent owner/receipt settlement")
		}
		return result.SourceOwnerReference, observation
	}
	_, preview := deliver(0, 13, "response:preview")
	shown, exactPage := deliver(13, 26, "response:page")
	// A transform deliberately drops mapping candidates; stdout text naming a
	// file or matching a captured string cannot replace that producer fact.
	transformed := strings.ToUpper(string(f.source))
	transformedMapping, err := commandprogram.ProjectStreamMappings(ctx, f.quota, []toolcontract.StreamSourceMapping{f.mapping}, 0, int64(len(transformed)), commandprogram.MappingArbitrary)
	testutil.FailErr(t, "drop arbitrary transform mappings", err)
	defer transformedMapping.Close()
	if len(transformedMapping.Mappings) != 0 {
		t.Fatal("transform retained a source attestation")
	}
	if _, err = evidence.ConfirmDeliveredSource("fixture.txt", f.pin, f.source, []byte(transformed[:13]), toolcontract.StreamSourceMapping{Source: f.pin, SourceEndByte: 13, OutputEndByte: 13}, "response:transformed", "delivery:transformed"); err == nil {
		t.Fatal("transformed source-like output qualified")
	}
	failed, err := f.recorder.CommitProgramSourceDelivery(ctx, invocation.ProgramSourceDelivery{SessionID: f.session, ProgramID: f.program, OutputReference: "output:external-printed-path", OutputStartByte: 0, OutputEndByte: 13, Observation: preview}, f.owner, f.quota)
	if err == nil || failed.SourceOwnerReference != "" {
		t.Fatal("printed external path created native delivery evidence")
	}
	// Idempotent acknowledgement records exactly one delivery, and unknown output
	// receipt errors cannot erase an already committed source owner observation.
	repeated, err := f.recorder.CommitProgramSourceDelivery(ctx, invocation.ProgramSourceDelivery{SessionID: f.session, ProgramID: f.program, OutputReference: f.reference, OutputStartByte: 13, OutputEndByte: 26, Observation: exactPage}, f.owner, f.quota)
	testutil.FailErr(t, "repeat same exact acknowledged delivery", err)
	if repeated.SourceOwnerReference != shown {
		t.Fatal("delivery retry manufactured another observation")
	}
	restored := NewSQL(f.owner.db)
	restored.SetDataDir(f.owner.dataDir)
	ledger, err := restored.LoadLedger(ctx, f.session)
	testutil.FailErr(t, "recover qualified source pages from actual owner", err)
	record, ok := evidence.ResolveHandle(ledger, shown)
	if !ok {
		t.Fatal("qualified exact page was not retained")
	}
	if yes, _ := evidence.VerifyFileRegion(record, evidence.Claim{Path: "fixture.txt", Line: 2, Excerpt: "second shown"}); !yes {
		t.Fatal("actually fetched/delivered page did not qualify")
	}
	for _, line := range []int{1, 3} {
		if yes, _ := evidence.VerifyFileRegion(record, evidence.Claim{Path: "fixture.txt", Line: line}); yes {
			t.Fatalf("unseen range %d qualified from retained full body", line)
		}
	}
	_, err = f.owner.db.ExecContext(ctx, `DELETE FROM invocation_programs WHERE id=?`, f.program)
	testutil.FailErr(t, "release compacted invocation detail references", err)
	testutil.FailErr(t, "collect full output while evidence owner keeps only delivered page", f.outputs.Close(ctx))
	after, err := restored.LoadLedger(ctx, f.session)
	testutil.FailErr(t, "retain exact observation through output collection", err)
	if _, ok = evidence.ResolveHandle(after, shown); !ok {
		t.Fatal("receipt/output collection erased independent delivered evidence")
	}
	if f.quota.state != 0 || f.quota.disk != 0 || f.quota.scratch != 0 || f.quota.detector != 0 {
		t.Fatalf("source delivery lifetime leaked: %+v", f.quota)
	}
}
