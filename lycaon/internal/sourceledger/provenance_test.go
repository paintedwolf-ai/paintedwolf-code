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

func TestDuringCommandOfNeedsTheViewersOwnWindow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		effect Effect
		viewer string
		want   bool
	}{
		{"own window", Effect{CommandWindowID: "w", SessionID: "s1"}, "s1", true},
		{"another session's window", Effect{CommandWindowID: "w", SessionID: "s2"}, "s1", false},
		{"no window", Effect{SessionID: "s1"}, "s1", false},
		{"no viewer", Effect{CommandWindowID: "w"}, "", false},
	} {
		if got := tc.effect.DuringCommandOf(tc.viewer); got != tc.want {
			t.Errorf("%s: DuringCommandOf = %v, want %v", tc.name, got, tc.want)
		}
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
		RecordLocation: RecordLocation{RootID: "r1", Path: "a.txt"},
		ProjectID:      "p1",
		Op:             api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		OperationID: "op-user-a", After: []byte("a\n")})
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
		RecordLocation: RecordLocation{RootID: "r1", Path: "a.txt"},
		ProjectID:      "p1",
		Op:             api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent,
		SessionID: "s1", Turn: 1, OperationID: "op-agent-a", After: []byte("a\n")})

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
			RecordLocation: RecordLocation{RootID: "r1", Path: step.path},
			ProjectID:      "p1",
			Op:             api.SourceChangeOpWrite, Origin: api.SourceChangeOriginUser,
			OperationID: step.op, After: []byte(step.content)})
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

func TestEffectsBetweenOnBranchIsStrictPerRootAndBounded(t *testing.T) {
	store, ctx := openLedger(t)
	worktree := sourcebranch.ForWorktree("w1")
	record := func(op, path string, branch sourcebranch.ID) {
		mustRecord(t, store, ctx, RecordInput{
			RecordLocation: RecordLocation{RootID: "r1", Path: path}, ProjectID: "p1", BranchID: branch,
			Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginUser, OperationID: op, After: []byte(op + "\n"),
		})
	}
	record("op-1", "trunk.txt", sourcebranch.Trunk)
	record("op-2", "worker.txt", "job-1")
	record("op-3", "trunk.txt", sourcebranch.Trunk)
	record("op-4", "worktree.txt", worktree)
	record("op-5", "trunk.txt", sourcebranch.Trunk)

	read := func(branch sourcebranch.ID, after, through int64) []Effect {
		t.Helper()
		effects, err := store.History.EffectsBetweenOnBranch(ctx, "p1", map[string]sourcebranch.ID{"r1": branch}, after, through, 10)
		testutil.FailErr(t, "list branch effects", err)
		return effects
	}
	trunk := read(sourcebranch.Trunk, 0, 0)
	if len(trunk) != 3 || trunk[0].Ordinal <= trunk[1].Ordinal || trunk[1].Ordinal <= trunk[2].Ordinal {
		t.Fatalf("trunk effects = %+v, want the three trunk operations newest first", trunk)
	}
	for _, effect := range trunk {
		if effect.Path != "trunk.txt" || effect.BranchID != sourcebranch.Trunk {
			t.Fatalf("trunk read returned %+v", effect)
		}
	}
	if got := read("job-1", 0, 0); len(got) != 1 || got[0].Path != "worker.txt" {
		t.Fatalf("worker effects = %+v", got)
	}
	if got := read(worktree, 0, 0); len(got) != 1 || got[0].Path != "worktree.txt" {
		t.Fatalf("worktree effects = %+v", got)
	}
	// The lower bound excludes, the upper bound includes.
	if got := read(sourcebranch.Trunk, trunk[2].Ordinal, trunk[1].Ordinal); len(got) != 1 || got[0].Ordinal != trunk[1].Ordinal {
		t.Fatalf("bounded effects = %+v, want only the middle trunk effect", got)
	}
	if got, err := store.History.EffectsBetweenOnBranch(ctx, "p1", nil, 0, 0, 0); err != nil || got != nil {
		t.Fatalf("no roots = (%v, %v), want nothing", got, err)
	}
	unattached, err := store.History.EffectsBetweenOnBranch(ctx, "p1", map[string]sourcebranch.ID{"elsewhere": sourcebranch.Trunk}, 0, 0, 0)
	testutil.FailErr(t, "list unattached root", err)
	if len(unattached) != 0 {
		t.Fatalf("unattached root effects = %+v", unattached)
	}
	var none *History
	if _, err := none.EffectsBetweenOnBranch(ctx, "p1", nil, 0, 0, 1); err == nil {
		t.Fatal("a missing ledger answered")
	}
}

