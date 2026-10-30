//go:build linux || darwin

package capabilityadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"golang.org/x/sys/unix"
)

// socketOwnerReview supplies the human choice; the real journal, resolver and
// installer retain every authority and transaction boundary under test.
type socketOwnerReview struct {
	*hitl.Checkpoints
	cards           int
	secretCards     int
	denySecrets     bool
	decisionContext context.Context
}

func (r *socketOwnerReview) RequestCheckpoint(ctx context.Context, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	response, err := r.Checkpoints.RequestCheckpoint(ctx, req)
	if err != nil {
		return nil, err
	}
	row, err := r.Store.Get(ctx, response.CheckpointID)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(row.Payload["approval_plan"])
	if err != nil {
		return nil, err
	}
	var plan hitl.ApprovalPlan
	if err := json.Unmarshal(encoded, &plan); err != nil {
		return nil, err
	}
	r.cards++
	var chosen string
	socketChoice := false
	hasSecret := req.SecretScreen != nil
	for _, option := range plan.Options {
		for _, delta := range option.Authority {
			if delta.Grant != nil && delta.Grant.Predicate.Category == hitl.ApprovalGrantCategorySecret {
				hasSecret = true
			}
		}
		if option.Disabled || option.DecisionAction != hitl.ApprovalOptionApprove {
			continue
		}
		if option.Rung == hitl.ApprovalRungChat && !socketChoice {
			chosen = option.ID
		}
		for _, delta := range option.Authority {
			if option.Rung == hitl.ApprovalRungChat && delta.Kind == hitl.AuthoritySocketChat {
				chosen, socketChoice = option.ID, true
			}
		}
		if chosen == "" && hasSecret && option.Rung == hitl.ApprovalRungUnchanged {
			chosen = option.ID
		}
	}
	if hasSecret {
		r.secretCards++
	}
	if hasSecret && r.denySecrets {
		return r.ResolveCheckpoint(r.decisionContext, req.SessionID, response.CheckpointID, req.Kind, &hitl.DecisionResult{Approved: false}, nil)
	}

	if chosen == "" {
		return nil, fmt.Errorf("socket owner fixture has no usable choice")
	}
	return r.Authority.ResolveApprovalOption(r.decisionContext, req.SessionID, response.CheckpointID, chosen)
}

func TestUnixSocketNativeOwnerRetainsRecipientAcrossApprovalAndReplacement(t *testing.T) {
	for _, tokens := range []bool{false, true} {
		name := "credential"
		if tokens {
			name = "issued token"
		}
		t.Run(name, func(t *testing.T) { testUnixSocketRecipientLifetime(t, tokens) })
	}
}

