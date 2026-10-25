package hitl_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/capabilitygrants"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNativeAuthorityProjectionPreservesExactRawPaths(t *testing.T) {
	for _, path := range []string{"/fixture/socket\xff ", "/fixture/socket\xfe ", "/fixture/socket\ufffd "} {
		memory := &nativeReviewMemory{}
		once := hitl.CurrentActionOption()
		once.Authority = []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthoritySocketPermit, SessionID: "session", ToolCallID: "model-call", ExecutionID: "actual-child", ActionDigest: "actual-reviewed-action", Sockets: []hitl.ApprovalSocketTarget{{ApprovedPath: path, ResolvedPath: path}}}}
		original := []hitl.ApprovalOption{once}
		projection, release, err := hitl.NewNativeAuthorityProjection(t.Context(), memory, original)
		if err != nil {
			t.Fatal(err)
		}
		if !projection.Valid() || projection.Representation() != "native_approval_authority_v1" || !strings.Contains(projection.Canonical(), "native_approval_authority") {
			t.Fatal("authority lost its explicit domain")
		}
		if _, wrongRelease, err := toolcontract.RestoreReviewedOperationInputs(t.Context(), memory, projection.Representation(), projection.Digest(), projection.Canonical()); err == nil || wrongRelease != nil {
			t.Fatal("native authority became executable operation inputs")
		}
		restored, releaseRestored, err := hitl.RestoreNativeAuthorityProjection(t.Context(), memory, projection.Representation(), projection.Digest(), projection.Canonical())
		if err != nil {
			t.Fatal(err)
		}
		options, releaseOptions, err := restored.Options(t.Context(), memory)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(options, original) || options[0].Authority[0].Sockets[0].ApprovedPath != path {
			t.Fatal("native authority path bytes/whitespace or typed metadata changed")
		}
		releaseOptions()
		releaseRestored()
		release()
		if memory.held != 0 {
			t.Fatal("native authority leases leaked")
		}
	}
}

func TestNativeAuthorityProjectionRejectsUnknownFieldsEnumsAndUnadmittedWork(t *testing.T) {
	memory := &nativeReviewMemory{}
	projection, release, err := hitl.NewNativeAuthorityProjection(t.Context(), memory, []hitl.ApprovalOption{hitl.CurrentActionOption()})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, change := range []string{"unknown field", "missing field", "unknown enum"} {
		value, releaseValue, err := toolcontract.RestoreNativeValue(t.Context(), memory, toolcontract.ApprovalAuthorityDomain, projection.Representation(), projection.Digest(), projection.Canonical())
		if err != nil {
			t.Fatal(err)
		}
		tree, releaseTree, err := value.Value(t.Context(), memory)
		if err != nil {
			t.Fatal(err)
		}
		option := tree.([]any)[0].(map[string]any)
		switch change {
		case "unknown field":
			option["unrecognized_native_field"] = true
		case "missing field":
			delete(option, "kind")
		case "unknown enum":
			option["kind"] = "invented_option_kind"
		}
		tampered, releaseTampered, err := toolcontract.NewNativeValue(t.Context(), memory, toolcontract.ApprovalAuthorityDomain, tree)
		if err != nil {
			t.Fatal(err)
		}
		if _, retained, err := hitl.RestoreNativeAuthorityProjection(t.Context(), memory, tampered.Representation(), tampered.Digest(), tampered.Canonical()); err == nil || retained != nil {
			t.Fatal("unknown native authority shape acquired meaning", change)
		}
		releaseTampered()
		releaseTree()
		releaseValue()
	}
	memory.refuse = true
	if _, retained, err := hitl.NewNativeAuthorityProjection(t.Context(), memory, []hitl.ApprovalOption{hitl.CurrentActionOption()}); err == nil || retained != nil {
		t.Fatal("native typed conversion allocated without admission")
	}
	memory.refuse = false
	release()
	if memory.held != 0 {
		t.Fatal("refused native authority binding retained state")
	}
}

// nativeAuthorityHost is one engine start over a store file: a fresh handle,
// checkpoint manager and native admission, as after a restart.
type nativeAuthorityHost struct {
	database *db.Store
	manager  *hitl.Checkpoints
	memory   *nativeReviewMemory
}

