package toolvocabtest_test

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
)

func TestSchemaDiffCoversAllDynamicProjections(t *testing.T) {
	idx, err := toolvocabtest.DiscoverPrunableSchemaIndex(t.Context(), nil)
	testutil.FailErr(t, "discover prunable schema index", err)

	wantCommandTop := []string{
		"capability_request",
		"secret_use",
		"snapshot_capture",
		"socks_proxy",
		"terminal_capture",
		"verification",
	}
	for _, field := range wantCommandTop {
		if !idx.IsPrunablePath("command", field) {
			t.Fatalf("expected command.%s to be discovered as prunable, got %v", field, idx.PrunablePaths("command"))
		}
		if !slices.Contains(idx.PrunableOwners(field), "command") {
			t.Fatalf("expected command in PrunableOwners(%q), got %v", field, idx.PrunableOwners(field))
		}
	}
	for _, nested := range []string{
		"capability_request.host_resources",
		"capability_request.local_listen",
		"capability_request.loopback_connect",
		"capability_request.write_root",
		"secret_use.services",
		"snapshot_capture.env_var",
		"terminal_capture.winsize.cols",
	} {
		if !idx.IsPrunablePath("command", nested) {
			t.Fatalf("expected nested command.%s to be discovered as prunable", nested)
		}
	}
	if idx.IsPrunablePath("command", "command") || idx.IsPrunablePath("command", "cwd") {
		t.Fatalf("core command fields must not be marked prunable: %v", idx.PrunablePaths("command"))
	}

	if !idx.IsPrunablePath("ask_user", "secret") || !idx.IsPrunablePath("ask_user", "secret.name") {
		t.Fatalf("expected ask_user.secret and ask_user.secret.name to be discovered as prunable, got %v", idx.PrunablePaths("ask_user"))
	}
	if !idx.IsPrunableEnum("ask_user", "response_type", "secret") {
		t.Fatalf("expected (ask_user, response_type, secret) enum narrowing to be discovered, got %v", idx.PrunableEnums("ask_user", "response_type"))
	}
	if idx.IsPrunableEnum("ask_user", "response_type", "text") {
		t.Fatalf("ask_user response_type=text is never narrowed out")
	}

	if !idx.IsPrunablePath("wait", "conditions") || !idx.IsPrunablePath("wait", "conditions.kind") {
		t.Fatalf("expected wait.conditions and wait.conditions.kind to be discovered as prunable, got %v", idx.PrunablePaths("wait"))
	}
	for _, kind := range []string{
		"next_worker_done",
		"all_workers_idle",
		"overlay_promote_pending",
		"scan_done",
		"process_done",
		"http_ready",
		"port_ready",
	} {
		if !idx.IsPrunableEnum("wait", "conditions.kind", kind) {
			t.Fatalf("expected (wait, conditions.kind, %q) enum narrowing to be discovered, got %v", kind, idx.PrunableEnums("wait", "conditions.kind"))
		}
	}
}