func testUnixSocketRecipientLifetime(t *testing.T, tokens bool) {
	root, err := os.MkdirTemp("/tmp", "sh-") //nolint:usetesting // Kernel socket addresses are bounded.
	testutil.FailErr(t, "socket owner fixture", err)
	defer os.RemoveAll(root)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(root, "state"))
	workspace := filepath.Join(root, "workspace")
	testutil.FailErr(t, "create workspace", os.Mkdir(workspace, 0700))
	path := filepath.Join(root, "service")
	original, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	testutil.FailErr(t, "original listener", err)
	original.SetUnlinkOnClose(false)
	var originalBytes atomic.Int64
	var originalTokenRequests atomic.Int64
	const issuedToken = "native-issued-token"
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		originalBytes.Add(int64(len(req.Header.Get("Authorization"))))
		if req.Header.Get("Authorization") == "Bearer "+issuedToken {
			originalTokenRequests.Add(1)
		}
		w.Header().Set("X-Service-Token", issuedToken)
		_, _ = io.WriteString(w, "original")
	})}
	go func() { _ = server.Serve(original) }()
	defer server.Close()
	database := testdbfixture.Open(t, "socket-owner.db")
	testdbseed.InsertSessionWithRoot(t, database, "root-1", testdbseed.DefaultProjectID, workspace)
	ctx := testdbseed.OwnerCaller(t, t.Context(), database)
	const secret = "socket-owner-secret-value"
	service, matcher, credentialReference := newSocketRecipientSecret(t, ctx, database, root, secret)
	approvals, err := settings.NewApprovalStoreAt(filepath.Join(root, "approvals.yaml"))
	testutil.FailErr(t, "approval store", err)
	defer approvals.Close()
	authority := settings.NewRuleApprovalGate(approvals, settings.NoSources())
	runtime := approvalstate.NewSocketCapabilityRuntime()
	defer runtime.Close()
	checkpoints := hitl.NewCheckpoints(hitl.NewSQLStore(database), &events.Publisher{Hub: events.NewMemoryHub()}, authzcontext.SQLRecorder(database))
	defer func() {
		testutil.FailErr(t, "stop socket checkpoint owners", checkpoints.StopExpiryTimers(context.Background()))
	}()
	installer := &Installation{Gate: authority, Sockets: runtime, authorityMu: &sync.Mutex{}}
	checkpoints.Authority.SetApprovalAuthorityInstaller(installer)
	review := &socketOwnerReview{Checkpoints: checkpoints, decisionContext: ctx}
	registry := tools.NewDefaultRegistry()
	executor := toolexecution.NewExecutor(nil, registry, "implement")
	executor.Approvals.SetCheckpointManager(ctx, review, authority)
	executor.Approvals.SetAuthzRecorder(authzcontext.SQLRecorder(database))
	defer confine.SetEgressResolver(nil)
	executor.Network.SetEgressPostureSource(func(string) gate.Posture { return gate.PostureStrict })
	executor.Capabilities.SetSocketCapabilityRuntime(runtime)
	executor.Secrets.SetSecretResolver(service)
	executor.Secrets.SetSecretMatcher(matcher)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "implement", Tools: map[string]bool{"http_request": true}}})
	var decoy *net.UnixListener
	swapped := false
	testutil.FailErr(t, "register retained HTTP consumer", httpaction.Register(registry, httpaction.Deps{Boundary: boundary, Secrets: service, SecretMatcher: matcher, SecretAsk: func(ctx context.Context, alert secretmatch.Alert) (secretmatch.Resolution, error) {
		if !swapped {
			// ClaimDeclaredSocket has consumed the real permit before this final screen.
			if err := os.Rename(path, path+"-original"); err != nil {
				return secretmatch.Resolution{}, err
			}
			var err error
			decoy, err = net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				return secretmatch.Resolution{}, err
			}
			decoy.SetUnlinkOnClose(false)
			t.Cleanup(func() { _ = decoy.Close() })
			swapped = true
		}
		return executor.Secrets.AskSecretScreen(ctx, alert)
	}}))
	args := map[string]any{"url": "http://service.invalid/info", "unix_socket": path, "auth": map[string]any{"scheme": "bearer", "token": credentialReference}, "token_jar": "service-tokens", "capture_tokens": []any{map[string]any{"name": "api", "from": "header:X-Service-Token"}}}
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "root-1", ProjectID: testdbseed.DefaultProjectID, ToolCallID: "first", Agent: "implement"}, Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: workspace, IsPrimary: true}}, ActiveRootID: "root"}}
	_, err = executor.Invoke(ctx, "http_request", args, tc)
	testutil.FailErr(t, "send to retained original", err)
	if !swapped || originalBytes.Load() != int64(len("Bearer "+secret)) {
		t.Fatal("original recipient did not receive exactly the reviewed credential")
	}
	assertNoUnixAccept(t, decoy)
	firstSecretCards := review.secretCards
	if firstSecretCards == 0 {
		t.Fatal("credential was sent without real secret approval")
	}
	testutil.FailErr(t, "move decoy aside", os.Rename(path, path+"-unused"))
	testutil.FailErr(t, "restore original name", os.Rename(path+"-original", path))
	tc.Identity.ToolCallID = "reuse"
	_, err = executor.Invoke(ctx, "http_request", args, tc)
	testutil.FailErr(t, "reuse same live recipient", err)
	if review.secretCards != firstSecretCards {
		t.Fatal("same live binding did not reuse recipient permission")
	}
	jarRequest := secretcap.TokenJarRequest{ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1", OperationID: "inspect", Name: "service-tokens"}
	jar, err := service.OpenTokenJar(ctx, jarRequest)
	testutil.FailErr(t, "read retained token jar", err)
	originalToken := jar.Tokens["api"]
	if originalToken.Value != issuedToken || originalToken.Origin == "" {
		t.Fatal("original listener token was not captured with recipient identity")
	}
	if tokens {
		args = map[string]any{"url": "http://service.invalid/info", "unix_socket": path, "token_jar": "service-tokens", "headers": []any{map[string]any{"name": "Authorization", "value": "Bearer {{token:api}}"}}}
		tc.Identity.ToolCallID = "same-issuer-token"
		_, err = executor.Invoke(ctx, "http_request", args, tc)
		testutil.FailErr(t, "place token at same retained issuer", err)
		if originalTokenRequests.Load() != 1 || review.secretCards != firstSecretCards {
			t.Fatal("same retained issuer did not reuse its token without disclosure review")
		}
	}
	testutil.FailErr(t, "stop original daemon", server.Close())
	testutil.FailErr(t, "remove stopped listener name", os.Remove(path))
	testutil.FailErr(t, "move decoy listener into approved name", os.Rename(path+"-unused", path))
	review.denySecrets = true
	tc.Identity.ToolCallID = "replacement"
	_, err = executor.Invoke(ctx, "http_request", args, tc)
	reject := toolrejection.AsToolReject(err)
	if reject == nil {
		t.Fatalf("changed recipient sent without fresh approval: %v", err)
	}
	if review.secretCards <= firstSecretCards {
		t.Fatal("changed socket object reused old recipient authority")
	}
	assertNoUnixAccept(t, decoy)
	assertSocketCredentialPreserved(t, ctx, service, credentialReference)
	preserved, err := service.OpenTokenJar(ctx, jarRequest)
	testutil.FailErr(t, "preserve original token jar", err)
	if preserved.Tokens["api"] != originalToken {
		t.Fatal("changed recipient rewrote or discarded token data")
	}
	if tokens {
		runtime.Close()
		testutil.FailErr(t, "stop old checkpoint owners", checkpoints.StopExpiryTimers(context.Background()))
		restartedRuntime := approvalstate.NewSocketCapabilityRuntime()
		defer restartedRuntime.Close()
		restarted := hitl.NewCheckpoints(hitl.NewSQLStore(database), &events.Publisher{Hub: events.NewMemoryHub()}, authzcontext.SQLRecorder(database))
		defer func() {
			testutil.FailErr(t, "stop restarted token checkpoint owners", restarted.StopExpiryTimers(context.Background()))
		}()
		installer.Sockets = restartedRuntime
		restarted.Authority.SetApprovalAuthorityInstaller(installer)
		review.Checkpoints = restarted
		executor.Capabilities.SetSocketCapabilityRuntime(restartedRuntime)
		executor.Approvals.SetCheckpointManager(ctx, review, authority)
		testutil.FailErr(t, "restore unavailable socket intent", restarted.Authority.RestoreChatGrants(ctx))
		if len(restartedRuntime.ListChatGrants("root-1")) == 0 || len(restartedRuntime.AppliedGrants("root-1")) != 0 {
			t.Fatal("restart lost socket intent or restored stale authority")
		}
		beforeRestartReview := review.secretCards
		tc.Identity.ToolCallID = "token-after-engine-restart"
		_, err = executor.Invoke(ctx, "http_request", args, tc)
		if toolrejection.AsToolReject(err) == nil || review.secretCards <= beforeRestartReview {
			t.Fatalf("restart reused stale token recipient authority: %v", err)
		}
		assertNoUnixAccept(t, decoy)
		restored, err := service.OpenTokenJar(ctx, jarRequest)
		testutil.FailErr(t, "token data survives restart", err)
		if restored.Tokens["api"] != originalToken {
			t.Fatal("engine restart discarded or rebound token data")
		}
	}
}

