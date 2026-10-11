package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Terminal worker status arrives only through SSE.
func TestChatActionsHasNoOptimisticTerminalPatch(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "lycaon-den", "src", "chat", "actions", "chat-actions.ts"))
	contractcheck.FailErr(t, "read chat-actions.ts", err)
	if strings.Contains(string(src), "patchWorkerStatus") {
		t.Fatalf("chat-actions.ts must not patch worker status optimistically; terminal status flows via the worker SSE topic")
	}
}

// Graceful cancellation outlives the request context.
func TestRegisterWorkerGracefulCancelTakesNoRequestContext(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "lycaon", "internal", "session", "workerresults", "graceful_cancel.go"))
	contractcheck.FailErr(t, "read workerresults/graceful_cancel.go", err)
	body := string(src)
	if !strings.Contains(body, "func (m *GracefulCancel) Register(childSessionID, jobID, reason string)") {
		t.Fatalf("GracefulCancel.Register must keep its no-ctx signature (childSessionID, jobID, reason string); a request context would let a dead request clear the registration")
	}
	if strings.Contains(body, "Register(ctx") || strings.Contains(body, "Register(context.") {
		t.Fatalf("GracefulCancel.Register must not take a request context; the registration outlives the request")
	}
	if !strings.Contains(body, "func (m *GracefulCancel) Finish(childSessionID string)") {
		t.Fatal("graceful closeout must release its registration without a request context")
	}
	caller, err := os.ReadFile(filepath.Join(root, "lycaon", "internal", "worker", "cancel_service.go"))
	contractcheck.FailErr(t, "read graceful cancellation caller", err)
	if !strings.Contains(string(caller), "s.Graceful.Register(childSessionID, task.ID, reason)") {
		t.Fatal("worker cancellation must register closeout on the request-independent graceful service")
	}
}
