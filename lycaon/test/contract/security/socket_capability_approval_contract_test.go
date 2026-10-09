package contract

import (
	"github.com/lycaon/lycaon/internal/capabilitygrants"

	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Guards structured exact-socket capability request/approval contracts.

// assertNoStandingSocketGrant is the shared invariant behind both socket scope
// guards: the execution card may widen how long an answer lasts, never whether it
// expires. Every offer it mints is either task-bound or time-boxed, and none is
// device-scoped — so no answer to "connect to this daemon" outlives review.
func assertNoStandingSocketGrant(t *testing.T) {
	t.Helper()
	action := hitl.ProposedAction{Tool: "command", SessionID: "sess", ProjectID: "proj", ProjectDir: "/tmp/proj"}
	grant := confine.SocketGrant{ApprovedPath: "/tmp/svc.sock", ResolvedPath: "/private/tmp/svc.sock"}
	offers := capabilitygrants.SocketExecutionGrantOffers(action, []confine.SocketGrant{grant})
	if len(offers) == 0 {
		t.Fatal("socket execution card offered no ladder")
	}
	for _, offer := range offers {
		if offer.Scope == hitl.ApprovalGrantScopeDevice {
			t.Fatalf("socket execution card offered device scope: %+v", offer)
		}
		if offer.Scope != hitl.ApprovalGrantScopeChat && offer.TTLSeconds <= 0 && offer.Grant.ExpiresAt == nil {
			t.Fatalf("socket execution card offered a standing grant beyond the task: %+v", offer)
		}
	}
}

func TestContractSocketCapabilityRequestNotGrant(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	commandRel := "lycaon/config/packs/painted-wolf/platform/tools/schemas/command.yaml"
	termRel := "lycaon/config/packs/painted-wolf/platform/tools/schemas/terminal_open.yaml"
	for _, rel := range []string{commandRel, termRel} {
		src := contractcheck.ReadRepoFile(t, root, rel)
		if !strings.Contains(src, "capability_request") {
			t.Fatalf("%s must declare capability_request", rel)
		}
		if !strings.Contains(src, "Declares confinement this invocation needs") {
			t.Fatalf("%s capability_request must describe a confinement declaration", rel)
		}
		for _, banned := range []string{"Requests human approval", "approval card", "does not grant access"} {
			if strings.Contains(src, banned) {
				t.Fatalf("%s capability_request must not teach the approval system (%q)", rel, banned)
			}
		}
	}
	command := contractcheck.ReadRepoFile(t, root, commandRel)
	if !strings.Contains(command, "direct_ip") {
		t.Fatalf("%s must expose direct_ip in capability_request", commandRel)
	}
	if !strings.Contains(command, "Actual destinations will be unobserved") {
		t.Fatalf("%s direct_ip must state destinations are unobserved", commandRel)
	}
	term := contractcheck.ReadRepoFile(t, root, termRel)
	if strings.Contains(term, "direct_ip") {
		t.Fatalf("%s must not expose direct_ip in capability_request", termRel)
	}
}

func TestContractNoSkipSubstrateApprovalBoolean(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	// Split so this test file does not match its own needle.
	banned := "skipSubstrate" + "Approval"
	err := filepath.WalkDir(filepath.Join(root, "lycaon"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.Contains(path, string(filepath.Separator)+"testdata"+string(filepath.Separator)) {
			return nil
		}
		if strings.HasSuffix(path, "socket_capability_approval_contract_test.go") {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(b), banned) {
			t.Errorf("%s must not introduce %s", path, banned)
		}
		return nil
	})
	contractcheck.FailErr(t, "walk lycaon", err)
}

func TestContractSocketCapabilityCardScopes(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	src := socketCapabilitySources(t, root)
	if !strings.Contains(src, "hitl.TitleAllowForThisChat") {
		t.Fatal("socket cards must offer chat scope")
	}
	if strings.Contains(src, "ApprovalGrantScopeDevice") {
		t.Fatal("socket execution cards must not offer device scope")
	}
	// Project scope is allowed only time-boxed, so no grant outlives observation.
	assertNoStandingSocketGrant(t)
}

func TestContractSocketPermitIsBoundedRuntimeState(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	src := contractcheck.ReadRepoFile(t, root, "lycaon/internal/session/approvalstate/socket_capability_runtime.go")
	if !strings.Contains(src, "scopedstore.LRU[*socketPermitSlot]") || !strings.Contains(src, "LoadAndDelete") {
		t.Fatal("socket permits must be bounded and deleted when consumed")
	}
}
