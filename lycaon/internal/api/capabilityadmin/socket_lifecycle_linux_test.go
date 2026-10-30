//go:build linux

package capabilityadmin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func socketLifecycleFixture(t *testing.T) (*Handler, confine.SocketGrant) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "sl-") //nolint:usetesting // Kernel socket addresses are bounded.
	testutil.FailErr(t, "create socket directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	path := filepath.Join(root, "service")
	listener, err := net.Listen("unix", path)
	testutil.FailErr(t, "listen on socket", err)
	t.Cleanup(func() { _ = listener.Close() })
	resolved, err := confine.ResolveSocketRequest(path)
	testutil.FailErr(t, "resolve socket", err)
	owner, err := socketref.Capture(path)
	testutil.FailErr(t, "retain exact socket", err)
	t.Cleanup(func() { owner.Close() })
	resolved.Binding = owner.Binding()
	saved, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "create approval store", err)
	runtime := approvalstate.NewSocketCapabilityRuntime()
	t.Cleanup(runtime.Close)
	handler := New(&httpio.Responder{Logger: slog.Default()}, withHost(t, Deps{Settings: &settings.Service{Approvals: saved}, Gate: settings.NewRuleApprovalGate(saved, settings.NoSources()), Authority: Authority{Sockets: runtime}}))
	return &handler, resolved
}

func TestSocketSettingsCreationPreservesCapturedReference(t *testing.T) {
	handler, target := socketLifecycleFixture(t)
	body, err := json.Marshal(wire.CreateApprovalGrantRequest{Scope: wire.ApprovalGrantScopeDevice, Category: wire.ApprovalGrantCategorySocketPath, SocketPath: target.ApprovedPath})
	testutil.FailErr(t, "encode settings socket grant", err)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/approval-grants", strings.NewReader(string(body))).WithContext(people.WithCaller(t.Context(), testutil.HostOwner()))
	request.Header.Set("Content-Type", "application/json")
	handler.Grants.HandleCreateApprovalGrant(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create socket grant: %d %s", response.Code, response.Body.String())
	}
	var created wire.ApprovalGrant
	testutil.FailErr(t, "decode created grant", json.Unmarshal(response.Body.Bytes(), &created))
	grants := handler.Grants.Settings.Approvals.GlobalGrants()
	if created.ID == "" || created.ApprovedPath != target.ApprovedPath || len(grants) != 1 || grants[0].Binding.Mode() != socketref.RetainedObject || grants[0].Binding.ID == "" || grants[0].ResolvedPath != target.ResolvedPath {
		t.Fatalf("settings grant lost exact target: response=%+v saved=%+v", created, grants)
	}
	mapped := socketGrantToDomain(grants[0])
	if mapped.Binding.Key() != grants[0].Binding.Key() || mapped.ApprovedPath != target.ApprovedPath {
		t.Fatal("inventory changed socket authority")
	}
}

func TestSocketRestartIntentIsVisibleButNeverExecutable(t *testing.T) {
	handler, target := socketLifecycleFixture(t)
	now := time.Now().UTC()
	saved := hitl.ChatGrant{ID: "saved", CheckpointID: "checkpoint-old", Delta: hitl.ApprovalAuthorityDelta{Kind: hitl.AuthoritySocketChat, ChatSessionID: "chat", Grant: &hitl.ApprovalGrant{ID: "saved", GrantedAt: now}, Sockets: []hitl.ApprovalSocketTarget{{ApprovedPath: target.ApprovedPath, ResolvedPath: target.ResolvedPath, Binding: target.Binding}}}}
	testutil.FailErr(t, "restore reviewable intent", handler.Installation.RestoreUnavailableSocketIntent(t.Context(), saved))
	if len(handler.Installation.Sockets.AppliedGrants("chat")) != 0 {
		t.Fatal("restored intent minted live authority")
	}
	rows := chatSocketGrantsToDomain("chat", handler.Installation.Sockets.ListChatGrants("chat"))
	if len(rows) != 1 || !rows[0].Unavailable || rows[0].Binding.Key() != target.Binding.Key() {
		t.Fatalf("review history lost exact unavailable target: %+v", rows)
	}
	other := saved
	other.Delta.ChatSessionID = "other-chat"
	if err := handler.Installation.RestoreUnavailableSocketIntent(t.Context(), other); err == nil {
		t.Fatal("restored another chat over an owned id")
	}
	invalid := saved
	invalid.Delta.Grant = nil
	if err := handler.Installation.RestoreUnavailableSocketIntent(t.Context(), invalid); err == nil {
		t.Fatal("accepted incomplete restored authority")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := handler.Installation.RestoreUnavailableSocketIntent(ctx, saved); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled restoration changed state: %v", err)
	}
	saved.Delta.TTLSeconds = 3600
	undo, err := handler.Installation.InstallApprovalOption(t.Context(), "checkpoint-new", hitl.ApprovalOption{Authority: []hitl.ApprovalAuthorityDelta{saved.Delta}})
	testutil.FailErr(t, "fresh approval replaces unavailable intent", err)
	if len(handler.Installation.Sockets.AppliedGrants("chat")) != 1 {
		t.Fatal("fresh approval failed to retain target")
	}
	liveRows := chatSocketGrantsToDomain("chat", handler.Installation.Sockets.ListChatGrants("chat"))
	if len(liveRows) != 1 || liveRows[0].Unavailable || liveRows[0].ExpiresAt == nil {
		t.Fatal("fresh time-bounded grant lost its live inventory projection")
	}
	undo()
	if len(handler.Installation.Sockets.AppliedGrants("chat")) != 0 {
		t.Fatal("rollback left executable socket authority")
	}
}