func startNativeAuthorityHost(t *testing.T, path string) nativeAuthorityHost {
	t.Helper()
	database := testdbfixture.OpenPath(t, path)
	manager := hitl.NewCheckpoints(hitl.NewSQLStore(database), &events.Publisher{Hub: events.NewMemoryHub()}, authzcontext.SQLRecorder(database))
	t.Cleanup(func() { testutil.FailErr(t, "stop checkpoint expiries", manager.StopExpiryTimers(context.Background())) })
	memory := &nativeReviewMemory{}
	manager.Authority.SetNativeAuthorityAdmission(memory)
	return nativeAuthorityHost{database: database, manager: manager, memory: memory}
}

// requestNativeSocketApproval mints a socket card for a native action through
// the actual socket offer and plan producers.
func requestNativeSocketApproval(t *testing.T, ctx context.Context, host nativeAuthorityHost, review *hitl.NativeActionReview, sessionID, path string) string {
	t.Helper()
	args, input, err := review.Freeze(ctx, "command", map[string]any{"socket": path})
	testutil.FailErr(t, "freeze native inputs", err)
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{Tool: "command", Args: args, NativeInputs: input, ActionID: "actual-model-call"},
		Scope:      hitl.ActionScope{SessionID: sessionID, RootSessionID: sessionID},
	}
	grants := []confine.SocketGrant{{ApprovedPath: path, ResolvedPath: path}}
	response, err := requestExplicitApprovalCheckpoint(t, ctx, host.manager, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval, ToolCallID: "actual-model-call", ProposedAction: &action,
		SocketCapability: &hitl.SocketCapability{Targets: []hitl.SocketCapabilityTarget{{ApprovedPath: path, ResolvedPath: path}}},
		GrantOffers:      capabilitygrants.SocketExecutionGrantOffers(action, grants),
	})
	testutil.FailErr(t, "request native socket approval", err)
	return response.CheckpointID
}

type nativeInstallRecorder struct {
	mu         sync.Mutex
	installed  []hitl.ApprovalOption
	rolledBack []hitl.ApprovalOption
	interrupt  bool
}

func (r *nativeInstallRecorder) InstallApprovalOption(_ context.Context, _ string, option hitl.ApprovalOption) (func(), error) {
	r.mu.Lock()
	r.installed = append(r.installed, option)
	interrupt := r.interrupt
	r.mu.Unlock()
	if interrupt {
		// The engine stops after authority installation, before its commit.
		runtime.Goexit()
	}
	return func() {}, nil
}

func (r *nativeInstallRecorder) RollbackApprovalOption(_ context.Context, _ string, option hitl.ApprovalOption) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rolledBack = append(r.rolledBack, option)
	return nil
}

func socketPaths(option hitl.ApprovalOption) []string {
	var paths []string
	for _, delta := range option.Authority {
		for _, socket := range delta.Sockets {
			paths = append(paths, socket.ApprovedPath, socket.ResolvedPath)
		}
	}
	return paths
}

func chatOptionID(t *testing.T, row *hitl.StoredCheckpoint) string {
	t.Helper()
	raw, err := json.Marshal(row.Payload["approval_plan"])
	testutil.FailErr(t, "encode display plan", err)
	var plan hitl.ApprovalPlan
	testutil.FailErr(t, "decode display plan", json.Unmarshal(raw, &plan))
	for _, option := range plan.Options {
		for _, delta := range option.Authority {
			if delta.Kind == hitl.AuthoritySocketChat && option.Rung == hitl.ApprovalRungChat {
				return option.ID
			}
		}
	}
	t.Fatal("actual socket producer offered no chat rung")
	return ""
}