func assertNoUnixAccept(t *testing.T, listener *net.UnixListener) {
	t.Helper()
	if listener == nil {
		t.Fatal("missing decoy fixture")
	}
	raw, err := listener.SyscallConn()
	testutil.FailErr(t, "decoy raw connection", err)
	var ready int
	var pollErr error
	testutil.FailErr(t, "inspect decoy readiness", raw.Control(func(fd uintptr) { ready, pollErr = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 0) }))
	testutil.FailErr(t, "poll decoy readiness", pollErr)
	if ready != 0 {
		t.Fatal("decoy accepted or queued a connection")
	}
}

func newSocketRecipientSecret(t *testing.T, ctx context.Context, database db.Handle, root, secret string) (*secretcap.Service, *secretmatch.Matcher, string) {
	t.Helper()
	values := credentialstore.NewEmpty(credentialstore.Slot{Path: filepath.Join(root, credentialstore.VaultBasename), Namespace: credentialstore.NamespaceManagedSecrets, Context: "socket owner fixture"}, func(string) bool { return true })
	service := secretcap.NewWithStore(database, values, nil)
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "socket matcher", err)
	fingerprinter, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("h", 32)))
	testutil.FailErr(t, "socket fingerprinter", err)
	matcher.SetFingerprinter(fingerprinter)
	service.SetFingerprinter(fingerprinter)
	stored, err := service.Put(ctx, secretcap.PutRequest{ProjectID: testdbseed.DefaultProjectID, SessionID: "root-1", OperationID: "fixture", Name: "Service credential", Purpose: "socket owner fixture", Scope: secretcap.ScopeProject, Origin: secretcap.OriginGenerated, Value: secret, Format: secretcap.FormatBase64URL, EntropyBits: 256})
	testutil.FailErr(t, "store credential", err)
	return service, matcher, stored.Metadata.Reference
}

func assertSocketCredentialPreserved(t *testing.T, ctx context.Context, service *secretcap.Service, reference string) {
	t.Helper()
	metadata, err := service.ListProject(ctx, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "preserve stored credential", err)
	preservedCredential := false
	for _, meta := range metadata {
		if meta.Reference == reference {
			preservedCredential = true
		}
	}
	if !preservedCredential {
		t.Fatal("recipient refusal discarded stored credential")
	}
}
