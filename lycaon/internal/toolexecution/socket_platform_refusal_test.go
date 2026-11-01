//go:build linux || darwin

package toolexecution

import (
	"context"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"golang.org/x/sys/unix"
)

func TestNativeSocketNoRealizationRefusesAtCapabilitiesAndPreservesData(t *testing.T) {
	_ = socketInvocationContext(t)
	root := shortTempDir(t)
	path := filepath.Join(root, "service")
	listener := listenUnixSocket(t, path)
	grant, err := confine.ResolveSocketRequest(path)
	testutil.FailErr(t, "resolve live platform fixture", err)
	grant = retainedTestSocket(t, grant)
	store, err := settings.NewApprovalStoreAt(filepath.Join(root, "approvals.yaml"))
	testutil.FailErr(t, "platform intent store", err)
	defer store.Close()
	_, err = store.UpsertGlobalGrant(settings.ApprovalGrant{ID: "platform-intent", Scope: hitl.ApprovalGrantScopeDevice, Category: settings.ApprovalCategorySocketPath, Pattern: path, ApprovedPath: grant.ApprovedPath, ResolvedPath: grant.ResolvedPath, Binding: grant.Binding, GrantedByPersonID: testutil.HostOwner().ID, Title: "Allow local service", Coverage: path, GrantedAt: time.Now().UTC(), ExpiresWhen: "revoked", ReaskWhen: "service changes"})
	testutil.FailErr(t, "save platform intent", err)
	beforeIntent := store.GlobalGrants()
	database := testdbfixture.Open(t, "platform-secret.db")
	testdbseed.InsertSessionWithRoot(t, database, "task", testdbseed.DefaultProjectID, root)
	ctx := testdbseed.OwnerCaller(t, t.Context(), database)
	values := credentialstore.NewEmpty(credentialstore.Slot{Path: filepath.Join(t.TempDir(), credentialstore.VaultBasename), Namespace: credentialstore.NamespaceManagedSecrets, Context: "platform refusal fixture"}, func(string) bool { return true })
	service := secretcap.NewWithStore(database, values, nil)
	fingerprinter, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("h", 32)))
	testutil.FailErr(t, "platform secret fingerprinter", err)
	service.SetFingerprinter(fingerprinter)
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "platform secret matcher", err)
	matcher.SetFingerprinter(fingerprinter)
	const value = "preserved-platform-secret"
	stored, err := service.Put(ctx, secretcap.PutRequest{ProjectID: testdbseed.DefaultProjectID, SessionID: "task", OperationID: "fixture", Name: "Service credential", Purpose: "platform refusal", Scope: secretcap.ScopeProject, Origin: secretcap.OriginGenerated, Value: value, Format: secretcap.FormatBase64URL, EntropyBits: 256})
	testutil.FailErr(t, "store platform credential", err)
	registry := tools.NewDefaultRegistry()
	entered := false
	testutil.FailErr(t, "register native consumer", registry.Register("http_request", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		entered = true
		return "{}", nil
	}))
	executor := NewExecutor(nil, registry, "implement")
	executor.Capabilities.socketAdapter = &socketref.Adapter{Command: socketref.NoRealization, Native: socketref.NoRealization}
	executor.Capabilities.SetDurableSocketSource(store.SocketPathsForProject)
	executor.Secrets.SetSecretResolver(service)
	executor.Secrets.SetSecretMatcher(matcher)
	recorder := &capabilityRecorder{}
	executor.Approvals.SetAuthzRecorder(recorder)
	tc := httpRequestSocketContext(t, root)
	tc.Identity.ProjectID = testdbseed.DefaultProjectID
	tc.Identity.Agent = "implement"
	args := map[string]any{"url": "http://service.invalid/", "unix_socket": path, "auth": map[string]any{"scheme": "bearer", "token": stored.Metadata.Reference}}
	_, err = executor.Invoke(ctx, "http_request", args, tc)
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != isolation.CodeSocketPathRefused || reject.Data["state"] != "socket_realization_unavailable" || reject.Data["recovery"] != "use_approved_service_endpoint" {
		t.Fatalf("platform refusal=%v", err)
	}
	if entered {
		t.Fatal("platform with no realization dispatched native consumer")
	}
	assertSocketLedgerSource(t, recorder.records, authzledger.ActionCapabilityDenied, authzledger.AuthorizationSourcePolicy)
	for _, record := range recorder.records {
		if record.Action == authzledger.ActionCapabilityDenied && record.ResolvedBy != authzledger.ResolvedByPolicy {
			t.Fatal("platform refusal credited a human approval")
		}
	}
	if !reflect.DeepEqual(beforeIntent, store.GlobalGrants()) {
		t.Fatal("platform refusal changed saved intent")
	}
	if args["auth"].(map[string]any)["token"] != stored.Metadata.Reference {
		t.Fatal("platform refusal changed request intent")
	}
	resolved, err := service.Resolve(ctx, args, secretcap.ResolveContext{ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "task", SessionID: "task", ToolName: "http_request", ToolCallID: "read-preserved"})
	testutil.FailErr(t, "read preserved platform credential", err)
	defer resolved.Finish(ctx)
	if resolved.Arguments["auth"].(map[string]any)["token"] != value {
		t.Fatal("platform refusal discarded credential data")
	}
	raw, err := listener.(*net.UnixListener).SyscallConn()
	testutil.FailErr(t, "platform listener descriptor", err)
	var ready int
	var pollErr error
	testutil.FailErr(t, "platform listener untouched", raw.Control(func(fd uintptr) { ready, pollErr = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 0) }))
	testutil.FailErr(t, "platform listener readiness", pollErr)
	if ready != 0 {
		t.Fatal("platform refusal connected to live listener")
	}
}