// assertNativeExport checks that generic rows and the wire event carry only
// the screened display, never the lossless authority body.
func assertNativeExport(t *testing.T, database db.Handle, row *hitl.StoredCheckpoint, path string) {
	t.Helper()
	if row.AuthorityFormat != hitl.NativeAuthorityFormat {
		t.Fatalf("generic reader authority format = %q", row.AuthorityFormat)
	}
	var payload, body string
	testutil.FailErr(t, "read checkpoint columns", database.QueryRowContext(t.Context(), `SELECT payload_json, authority_body FROM checkpoints WHERE id = ?`, row.ID).Scan(&payload, &body))
	event, err := json.Marshal(hitl.StoredCheckpointToEvent(*row))
	testutil.FailErr(t, "encode checkpoint event", err)
	raw := base64.StdEncoding.EncodeToString([]byte(path))
	for label, exported := range map[string]string{"payload": payload, "event": string(event)} {
		if strings.Contains(exported, body) || strings.Contains(exported, "bytes_base64") || strings.Contains(exported, raw) {
			t.Fatalf("%s exported the lossless authority body", label)
		}
	}
	want := path
	if !utf8.ValidString(path) {
		want = "Bytes " + strconv.Quote(path)
	}
	var stored struct {
		Plan hitl.ApprovalPlan `json:"approval_plan"`
	}
	testutil.FailErr(t, "decode display payload", json.Unmarshal([]byte(payload), &stored))
	shown := 0
	for _, option := range stored.Plan.Options {
		for _, display := range socketPaths(option) {
			if display != want {
				t.Fatalf("display path = %q, want %q", display, want)
			}
			shown++
		}
	}
	if shown == 0 {
		t.Fatal("display plan lost its socket authority")
	}
}

func TestNativeSocketAuthoritySurvivesRestartResolutionAndChatReplay(t *testing.T) {
	for _, path := range []string{"/fixture/socket\xff ", "/fixture/socket\xfe ", "/fixture/socket� "} {
		t.Run(strconv.Quote(path), func(t *testing.T) {
			assertNativeSocketRoundTrip(t, path)
		})
	}
}

func assertNativeSocketRoundTrip(t *testing.T, path string) {
	t.Helper()
	const sessionID = "native-session"
	storePath := filepath.Join(t.TempDir(), "store.db")
	minting := startNativeAuthorityHost(t, storePath)
	insertSession(t, minting.database, sessionID)
	ctx := testdbseed.OwnerCaller(t, t.Context(), minting.database)
	review := hitl.NewNativeActionReview(ctx, minting.memory, nativeInspectionFixture{})
	checkpointID := requestNativeSocketApproval(t, ctx, minting, review, sessionID, path)
	review.Close()
	if minting.memory.held != 0 {
		t.Fatal("minting retained native authority state")
	}

	resolving := startNativeAuthorityHost(t, storePath)
	row, err := resolving.manager.Store.Get(ctx, checkpointID)
	testutil.FailErr(t, "restore checkpoint", err)
	assertNativeExport(t, resolving.database, row, path)
	installer := &nativeInstallRecorder{}
	resolving.manager.Authority.SetApprovalAuthorityInstaller(installer)
	_, err = resolving.manager.Authority.ResolveApprovalOption(ctx, sessionID, checkpointID, chatOptionID(t, row))
	testutil.FailErr(t, "resolve native approval", err)
	if len(installer.installed) != 1 {
		t.Fatalf("installed %d options", len(installer.installed))
	}
	for _, installed := range socketPaths(installer.installed[0]) {
		if installed != path {
			t.Fatalf("installed socket path %q, want exact %q", installed, path)
		}
	}
	if resolving.memory.held != 0 {
		t.Fatal("resolution retained native authority state")
	}
	var format, delta string
	testutil.FailErr(t, "read chat grant", resolving.database.QueryRowContext(ctx, `SELECT authority_format, delta_json FROM chat_grants WHERE chat_session_id = ?`, sessionID).Scan(&format, &delta))
	if format != hitl.NativeAuthorityFormat || strings.Contains(delta, base64.StdEncoding.EncodeToString([]byte(path))) {
		t.Fatal("chat grant lost its lossless format or exported its body")
	}

	replaying := startNativeAuthorityHost(t, storePath)
	replayed := &nativeInstallRecorder{}
	replaying.manager.Authority.SetApprovalAuthorityInstaller(replayed)
	testutil.FailErr(t, "replay chat grants", replaying.manager.Authority.RestoreChatGrants(ctx))
	if len(replayed.installed) != 1 || replayed.installed[0].Authority[0].Kind != hitl.AuthoritySocketChat {
		t.Fatalf("replayed %d chat grants", len(replayed.installed))
	}
	for _, installed := range socketPaths(replayed.installed[0]) {
		if installed != path {
			t.Fatalf("replayed socket path %q, want exact %q", installed, path)
		}
	}
	if replaying.memory.held != 0 {
		t.Fatal("chat grant replay retained native authority state")
	}
}

