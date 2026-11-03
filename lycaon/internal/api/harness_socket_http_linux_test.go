//go:build linux

package api

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestHarnessSocketHTTPUsesActualTargetAndRejectsChangedIdentity(t *testing.T) {
	t.Setenv(configdir.EnvDev, "1")
	t.Setenv(configdir.EnvHarness, "1")
	root, err := os.MkdirTemp("/tmp", "hsc-") //nolint:usetesting // Kernel socket addresses are bounded.
	testutil.FailErr(t, "create socket directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	path := filepath.Join(root, "service")
	listener, err := net.Listen("unix", path)
	testutil.FailErr(t, "listen on actual service", err)
	t.Cleanup(func() { _ = listener.Close() })
	projects := project.NewMemoryRegistry()
	p, err := projects.Create(t.Context(), project.CreateParams{Draft: true, Name: "Socket approval fixture"})
	testutil.FailErr(t, "create project", err)
	sessions := store.NewMemory()
	chat, err := sessions.Create(t.Context(), wire.CreateSessionRequest{}, p.ID)
	testutil.FailErr(t, "create chat", err)
	deps := requiredTestDeps(t, Dependencies{Core: CoreDependencies{Projects: projects, Store: sessions}})
	testdbseed.InsertSession(t, deps.Core.Database, chat.ID, p.ID)
	server := NewServer(deps, nil, "harness-test-token")
	for _, tc := range []struct {
		name, approved, resolved string
		code                     wire.ApiErrorCode
	}{
		{name: "actual retained target", approved: path},
		{name: "changed identity", approved: path, resolved: path + "-changed", code: confine.SocketResolveChanged},
		{name: "missing target", approved: path + "-missing", code: confine.SocketResolveNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(harnessToolApprovalReq{SessionID: chat.ID, ApprovedPath: tc.approved, ResolvedPath: tc.resolved, Command: "printf fixture"})
			testutil.FailErr(t, "encode socket card request", err)
			result := manualHarnessRequest(server, http.MethodPost, "/harness/checkpoints/tool_approval", string(body), true)
			if tc.code != "" {
				if result.Code < 400 || !strings.Contains(result.Body.String(), string(tc.code)) {
					t.Fatalf("socket refusal lost its typed cause: %d %s", result.Code, result.Body.String())
				}
				return
			}
			var card struct {
				CheckpointID string `json:"checkpoint_id"`
			}
			testutil.FailErr(t, "decode actual pending card", json.Unmarshal(result.Body.Bytes(), &card))
			if result.Code != http.StatusOK || card.CheckpointID == "" {
				t.Fatalf("socket card not created: %d %s", result.Code, result.Body.String())
			}
		})
	}
}

func TestHarnessSocketCapabilityEmptyAndResolvedPathContracts(t *testing.T) {
	applyHarnessSocketCapability(nil, confine.SocketGrant{ApprovedPath: "/fixture"}, "", "")
	request := hitl.CheckpointRequest{ProposedAction: &hitl.ProposedAction{}}
	applyHarnessSocketCapability(&request, confine.SocketGrant{}, "", "")
	if request.SocketCapability != nil {
		t.Fatal("empty target created authority")
	}
	applyHarnessSocketCapability(&request, confine.SocketGrant{ApprovedPath: "/fixture"}, "", "")
	if request.SocketCapability == nil || request.SocketCapability.Targets[0].ResolvedPath != "/fixture" || request.ProposedAction.Sockets.SocketGrants[0].ResolvedPath != "/fixture" {
		t.Fatal("display and action targets diverged")
	}
}
