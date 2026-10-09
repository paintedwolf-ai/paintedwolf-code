package sourceledger

import (
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestClassifyActorPlacesEveryOrigin(t *testing.T) {
	cases := []struct {
		name          string
		origin        api.SourceChangeOrigin
		effectSession string
		viewerSession string
		want          ActorClass
	}{
		{"own agent effect", api.SourceChangeOriginAgent, "s1", "s1", ActorYou},
		{"foreign agent effect", api.SourceChangeOriginAgent, "s2", "s1", ActorAgent},
		{"agent effect without session", api.SourceChangeOriginAgent, "", "s1", ActorAgent},
		{"agent effect with no viewer", api.SourceChangeOriginAgent, "s1", "", ActorAgent},
		{"user effect", api.SourceChangeOriginUser, "", "s1", ActorUser},
		{"external effect", api.SourceChangeOriginExternal, "", "s1", ActorExternal},
		{"unrecorded origin", api.SourceChangeOrigin(""), "", "s1", ActorUnknown},
	}
	for _, tc := range cases {
		if got := ClassifyActor(tc.origin, tc.effectSession, tc.viewerSession); got != tc.want {
			t.Errorf("%s: ClassifyActor = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestActorDisplayNamesForeignAgentsOnly(t *testing.T) {
	foreign := Effect{Origin: api.SourceChangeOriginAgent, SessionID: "s2", ActorLabel: "refactor worker"}
	if got := foreign.ActorDisplay("s1"); got != "refactor worker" {
		t.Fatalf("labeled foreign agent = %q, want recorded label", got)
	}
	unlabeled := Effect{Origin: api.SourceChangeOriginAgent, SessionID: "s2", JobID: "j1"}
	if got := unlabeled.ActorDisplay("s1"); got != "worker job" {
		t.Fatalf("unlabeled worker = %q, want %q", got, "worker job")
	}
	if got := (Effect{Origin: api.SourceChangeOriginUser}).ActorDisplay("s1"); got != "" {
		t.Fatalf("user effect display = %q, want empty — the class already says user", got)
	}
}

func TestTurnCheckpointAndActivityFloorReportAbsenceAsUnknown(t *testing.T) {
	store, ctx := openLedger(t)
	if _, found, err := store.Checkpoints.TurnCheckpoint(ctx, "p1", "s1", 1); err != nil || found {
		t.Fatalf("missing checkpoint = (%v, %v), want absent without error", found, err)
	}
	if _, found, err := store.History.SessionActivityFloor(ctx, "p1", "s1"); err != nil || found {
		t.Fatalf("missing floor = (%v, %v), want absent without error", found, err)
	}

	_, err := store.Checkpoints.CreateStructuralCheckpoint(ctx, StructuralCheckpointInput{
		ProjectID: "p1", Kind: CheckpointTurn, Label: "Turn start", SessionID: "s1", Turn: 1,
	})
	testutil.FailErr(t, "create turn 1 checkpoint", err)
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "a.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		OperationID: "op-user-a", After: []byte("a\n"),
	})
	_, err = store.Checkpoints.CreateStructuralCheckpoint(ctx, StructuralCheckpointInput{
		ProjectID: "p1", Kind: CheckpointTurn, Label: "Turn start", SessionID: "s1", Turn: 2,
	})
	testutil.FailErr(t, "create turn 2 checkpoint", err)

	first, found, err := store.Checkpoints.TurnCheckpoint(ctx, "p1", "s1", 1)
	testutil.FailErr(t, "resolve turn 1 checkpoint", err)
	second, foundSecond, err := store.Checkpoints.TurnCheckpoint(ctx, "p1", "s1", 2)
	testutil.FailErr(t, "resolve turn 2 checkpoint", err)
	if !found || !foundSecond || second.CreatedOrdinal <= first.CreatedOrdinal {
		t.Fatalf("turn checkpoints = (%v %d, %v %d), want both found in ordinal order",
			found, first.CreatedOrdinal, foundSecond, second.CreatedOrdinal)
	}
	floor, foundFloor, err := store.History.SessionActivityFloor(ctx, "p1", "s1")
	testutil.FailErr(t, "resolve activity floor", err)
	if !foundFloor || floor != first.CreatedOrdinal {
		t.Fatalf("activity floor = (%v, %d), want turn 1 ordinal %d", foundFloor, floor, first.CreatedOrdinal)
	}
}

func TestTurnCheckpointIsAnImmutableIdempotentBoundary(t *testing.T) {
	store, ctx := openLedger(t)
	input := StructuralCheckpointInput{
		ProjectID: "p1", Kind: CheckpointTurn, Label: "Turn start", SessionID: "s1", Turn: 1,
	}
	first, err := store.Checkpoints.CreateStructuralCheckpoint(ctx, input)
	testutil.FailErr(t, "create turn checkpoint", err)
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "a.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent,
		SessionID: "s1", Turn: 1, OperationID: "op-agent-a", After: []byte("a\n"),
	})

	replayed, err := store.Checkpoints.CreateStructuralCheckpoint(ctx, input)
	testutil.FailErr(t, "replay turn checkpoint", err)
	if replayed.ID != first.ID || replayed.CreatedOrdinal != first.CreatedOrdinal ||
		!replayed.CreatedTS.Equal(first.CreatedTS) {
		t.Fatalf("replayed checkpoint = %+v, want original boundary %+v", replayed, first)
	}

	resolved, found, err := store.Checkpoints.TurnCheckpoint(ctx, "p1", "s1", 1)
	testutil.FailErr(t, "resolve immutable checkpoint", err)
	if !found || resolved.ID != first.ID || resolved.CreatedOrdinal != first.CreatedOrdinal {
		t.Fatalf("resolved checkpoint = (%v, %+v), want original boundary %+v", found, resolved, first)
	}
	effects, err := store.History.EffectsBetween(ctx, "p1", resolved.CreatedOrdinal, 0, 10)
	testutil.FailErr(t, "list effects after immutable checkpoint", err)
	if len(effects) != 1 || effects[0].Path != "a.txt" || effects[0].Turn != 1 {
		t.Fatalf("effects after checkpoint = %+v, want the turn's recorded write", effects)
	}
}

