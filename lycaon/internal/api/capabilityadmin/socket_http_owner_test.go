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
	hasSecret := false
	for _, option := range plan.Options {
		for _, delta := range option.Authority {
			if delta.Grant != nil && delta.Grant.Predicate.Category == hitl.ApprovalGrantCategorySecret {
				hasSecret = true
			}
		}
		if !option.Disabled && option.Rung == hitl.ApprovalRungChat && option.DecisionAction == hitl.ApprovalOptionApprove {
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
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		originalBytes.Add(int64(len(req.Header.Get("Authorization"))))
		_, _ = io.WriteString(w, "original")
	})}
	go func() { _ = server.Serve(original) }()
	defer server.Close()
	database := testdbfixture.Open(t, "socket-owner.db")
	testdbseed.InsertSessionWithRoot(t, database, "root-1", testdbseed.DefaultProjectID, workspace)
	ctx := testdbseed.OwnerCaller(t, t.Context(), database)
	values := credentialstore.NewEmpty(credentialstore.Slot{Path: filepath.Join(root, credentialstore.VaultBasename), Namespace: credentialstore.NamespaceManagedSecrets, Context: "socket owner fixture"}, func(string) bool { return true })
	service := secretcap.NewWithStore(database, values, nil)
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "socket matcher", err)
	fingerprinter, err := secretmatch.NewFingerprinter([]byte(strings.Repeat("h", 32)))
	testutil.FailErr(t, "socket fingerprinter", err)
	matcher.SetFingerprinter(fingerprinter)
	service.SetFingerprinter(fingerprinter)
	const secret = "socket-owner-secret-value"
	stored, err := service.Put(ctx, secretcap.PutRequest{ProjectID: testdbseed.DefaultProjectID, SessionID: "root-1", OperationID: "fixture", Name: "Service credential", Purpose: "socket owner fixture", Scope: secretcap.ScopeProject, Origin: secretcap.OriginGenerated, Value: secret, Format: secretcap.FormatBase64URL, EntropyBits: 256})
	testutil.FailErr(t, "store credential", err)
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
	args := map[string]any{"url": "http://service.invalid/info", "unix_socket": path, "auth": map[string]any{"scheme": "bearer", "token": stored.Metadata.Reference}}
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
	metadata, err := service.ListProject(ctx, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "preserve stored credential", err)
	if len(metadata) != 1 || metadata[0].Reference != stored.Metadata.Reference {
		t.Fatal("recipient refusal discarded stored credential")
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
