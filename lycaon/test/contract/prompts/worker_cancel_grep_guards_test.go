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
	src, err := os.ReadFile(filepath.Join(root, "lycaon", "internal", "session", "worker_graceful_cancel.go"))
	contractcheck.FailErr(t, "read worker_graceful_cancel.go", err)
	body := string(src)
	if !strings.Contains(body, "RegisterWorkerGracefulCancel(childSessionID, jobID, reason string)") {
		t.Fatalf("RegisterWorkerGracefulCancel must keep its no-ctx signature (childSessionID, jobID, reason string); a request context would let a dead request clear the registration")
	}
	if strings.Contains(body, "RegisterWorkerGracefulCancel(ctx") || strings.Contains(body, "RegisterWorkerGracefulCancel(context.") {
		t.Fatalf("RegisterWorkerGracefulCancel must not take a request context; the registration outlives the request")
	}
}