func TestEffectsBetweenBoundsAreExclusiveInclusive(t *testing.T) {
	store, ctx := openLedger(t)
	for _, step := range []struct{ op, path, content string }{
		{"op-1", "a.txt", "one\n"},
		{"op-2", "a.txt", "two\n"},
		{"op-3", "b.txt", "three\n"},
	} {
		mustRecord(t, store, ctx, RecordInput{
			ProjectID: "p1", RootID: "r1", Path: step.path,
			Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginUser,
			OperationID: step.op, After: []byte(step.content),
		})
	}
	all, err := store.History.EffectsBetween(ctx, "p1", 0, 0, 10)
	testutil.FailErr(t, "list all effects", err)
	if len(all) != 3 {
		t.Fatalf("effects = %d, want 3", len(all))
	}
	// all is newest first: all[2] is the oldest effect.
	window, err := store.History.EffectsBetween(ctx, "p1", all[2].Ordinal, all[1].Ordinal, 10)
	testutil.FailErr(t, "list window", err)
	if len(window) != 1 || window[0].Ordinal != all[1].Ordinal {
		t.Fatalf("window = %d effects, want exactly the middle effect", len(window))
	}
}

func TestQueryFileEffectsPagesNewestFirst(t *testing.T) {
	store, ctx := openLedger(t)
	for _, op := range []string{"op-1", "op-2", "op-3"} {
		mustRecord(t, store, ctx, RecordInput{
			ProjectID: "p1", RootID: "r1", Path: "a.txt",
			Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
			SessionID: "s1", OperationID: op, After: []byte(op + "\n"),
		})
	}
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "other.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		OperationID: "op-other", After: []byte("noise\n"),
	})
	fileID, _ := mustResolve(t, store, ctx, "a.txt")

	page, err := store.History.QueryFileEffects(ctx, "p1", fileID, 0, 0, 2)
	testutil.FailErr(t, "first page", err)
	if len(page.Effects) != 2 || page.NextBeforeOrdinal == 0 {
		t.Fatalf("first page = %d effects cursor %d, want 2 with cursor", len(page.Effects), page.NextBeforeOrdinal)
	}
	rest, err := store.History.QueryFileEffects(ctx, "p1", fileID, 0, page.NextBeforeOrdinal, 2)
	testutil.FailErr(t, "second page", err)
	if len(rest.Effects) != 1 || rest.NextBeforeOrdinal != 0 {
		t.Fatalf("second page = %d effects cursor %d, want final single effect", len(rest.Effects), rest.NextBeforeOrdinal)
	}
	if page.Effects[0].Ordinal <= page.Effects[1].Ordinal || page.Effects[1].Ordinal <= rest.Effects[0].Ordinal {
		t.Fatal("file effects are not newest first across pages")
	}

	latest, found, err := store.History.LatestFileEffect(ctx, "p1", fileID)
	testutil.FailErr(t, "latest effect", err)
	if !found || latest.Ordinal != page.Effects[0].Ordinal {
		t.Fatalf("latest = (%v, %d), want newest ordinal %d", found, latest.Ordinal, page.Effects[0].Ordinal)
	}
	if _, found, err := store.History.LatestFileEffect(ctx, "p1", "no-such-file"); err != nil || found {
		t.Fatalf("unknown file latest = (%v, %v), want absent without error", found, err)
	}
}

