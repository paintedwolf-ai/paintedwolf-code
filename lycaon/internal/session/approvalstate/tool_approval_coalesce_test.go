package approvalstate_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
)

func TestToolApprovalCoalesceMintThenJoin(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewToolApprovalCoalesce()
	chat := "chat-1"
	key := "command\x00/proj\x00{\"command\":\"echo hi\"}"

	action, id := rt.Begin(chat, key)
	if action != approvalstate.ToolApprovalCoalesceMint || id != "" {
		t.Fatalf("first Begin: action=%v id=%q", action, id)
	}
	rt.RegisterPending(chat, key, "cp-shared", "tc-a")

	action, id = rt.Begin(chat, key)
	if action != approvalstate.ToolApprovalCoalesceJoin || id != "cp-shared" {
		t.Fatalf("second Begin: action=%v id=%q", action, id)
	}
}

func TestToolApprovalCoalesceDenySet(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewToolApprovalCoalesce()
	chat := "chat-1"
	key := "command\x00/proj\x00{\"command\":\"rm -rf x\"}"

	action, _ := rt.Begin(chat, key)
	if action != approvalstate.ToolApprovalCoalesceMint {
		t.Fatalf("Begin mint: %v", action)
	}
	rt.RegisterPending(chat, key, "cp-1", "tc-1")
	rt.ClearPending(chat, key)
	rt.RecordDeny(chat, key)

	action, _ = rt.Begin(chat, key)
	if action != approvalstate.ToolApprovalCoalesceSkipDenied {
		t.Fatalf("after deny want SkipDenied, got %v", action)
	}
	rt.ClearDeny(chat, key)
	action, _ = rt.Begin(chat, key)
	if action != approvalstate.ToolApprovalCoalesceMint {
		t.Fatalf("after ClearDeny want Mint, got %v", action)
	}
}

func TestToolApprovalCoalesceAbortMint(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewToolApprovalCoalesce()
	chat := "chat-1"
	key := "command\x00/proj\x00{}"

	action, _ := rt.Begin(chat, key)
	if action != approvalstate.ToolApprovalCoalesceMint {
		t.Fatalf("Begin: %v", action)
	}
	rt.AbortMint(chat, key)
	action, _ = rt.Begin(chat, key)
	if action != approvalstate.ToolApprovalCoalesceMint {
		t.Fatalf("after AbortMint want Mint, got %v", action)
	}
}

func TestToolApprovalCoalesceDistinctActionsFromOneToolCallMintSeparately(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewToolApprovalCoalesce()
	chat := "chat-1"

	action, _ := rt.Begin(chat, "key-a")
	if action != approvalstate.ToolApprovalCoalesceMint {
		t.Fatalf("first: %v", action)
	}
	action, _ = rt.Begin(chat, "key-b")
	if action != approvalstate.ToolApprovalCoalesceMint {
		t.Fatalf("distinct exact action must mint separately, got %v", action)
	}
}

func TestToolApprovalCoalesceForgetSession(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewToolApprovalCoalesce()
	chat := "chat-1"
	key := "key-forget"

	_, _ = rt.Begin(chat, key)
	rt.RegisterPending(chat, key, "cp-1", "tc-1")
	rt.ClearPending(chat, key)
	rt.RecordDeny(chat, key)
	rt.ForgetSession(chat)

	action, _ := rt.Begin(chat, key)
	if action != approvalstate.ToolApprovalCoalesceMint {
		t.Fatalf("after ForgetSession want Mint, got %v", action)
	}
}

func TestToolApprovalCoalesceUserTurnClearsDeny(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewToolApprovalCoalesce()
	chat := "chat-1"
	deniedKey := "key-denied"
	pendingKey := "key-pending"

	_, _ = rt.Begin(chat, pendingKey)
	rt.RegisterPending(chat, pendingKey, "cp-open", "tc-pending")
	rt.RecordDeny(chat, deniedKey)

	rt.NoteUserIntentBoundary(chat)

	action, _ := rt.Begin(chat, deniedKey)
	if action != approvalstate.ToolApprovalCoalesceMint {
		t.Fatalf("user turn must clear deny-set, got %v", action)
	}
	action, id := rt.Begin(chat, pendingKey)
	if action != approvalstate.ToolApprovalCoalesceJoin || id != "cp-open" {
		t.Fatalf("pending must survive user turn: action=%v id=%q", action, id)
	}
}

