package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const sweepSecret = "67ff6e39282cb4d81f8da08b44df3e8b524a5960"

// growingRedactor models the evidence base: it knows nothing until told, and
// once told it redacts the value everywhere.
type growingRedactor struct{ known bool }

func (g *growingRedactor) redact(_ context.Context, msg api.Message) (api.Message, bool) {
	if !g.known || !strings.Contains(msg.Content, sweepSecret) {
		return msg, false
	}
	out := msg
	out.Content = strings.ReplaceAll(msg.Content, sweepSecret, "[REDACTED]")
	out.HostSecretRedaction = api.NewHostSecretRedactionMeta([]api.RedactedSpan{{
		Field: "content", Start: strings.Index(out.Content, "[REDACTED]"), Length: 10,
		Kind: api.RedactionKindSecret, Source: api.RedactionSourceRememberedMatch,
	}})
	return out, true
}

// Sweeping removes newly recognized secrets from existing transcript rows.
func TestSweepRewritesRowsScreenedBeforeTheEvidenceGrew(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	evidence := &growingRedactor{}
	mgr.Runner.Transcript.SetRedactor(evidence.redact)
	sess, err := mem.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	// The minting call lands while nothing recognizes the value.
	mint := api.Message{
		ID: "tool-1", Role: api.MessageRoleTool,
		Content: "Access token was successfully created: " + sweepSecret,
	}
	testutil.FailErr(t, "append mint row", mgr.Runner.Transcript.Append(ctx, sess.ID, mint))

	stored, err := mem.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read messages", err)
	if !strings.Contains(stored[0].Content, sweepSecret) {
		t.Fatal("precondition failed: the row was redacted before the evidence existed")
	}

	// The same value is confirmed somewhere else, so the base grows.
	evidence.known = true
	mgr.Runner.Transcript.SweepSessionTree(ctx, sess.ID, 1)

	swept, err := mem.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read swept messages", err)
	if strings.Contains(swept[0].Content, sweepSecret) {
		t.Fatalf("the minting row still holds the value after the host learned it:\n%s", swept[0].Content)
	}
	if swept[0].HostSecretRedaction == nil || swept[0].HostSecretRedaction.Occurrences() != 1 {
		t.Fatalf("swept row carries no provenance: %+v", swept[0].HostSecretRedaction)
	}
	if swept[0].Seq <= stored[0].Seq {
		t.Errorf("seq did not advance (%d -> %d); a client would keep the stale row",
			stored[0].Seq, swept[0].Seq)
	}
}

// A second sweep at the same revision rewrites nothing: the stamp keeps a
// growing evidence base from re-walking settled history.
func TestSweepSkipsRowsAlreadyStampedAtTheGeneration(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	evidence := &growingRedactor{known: true}
	mgr.Runner.Transcript.SetRedactor(evidence.redact)
	sess, err := mem.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	testutil.FailErr(t, "append row", mgr.Runner.Transcript.Append(ctx, sess.ID, api.Message{
		ID: "tool-1", Role: api.MessageRoleTool, Content: "token " + sweepSecret,
	}))
	mgr.Runner.Transcript.SweepSessionTree(ctx, sess.ID, 1)

	afterFirst, err := mem.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read after first sweep", err)
	mgr.Runner.Transcript.SweepSessionTree(ctx, sess.ID, 1)
	afterSecond, err := mem.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read after second sweep", err)

	if afterFirst[0].Seq != afterSecond[0].Seq {
		t.Errorf("a repeat sweep at the same generation rewrote the row (seq %d -> %d)",
			afterFirst[0].Seq, afterSecond[0].Seq)
	}
}

func TestSweepRevisitsRowsStampedByAnEarlierRun(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	blind := &growingRedactor{}
	first := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	first.Transcript.SetRedactor(blind.redact)
	testutil.FailErr(t, "append mint row", first.Transcript.Append(ctx, sess.ID, api.Message{
		ID: "tool-1", Role: api.MessageRoleTool, Content: "token " + sweepSecret,
	}))
	// Build a persisted floor without recognizing this value.
	for revision := uint64(1); revision <= 3; revision++ {
		first.Transcript.SweepSessionTree(ctx, sess.ID, revision)
	}

	// Restart with an empty revision counter.
	second := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	second.Transcript.SetRedactor((&growingRedactor{known: true}).redact)
	second.Transcript.SweepSessionTree(ctx, sess.ID, 1)

	swept, err := mem.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read swept messages", err)
	if strings.Contains(swept[0].Content, sweepSecret) {
		t.Fatalf("a row stamped before the restart was never revisited:\n%s", swept[0].Content)
	}
}

// failingUpdateStore refuses to rewrite one row.
type failingUpdateStore struct {
	*store.Memory
	messageID string
}

func (s *failingUpdateStore) UpdateMessage(ctx context.Context, sessionID, messageID string, msg api.Message) (api.Message, error) {
	if messageID == s.messageID {
		return api.Message{}, errors.New("row is locked")
	}
	return s.Memory.UpdateMessage(ctx, sessionID, messageID, msg)
}

func TestSweepContinuesPastARowTheStoreRefuses(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	blocked := &failingUpdateStore{Memory: mem, messageID: "tool-1"}
	mgr := NewHost(blocked, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.Runner.Transcript.SetRedactor((&growingRedactor{known: true}).redact)
	sess, err := mem.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	for _, id := range []string{"tool-1", "tool-2"} {
		testutil.FailErr(t, "append row", mem.AppendMessages(ctx, sess.ID, api.Message{
			ID: id, Role: api.MessageRoleTool, Content: "token " + sweepSecret,
		}))
	}

	mgr.Runner.Transcript.SweepSessionTree(ctx, sess.ID, 1)

	swept, err := mem.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read swept messages", err)
	if !strings.Contains(swept[0].Content, sweepSecret) {
		t.Fatalf("precondition failed: the refused row was rewritten anyway:\n%s", swept[0].Content)
	}
	if strings.Contains(swept[1].Content, sweepSecret) {
		t.Fatalf("a refused row abandoned the rest of the pass:\n%s", swept[1].Content)
	}
	// The refused row keeps its earlier stamp, so the next pass finds it again.
	blocked.messageID = ""
	mgr.Runner.Transcript.SweepSessionTree(ctx, sess.ID, 2)
	retried, err := mem.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read retried messages", err)
	if strings.Contains(retried[0].Content, sweepSecret) {
		t.Fatalf("the refused row was stamped as screened and never retried:\n%s", retried[0].Content)
	}
}

// Stale rows are found below the generation, and a stamp lifts them out of the
// candidate set.
func TestScreenGenerationStampNarrowsTheCandidateSet(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	sess, err := mem.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append row", mgr.Runner.Transcript.Append(ctx, sess.ID, api.Message{
		ID: "m-1", Role: api.MessageRoleTool, Content: "plain",
	}))

	stale, err := mem.MessageIDsBelowScreenGeneration(ctx, sess.ID, 3)
	testutil.FailErr(t, "list stale", err)
	if len(stale) != 1 {
		t.Fatalf("unstamped rows = %d, want 1", len(stale))
	}
	testutil.FailErr(t, "stamp", mem.StampMessageScreenGeneration(ctx, sess.ID, "m-1", 3))
	stale, err = mem.MessageIDsBelowScreenGeneration(ctx, sess.ID, 3)
	testutil.FailErr(t, "list stale after stamp", err)
	if len(stale) != 0 {
		t.Fatalf("stamped row still listed as stale: %v", stale)
	}
}