// A move out of a path states what the path held; a move into one does not.
func TestOldestPathEffectAfterOnBranchReadsTheLocation(t *testing.T) {
	store, ctx := openLedger(t)
	record := func(in RecordInput) {
		t.Helper()
		in.ProjectID, in.RootID, in.Origin = "p1", "r1", api.SourceChangeOriginUser
		mustRecord(t, store, ctx, in)
	}
	record(RecordInput{RecordLocation: RecordLocation{Path: "held.go"}, Op: api.SourceChangeOpCreate, OperationID: "create-held", After: []byte("held\n")})
	_, held := mustResolve(t, store, ctx, "held.go")
	record(RecordInput{RecordLocation: RecordLocation{Path: "elsewhere.go"}, Op: api.SourceChangeOpCreate, OperationID: "create-elsewhere", After: []byte("visitor\n")})
	newest, err := store.History.EffectsBetween(ctx, "p1", 0, 0, 1)
	testutil.FailErr(t, "read the newest effect", err)
	floor := newest[0].Ordinal

	record(RecordInput{RecordLocation: RecordLocation{Path: "moved.go", FromPath: "held.go"}, Op: api.SourceChangeOpRename, OperationID: "move-out"})
	record(RecordInput{RecordLocation: RecordLocation{Path: "fresh.go", FromPath: "elsewhere.go"}, Op: api.SourceChangeOpRename, OperationID: "move-in"})

	bounds, found, err := store.History.OldestPathEffectAfterOnBranch(ctx, "p1", sourcebranch.Trunk, "r1", "held.go", floor)
	testutil.FailErr(t, "moved-away path", err)
	if !found || bounds.BeforeVersionID != held {
		t.Fatalf("moved-away bounds = %+v found=%v, want before %s", bounds, found, held)
	}
	bounds, found, err = store.History.OldestPathEffectAfterOnBranch(ctx, "p1", sourcebranch.Trunk, "r1", "fresh.go", floor)
	testutil.FailErr(t, "moved-into path", err)
	if !found || bounds.BeforeVersionID != "" {
		t.Fatalf("moved-into bounds = %+v found=%v, want no predecessor", bounds, found)
	}
	if _, found, err := store.History.OldestPathEffectAfterOnBranch(ctx, "p1", "job-2", "r1", "held.go", floor); err != nil || found {
		t.Fatalf("other branch found=%v err=%v", found, err)
	}
	if _, found, err := store.History.OldestPathEffectAfterOnBranch(ctx, "p1", sourcebranch.Trunk, "r2", "held.go", floor); err != nil || found {
		t.Fatalf("other root found=%v err=%v", found, err)
	}
	var none *History
	if _, _, err := none.OldestPathEffectAfterOnBranch(ctx, "p1", sourcebranch.Trunk, "r1", "held.go", 0); err == nil {
		t.Fatal("an unconfigured ledger answered")
	}
}