func TestToolApprovalCoalesceJoinedCount(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewToolApprovalCoalesce()
	chat := "chat-1"
	key := "key-join"

	_, _ = rt.Begin(chat, key)
	rt.RegisterPending(chat, key, "cp-1", "tc-0")
	if got := rt.JoinedCount(chat, key); got != 1 {
		t.Fatalf("after mint RegisterPending JoinedCount=%d want 1", got)
	}

	for i := 1; i <= 40; i++ {
		tc := fmt.Sprintf("tc-%d", i)
		action, id := rt.Begin(chat, key)
		if action != approvalstate.ToolApprovalCoalesceJoin || id != "cp-1" {
			t.Fatalf("join %d: action=%v id=%q", i, action, id)
		}
		got := rt.NoteJoin(chat, key, tc)
		if got != i+1 {
			t.Fatalf("NoteJoin count=%d want %d", got, i+1)
		}
	}
	if got := rt.JoinedCount(chat, key); got != 41 {
		t.Fatalf("JoinedCount=%d want 41", got)
	}
	ids := rt.JoinedToolCallIDs(chat, key)
	if len(ids) != 32 {
		t.Fatalf("JoinedToolCallIDs len=%d want 32", len(ids))
	}
	if ids[0] != "tc-0" {
		t.Fatalf("first id=%q want tc-0", ids[0])
	}
}

func TestToolApprovalCoalesceConcurrentBegin(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewToolApprovalCoalesce()
	chat := "chat-1"
	key := "key-race"

	var wg sync.WaitGroup
	results := make([]approvalstate.ToolApprovalCoalesceBegin, 32)
	ids := make([]string, 32)
	wg.Add(32)
	for i := 0; i < 32; i++ {
		go func(i int) {
			defer wg.Done()
			action, id := rt.Begin(chat, key)
			results[i] = action
			ids[i] = id
			if action == approvalstate.ToolApprovalCoalesceMint {
				rt.RegisterPending(chat, key, "cp-race", fmt.Sprintf("tc-%d", i))
			}
		}(i)
	}
	wg.Wait()

	mints := 0
	joins := 0
	for i, action := range results {
		switch action {
		case approvalstate.ToolApprovalCoalesceMint:
			mints++
		case approvalstate.ToolApprovalCoalesceJoin:
			joins++
			if ids[i] != "" && ids[i] != "cp-race" {
				// A join before RegisterPending sees an empty id; after it, the registered id.
				t.Fatalf("join id=%q", ids[i])
			}
		case approvalstate.ToolApprovalCoalesceSkipDenied:
			t.Fatalf("unexpected Begin result %v at %d", action, i)
		}
	}
	if mints < 1 {
		t.Fatal("expected at least one Mint")
	}
	if mints+joins != 32 {
		t.Fatalf("mints=%d joins=%d want sum 32", mints, joins)
	}
}

func TestGrantKeyParity(t *testing.T) {
	t.Parallel()
	action := hitl.ProposedAction{
		Tool:       "command",
		ProjectDir: "/proj",
		Args:       map[string]any{"command": "aws s3 rm --recursive s3://x", "z": 1, "a": "first"},
		SessionID:  "s1",
	}
	k1 := hitl.GrantKey(action)
	k2 := hitl.GrantKey(action)
	if k1 == "" || k1 != k2 {
		t.Fatalf("GrantKey unstable: %q vs %q", k1, k2)
	}
	// Map key order leaves the fingerprint unchanged (json.Marshal sorts keys).
	reordered := hitl.ProposedAction{
		Tool:       "command",
		ProjectDir: "/proj",
		Args:       map[string]any{"a": "first", "command": "aws s3 rm --recursive s3://x", "z": 1},
		SessionID:  "s1",
	}
	if hitl.GrantKey(reordered) != k1 {
		t.Fatalf("reordered args must share GrantKey: %q vs %q", hitl.GrantKey(reordered), k1)
	}
	changed := action
	changed.Args = map[string]any{"command": "echo different"}
	if hitl.GrantKey(changed) == k1 {
		t.Fatal("different args must produce a different GrantKey")
	}
	changedBoundary := action
	changedBoundary.Contained = hitl.Contained{
		FSJailed: true,
		Egress:   hitl.ContainedEgressProxy,
		Roots:    []string{"/proj"},
	}
	if hitl.GrantKey(changedBoundary) == k1 {
		t.Fatal("different confinement must produce a different GrantKey")
	}
	// Coalesce consumes the same string the session-grant plane uses.
	rt := approvalstate.NewToolApprovalCoalesce()
	actionBegin, _ := rt.Begin("chat", hitl.GrantKey(action))
	if actionBegin != approvalstate.ToolApprovalCoalesceMint {
		t.Fatalf("Begin with GrantKey: %v", actionBegin)
	}
	rt.RegisterPending("chat", hitl.GrantKey(action), "cp-1", "tc-1")
	join, id := rt.Begin("chat", hitl.GrantKey(reordered))
	if join != approvalstate.ToolApprovalCoalesceJoin || id != "cp-1" {
		t.Fatalf("parity join failed: action=%v id=%q", join, id)
	}
}