func TestNativeAuthorityRecoveryAfterInterruptedApproval(t *testing.T) {
	const sessionID, path = "native-session", "/fixture/socket\xff "
	storePath := filepath.Join(t.TempDir(), "store.db")
	host := startNativeAuthorityHost(t, storePath)
	insertSession(t, host.database, sessionID)
	ctx := testdbseed.OwnerCaller(t, t.Context(), host.database)
	review := hitl.NewNativeActionReview(ctx, host.memory, nativeInspectionFixture{})
	checkpointID := requestNativeSocketApproval(t, ctx, host, review, sessionID, path)
	review.Close()
	row, err := host.manager.Store.Get(ctx, checkpointID)
	testutil.FailErr(t, "read checkpoint", err)
	optionID := chatOptionID(t, row)
	host.manager.Authority.SetApprovalAuthorityInstaller(&nativeInstallRecorder{interrupt: true})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_, _ = host.manager.Authority.ResolveApprovalOption(ctx, sessionID, checkpointID, optionID)
	}()
	<-stopped

	restarted := startNativeAuthorityHost(t, storePath)
	var status, format string
	testutil.FailErr(t, "read interrupted operation", restarted.database.QueryRowContext(ctx, `SELECT status, authority_format FROM approval_operations WHERE checkpoint_id = ?`, checkpointID).Scan(&status, &format))
	if status != "prepared" || format != hitl.NativeAuthorityFormat {
		t.Fatalf("interrupted operation = %s/%s", status, format)
	}
	recovery := &nativeInstallRecorder{}
	restarted.manager.Authority.SetApprovalAuthorityInstaller(recovery)
	testutil.FailErr(t, "recover interrupted approval", restarted.manager.Authority.RecoverApprovalOperations(ctx))
	if len(recovery.rolledBack) != 1 {
		t.Fatalf("rolled back %d options", len(recovery.rolledBack))
	}
	for _, rolledBack := range socketPaths(recovery.rolledBack[0]) {
		if rolledBack != path {
			t.Fatalf("rolled back socket path %q, want exact %q", rolledBack, path)
		}
	}
	testutil.FailErr(t, "read recovered operation", restarted.database.QueryRowContext(ctx, `SELECT status FROM approval_operations WHERE checkpoint_id = ?`, checkpointID).Scan(&status))
	if status != "rolled_back" || restarted.memory.held != 0 {
		t.Fatalf("recovered operation = %s, retained %d", status, restarted.memory.held)
	}
}