func TestQueryAttributionCarriesEveryOrigin(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "a.txt",
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		OperationID: "op-user-create", After: []byte("alpha\nbeta\n"),
	})
	mustRecord(t, store, ctx, RecordInput{
		ProjectID: "p1", RootID: "r1", Path: "a.txt",
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent, SessionID: "s1",
		OperationID: "op-agent-write",
		Before:      []byte("alpha\nbeta\n"), After: []byte("alpha\ngamma\n"),
	})
	res, err := store.QueryAttribution(ctx, "p1", sourcebranch.Trunk, "r1", "a.txt")
	testutil.FailErr(t, "query attribution", err)
	origins := map[api.SourceChangeOrigin]bool{}
	for _, iv := range res.Intervals {
		origins[iv.Origin] = true
	}
	if !origins[api.SourceChangeOriginUser] || !origins[api.SourceChangeOriginAgent] {
		t.Fatalf("interval origins = %v, want both user and agent recorded — presentation filters, the ledger does not", origins)
	}
}

func TestMixedPublicationClassifiesContributorsRatherThanPublisher(t *testing.T) {
	t.Parallel()
	effect := Effect{Origin: api.SourceChangeOriginAgent, SessionID: "mine", Turn: 9, Contributors: []Contributor{
		{Origin: api.SourceChangeOriginAgent, SessionID: "mine", Turn: 9},
		{Origin: api.SourceChangeOriginUser, SessionID: "focused"},
		{Origin: api.SourceChangeOriginAgent, SessionID: "other", ActorLabel: "Other chat", Turn: 2},
	}}
	if effect.ActorClassFor("mine") != ActorMixed || effect.AuthoredTurn() != 0 {
		t.Fatalf("publisher flattened contributors: %+v", effect)
	}
	if got := effect.ActorDisplay("mine"); got != "you; user; agent: Other chat" {
		t.Fatalf("mixed authors = %q", got)
	}
	effect.Origin = api.SourceChangeOriginUser
	effect.Contributors = effect.Contributors[:1]
	if effect.ActorClassFor("mine") != ActorYou || effect.AuthoredTurn() != 9 {
		t.Fatal("human publication reassigned agent authorship")
	}
}