func TestSocketPermitOwnershipAndRetiredReferenceRefusal(t *testing.T) {
	handler, target := socketLifecycleFixture(t)
	requested := []confine.SocketGrant{target}
	delta := hitl.ApprovalAuthorityDelta{Kind: hitl.AuthoritySocketPermit, SessionID: "operation-session", ToolCallID: "call", ActionDigest: "action", Sockets: []hitl.ApprovalSocketTarget{{ApprovedPath: target.ApprovedPath, ResolvedPath: target.ResolvedPath, Binding: target.Binding}}}
	option := hitl.ApprovalOption{Authority: []hitl.ApprovalAuthorityDelta{delta}}
	undo, err := handler.Installation.InstallApprovalOption(t.Context(), "checkpoint", option)
	testutil.FailErr(t, "install operation-bound permit", err)
	if len(handler.Installation.Sockets.AuthorizedGrants("", delta.SessionID, delta.ToolCallID, delta.ActionDigest, requested)) != 1 {
		t.Fatal("installed permit did not authorize its operation")
	}
	if len(handler.Installation.Sockets.AuthorizedGrants("", delta.SessionID, "different-call", delta.ActionDigest, requested)) != 0 {
		t.Fatal("permit escaped its call identity")
	}
	duplicateUndo, err := handler.Installation.InstallApprovalOption(t.Context(), "other-checkpoint", option)
	testutil.FailErr(t, "observe already-owned permit", err)
	duplicateUndo()
	if len(handler.Installation.Sockets.AuthorizedGrants("", delta.SessionID, delta.ToolCallID, delta.ActionDigest, requested)) != 1 {
		t.Fatal("duplicate installation stole the existing permit lifetime")
	}
	testutil.FailErr(t, "recover original operation", handler.Installation.RollbackApprovalOption(t.Context(), "checkpoint", option))
	if len(handler.Installation.Sockets.AuthorizedGrants("", delta.SessionID, delta.ToolCallID, delta.ActionDigest, requested)) != 0 {
		t.Fatal("rollback left an operation permit installed")
	}
	undo()
	retired, err := socketref.Capture(target.ApprovedPath)
	testutil.FailErr(t, "capture subsequently retired target", err)
	binding := retired.Binding()
	retired.Close()
	delta.Sockets = []hitl.ApprovalSocketTarget{{ApprovedPath: target.ApprovedPath, ResolvedPath: target.ResolvedPath, Binding: binding}}
	if _, err := handler.Installation.InstallApprovalOption(t.Context(), "retired-permit", hitl.ApprovalOption{Authority: []hitl.ApprovalAuthorityDelta{delta}}); err == nil {
		t.Fatal("retired one-use authority was recaptured by pathname")
	}
	delta.Kind = hitl.AuthoritySocketChat
	delta.ChatSessionID = "chat"
	delta.Grant = &hitl.ApprovalGrant{ID: "retired-chat"}
	if _, err := handler.Installation.InstallApprovalOption(t.Context(), "retired-chat", hitl.ApprovalOption{Authority: []hitl.ApprovalAuthorityDelta{delta}}); err == nil {
		t.Fatal("retired chat authority was recaptured by pathname")
	}
}