func TestNativeAuthorityRefusesChangedOrUnadmittedRowsWithoutMutation(t *testing.T) {
	const sessionID, path = "native-session", "/fixture/socket\xff "
	storePath := filepath.Join(t.TempDir(), "store.db")
	host := startNativeAuthorityHost(t, storePath)
	insertSession(t, host.database, sessionID)
	ctx := testdbseed.OwnerCaller(t, t.Context(), host.database)
	review := hitl.NewNativeActionReview(ctx, host.memory, nativeInspectionFixture{})
	defer review.Close()
	checkpointID := requestNativeSocketApproval(t, ctx, host, review, sessionID, path)
	other := requestNativeSocketApproval(t, ctx, host, review, sessionID, "/fixture/other\xfe ")
	row, err := host.manager.Store.Get(ctx, checkpointID)
	testutil.FailErr(t, "read checkpoint", err)
	optionID := chatOptionID(t, row)
	var digest, body string
	testutil.FailErr(t, "read original authority", host.database.QueryRowContext(ctx, `SELECT authority_digest, authority_body FROM checkpoints WHERE id = ?`, checkpointID).Scan(&digest, &body))
	restore := func() {
		_, err := host.database.ExecContext(ctx, `UPDATE checkpoints SET authority_digest = ?, authority_body = ? WHERE id = ?`, digest, body, checkpointID)
		testutil.FailErr(t, "restore original authority", err)
	}
	for _, change := range []struct {
		name  string
		apply func()
	}{
		{"digest", func() {
			_, err := host.database.ExecContext(ctx, `UPDATE checkpoints SET authority_digest = ? WHERE id = ?`, strings.Repeat("0", 64), checkpointID)
			testutil.FailErr(t, "change digest", err)
		}},
		{"another approval's authority", func() {
			_, err := host.database.ExecContext(ctx, `UPDATE checkpoints SET (authority_digest, authority_body) = (SELECT authority_digest, authority_body FROM checkpoints WHERE id = ?) WHERE id = ?`, other, checkpointID)
			testutil.FailErr(t, "substitute authority", err)
		}},
		{"no admission", func() { host.manager.Authority.SetNativeAuthorityAdmission(nil) }},
		{"refused admission", func() { host.memory.refuse = true }},
	} {
		t.Run(change.name, func(t *testing.T) {
			change.apply()
			defer func() {
				restore()
				host.memory.refuse = false
				host.manager.Authority.SetNativeAuthorityAdmission(host.memory)
			}()
			installer := &nativeInstallRecorder{}
			host.manager.Authority.SetApprovalAuthorityInstaller(installer)
			if _, err := host.manager.Authority.ResolveApprovalOption(ctx, sessionID, checkpointID, optionID); err == nil {
				t.Fatal("changed native authority resolved")
			}
			var status string
			var operations int
			testutil.FailErr(t, "read refused checkpoint", host.database.QueryRowContext(ctx, `SELECT status, (SELECT count(*) FROM approval_operations WHERE checkpoint_id = checkpoints.id) FROM checkpoints WHERE id = ?`, checkpointID).Scan(&status, &operations))
			if status != "pending" || operations != 0 || len(installer.installed) != 0 {
				t.Fatalf("refusal mutated state: %s, %d operations, %d installs", status, operations, len(installer.installed))
			}
		})
	}
	if _, err := host.database.ExecContext(ctx, `UPDATE checkpoints SET authority_format = 'native_approval_authority_v2' WHERE id = ?`, checkpointID); err == nil {
		t.Fatal("store accepted an unknown native authority format")
	}
}