func TestOldestFileEffectAfterOnBranchIgnoresOtherBranches(t *testing.T) {
	store, ctx := openLedger(t)
	record := func(op, content string, branch sourcebranch.ID) {
		mustRecord(t, store, ctx, RecordInput{
			RecordLocation: RecordLocation{RootID: "r1", Path: "a.txt"}, ProjectID: "p1", BranchID: branch,
			Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginUser, OperationID: op, After: []byte(content),
		})
	}
	record("op-1", "base\n", sourcebranch.Trunk)
	fileID, baseVersion := mustResolve(t, store, ctx, "a.txt")
	before, err := store.History.EffectsBetween(ctx, "p1", 0, 0, 1)
	testutil.FailErr(t, "read the newest effect", err)
	floor := before[0].Ordinal
	record("op-2", "worker\n", "job-1")
	record("op-3", "first trunk\n", sourcebranch.Trunk)
	_, firstTrunk := mustResolve(t, store, ctx, "a.txt")
	record("op-4", "second trunk\n", sourcebranch.Trunk)

	bounds, found, err := store.History.OldestFileEffectAfterOnBranch(ctx, "p1", sourcebranch.Trunk, fileID, floor)
	testutil.FailErr(t, "resolve oldest trunk effect", err)
	if !found || bounds.BeforeVersionID != baseVersion || bounds.AfterVersionID != firstTrunk {
		t.Fatalf("bounds = %+v (found %v), want %s to %s", bounds, found, baseVersion, firstTrunk)
	}
	if _, found, err := store.History.OldestFileEffectAfterOnBranch(ctx, "p1", "job-2", fileID, floor); err != nil || found {
		t.Fatalf("other branch = (%v, %v), want none", found, err)
	}
	var none *History
	if _, _, err := none.OldestFileEffectAfterOnBranch(ctx, "p1", sourcebranch.Trunk, fileID, 0); err == nil {
		t.Fatal("a missing ledger answered")
	}
}

func TestQueryFileEffectsPagesNewestFirst(t *testing.T) {
	store, ctx := openLedger(t)
	for _, op := range []string{"op-1", "op-2", "op-3"} {
		mustRecord(t, store, ctx, RecordInput{
			RecordLocation: RecordLocation{RootID: "r1", Path: "a.txt"},
			ProjectID:      "p1",
			Op:             api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
			SessionID: "s1", OperationID: op, After: []byte(op + "\n")})
	}
	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "other.txt"},
		ProjectID:      "p1",
		Op:             api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		OperationID: "op-other", After: []byte("noise\n")})
	fileID, _ := mustResolve(t, store, ctx, "a.txt")

	page, err := store.History.QueryFileEffects(ctx, "p1", sourcebranch.Trunk, fileID, 0, 0, 2)
	testutil.FailErr(t, "first page", err)
	if len(page.Effects) != 2 || page.NextBeforeOrdinal == 0 {
		t.Fatalf("first page = %d effects cursor %d, want 2 with cursor", len(page.Effects), page.NextBeforeOrdinal)
	}
	rest, err := store.History.QueryFileEffects(ctx, "p1", sourcebranch.Trunk, fileID, 0, page.NextBeforeOrdinal, 2)
	testutil.FailErr(t, "second page", err)
	if len(rest.Effects) != 1 || rest.NextBeforeOrdinal != 0 {
		t.Fatalf("second page = %d effects cursor %d, want final single effect", len(rest.Effects), rest.NextBeforeOrdinal)
	}
	if page.Effects[0].Ordinal <= page.Effects[1].Ordinal || page.Effects[1].Ordinal <= rest.Effects[0].Ordinal {
		t.Fatal("file effects are not newest first across pages")
	}

	latest, found, err := store.History.LatestFileEffect(ctx, "p1", sourcebranch.Trunk, fileID)
	testutil.FailErr(t, "latest effect", err)
	if !found || latest.Ordinal != page.Effects[0].Ordinal {
		t.Fatalf("latest = (%v, %d), want newest ordinal %d", found, latest.Ordinal, page.Effects[0].Ordinal)
	}
	if _, found, err := store.History.LatestFileEffect(ctx, "p1", sourcebranch.Trunk, "no-such-file"); err != nil || found {
		t.Fatalf("unknown file latest = (%v, %v), want absent without error", found, err)
	}
}

func TestQueryAttributionCarriesEveryOrigin(t *testing.T) {
	store, ctx := openLedger(t)
	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "a.txt"},
		ProjectID:      "p1",
		Op:             api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser,
		OperationID: "op-user-create", After: []byte("alpha\nbeta\n")})
	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r1", Path: "a.txt"},
		ProjectID:      "p1",
		Op:             api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent, SessionID: "s1",
		OperationID: "op-agent-write",
		Before:      []byte("alpha\nbeta\n"), After: []byte("alpha\ngamma\n")})
	res, err := store.History.QueryAttribution(ctx, "p1", sourcebranch.Trunk, "r1", "a.txt")
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