// A composed capability card keeps every contributing native path exact from
// mint through restart and installation.
func TestNativeCapabilityCompositionKeepsEveryRawPathThroughRestart(t *testing.T) {
	const sessionID, socket, protected = "native-session", "/fixture/socket\xff ", "/fixture/protected\xfe"
	storePath := filepath.Join(t.TempDir(), "store.db")
	host := startNativeAuthorityHost(t, storePath)
	insertSession(t, host.database, sessionID)
	ctx := testdbseed.OwnerCaller(t, t.Context(), host.database)
	review := hitl.NewNativeActionReview(ctx, host.memory, nativeInspectionFixture{})
	args, input, err := review.Freeze(ctx, "command", map[string]any{"socket": socket, "path": protected})
	testutil.FailErr(t, "freeze native inputs", err)
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{Tool: "command", Args: args, NativeInputs: input, ActionID: "actual-model-call"},
		Scope:      hitl.ActionScope{SessionID: sessionID, RootSessionID: sessionID, ProjectID: testdbseed.DefaultProjectID},
	}
	_, explicit := gate.Evaluate(gate.Facts{Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest, ApprovalRequest: &gate.ApprovalRequest{Count: 1}}, gate.DefaultPosture)
	socketRequest := hitl.CheckpointRequest{
		SessionID: sessionID, ProjectID: testdbseed.DefaultProjectID, Kind: api.CheckpointKindToolApproval, ToolCallID: "actual-model-call", ProposedAction: &action, Decision: explicit,
		SocketCapability: &hitl.SocketCapability{Targets: []hitl.SocketCapabilityTarget{{ApprovedPath: socket, ResolvedPath: socket}}},
		GrantOffers:      capabilitygrants.SocketExecutionGrantOffers(action, []confine.SocketGrant{{ApprovedPath: socket, ResolvedPath: socket}}),
	}
	socketPlan, err := hitl.CompileCheckpointApprovalPlan(socketRequest)
	testutil.FailErr(t, "compile native socket review", err)
	socketRequest.ApprovalPlan = socketPlan
	grant := hitl.ApprovalGrant{
		ID: "grant_protected", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: sessionID, ProjectID: testdbseed.DefaultProjectID,
		Predicate: hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryReadPath, Pattern: protected},
		Title:     hitl.TitleAllowForThisChat, Coverage: "protected path", ExpiresWhen: hitl.ExpiresWhenChatDeleted, ReaskWhen: "a different path is requested",
	}
	widening := &gate.Decision{Primary: api.GateCapabilityWidening, Posture: gate.PostureBalanced, Cited: []gate.Fact{{Gate: api.GateCapabilityWidening, Key: "path", Value: "protected path", Source: "host"}}}
	primary, cited, reasons := hitl.PresentDecision(widening)
	readPlan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn,
		hitl.ApprovalSubject{Kind: hitl.ApprovalSubjectReadPathSet, Title: "Allow path access", Targets: []hitl.ApprovalTarget{{Kind: "read_path", Label: "protected path"}}},
		hitl.ApprovalPresentation{Action: "Access path", Impact: "Read the protected path.", Gate: primary, Cited: cited}, reasons,
		[]hitl.ApprovalOption{hitl.GrantOption(hitl.ApprovalGrantOffer{
			ID: grant.ID, Rung: hitl.ApprovalRungChat, Scope: grant.Scope, Title: grant.Title, Coverage: grant.Coverage,
			ExpiresWhen: grant.ExpiresWhen, ReaskWhen: grant.ReaskWhen, Grant: grant,
			Authority: []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityReadPathChat, Grant: &grant, ChatSessionID: sessionID, ReadPaths: []string{protected}}},
		})}, hitl.FaceContext{})
	testutil.FailErr(t, "build native read review", err)
	readRequest := socketRequest
	readRequest.SocketCapability, readRequest.GrantOffers, readRequest.ApprovalPlan, readRequest.Decision = nil, nil, readPlan, widening
	plan, decision, err := hitl.ComposeCapabilityApprovals(action, []*hitl.PreparedApproval{{Request: socketRequest}, {Request: readRequest}})
	testutil.FailErr(t, "compose native capability reviews", err)
	response, err := host.manager.RequestCheckpoint(ctx, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval, ToolCallID: "actual-model-call", ProposedAction: &action, ApprovalPlan: plan, Decision: decision,
	})
	testutil.FailErr(t, "request composed native approval", err)
	review.Close()

	restarted := startNativeAuthorityHost(t, storePath)
	installer := &nativeInstallRecorder{}
	restarted.manager.Authority.SetApprovalAuthorityInstaller(installer)
	_, err = restarted.manager.Authority.ResolveApprovalOption(ctx, sessionID, response.CheckpointID, "capabilities_"+string(hitl.ApprovalRungChat))
	testutil.FailErr(t, "resolve composed native approval", err)
	if len(installer.installed) != 1 {
		t.Fatalf("installed %d options", len(installer.installed))
	}
	var sockets, reads []string
	for _, delta := range installer.installed[0].Authority {
		for _, target := range delta.Sockets {
			sockets = append(sockets, target.ApprovedPath, target.ResolvedPath)
		}
		reads = append(reads, delta.ReadPaths...)
	}
	if len(sockets) == 0 || len(reads) != 1 || reads[0] != protected {
		t.Fatalf("composed authority lost a contribution: sockets %q, reads %q", sockets, reads)
	}
	for _, path := range sockets {
		if path != socket {
			t.Fatalf("composed socket path %q, want exact %q", path, socket)
		}
	}
	if host.memory.held != 0 || restarted.memory.held != 0 {
		t.Fatal("composed native authority retained state")
	}
}
