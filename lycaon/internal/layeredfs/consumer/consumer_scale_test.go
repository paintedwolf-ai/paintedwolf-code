package consumer_test

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/consumer"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
)

type bytesReadSeekCloser struct{ *bytes.Reader }

func (b *bytesReadSeekCloser) Close() error { return nil }

type simulated2MLower struct {
	realFiles    map[string][]byte
	realDirs     map[string][]layeredfs.LowerDirEntry
	readDirCalls atomic.Int64
	lstatCalls   atomic.Int64
	openCalls    atomic.Int64
	modTime      time.Time
}

func newSimulated2MLower() *simulated2MLower {
	now := time.Unix(1710000000, 0).UTC()
	loginData := []byte("package auth\n\nfunc Login(user string) bool {\n\treturn VerifyToken(user)\n}\n")
	tokenData := []byte("package auth\n\nfunc VerifyToken(tok string) bool {\n\treturn tok != \"\"\n}\n")
	return &simulated2MLower{
		modTime: now,
		realFiles: map[string][]byte{
			"pkg/auth/login.go": loginData,
			"pkg/auth/token.go": tokenData,
		},
		realDirs: map[string][]layeredfs.LowerDirEntry{
			"pkg/auth": {
				{Name: "login.go", Kind: layeredfs.EntryRegular, Mode: 0o644, Ino: 101},
				{Name: "token.go", Kind: layeredfs.EntryRegular, Mode: 0o644, Ino: 102},
			},
		},
	}
}

func (s *simulated2MLower) Lstat(rel string) (layeredfs.LowerEntry, error) {
	s.lstatCalls.Add(1)
	if rel == "." || rel == "pkg" || rel == "pkg/auth" {
		return layeredfs.LowerEntry{Kind: layeredfs.EntryDirectory, Mode: fs.ModeDir | 0o755, ModTime: s.modTime, Dev: 1, Ino: 1}, nil
	}
	if data, ok := s.realFiles[rel]; ok {
		return layeredfs.LowerEntry{
			Kind: layeredfs.EntryRegular, Mode: 0o644, Size: int64(len(data)),
			ModTime: s.modTime, Dev: 1, Ino: 100, CtimeSec: s.modTime.Unix(),
		}, nil
	}
	return layeredfs.LowerEntry{}, fs.ErrNotExist
}

func (s *simulated2MLower) ReadDir(rel string) ([]layeredfs.LowerDirEntry, error) {
	s.readDirCalls.Add(1)
	if entries, ok := s.realDirs[rel]; ok {
		return append([]layeredfs.LowerDirEntry(nil), entries...), nil
	}
	return nil, fs.ErrNotExist
}

func (s *simulated2MLower) OpenRead(rel string) (layeredfs.ReadSeekCloser, layeredfs.LowerEntry, error) {
	s.openCalls.Add(1)
	data, ok := s.realFiles[rel]
	if !ok {
		return nil, layeredfs.LowerEntry{}, fs.ErrNotExist
	}
	le := layeredfs.LowerEntry{
		Kind: layeredfs.EntryRegular, Mode: 0o644, Size: int64(len(data)),
		ModTime: s.modTime, Dev: 1, Ino: 100, CtimeSec: s.modTime.Unix(),
	}
	return &bytesReadSeekCloser{Reader: bytes.NewReader(data)}, le, nil
}

func (s *simulated2MLower) Readlink(string) (string, error)             { return "", fs.ErrInvalid }
func (s *simulated2MLower) ListXattr(string) (map[string][]byte, error) { return nil, nil }

func newTestLiveTracker(t *testing.T, viewID string, core *layeredfs.Core, roots []layeredfs.RootSpec) *observation.Tracker {
	t.Helper()
	tr, err := observation.NewTracker(observation.TrackerConfig{
		ViewID: viewID,
		Facts: observation.VisibilityFacts{
			AdapterName:          "fuselinux",
			UntouchedVisibility:  observation.VisibilityLive,
			SupportsInvalidation: true,
			Completeness:         observation.CompletenessComplete,
		},
		Core:  core,
		Roots: roots,
	})
	if err != nil {
		t.Fatalf("NewTracker: %v", err)
	}
	return tr
}

func TestInstrumentedFullWorkerTurnOn2MEntrySimulatedLower(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	lower := newSimulated2MLower()
	archiveLower := newSimulated2MLower()

	roots := []layeredfs.RootSpec{
		{ID: "code", Label: "code", IsPrimary: true},
		{ID: "archive", Label: "archive"},
	}
	mountCode := filepath.Join(tmp, "mount", "code")
	mountArchive := filepath.Join(tmp, "mount", "archive")
	if err := os.MkdirAll(filepath.Join(mountCode, "pkg", "auth"), 0o755); err != nil {
		t.Fatalf("mkdir mount: %v", err)
	}
	if err := os.MkdirAll(mountArchive, 0o755); err != nil {
		t.Fatalf("mkdir archive mount: %v", err)
	}
	for rel, data := range lower.realFiles {
		if err := os.WriteFile(filepath.Join(mountCode, filepath.FromSlash(rel)), data, 0o644); err != nil {
			t.Fatalf("seed mount file: %v", err)
		}
	}

	core, err := layeredfs.New(layeredfs.Config{
		ViewID: "view-2m-turn", BackingDir: filepath.Join(tmp, "backing"), Roots: roots,
	})
	if err != nil {
		t.Fatalf("layeredfs.New: %v", err)
	}
	t.Cleanup(func() { _ = core.ReleaseView() })
	if err := core.RegisterLowerProvider("code", lower); err != nil {
		t.Fatalf("RegisterLowerProvider code: %v", err)
	}
	if err := core.RegisterLowerProvider("archive", archiveLower); err != nil {
		t.Fatalf("RegisterLowerProvider archive: %v", err)
	}

	ckptStore, err := checkpoint.NewStore(filepath.Join(tmp, "checkpoints"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	const simulatedScale = 2_000_000
	var archiveQueried atomic.Bool
	indexProvider := consumer.SourceIndexFunc(func(_ context.Context, q consumer.SourceIndexQuery) (consumer.SourceIndexResult, error) {
		for _, r := range q.RootIDs {
			if r == "archive" {
				archiveQueried.Store(true)
			}
		}
		now := time.Unix(1710000100, 0).UTC()
		fresh := consumer.IndexFreshness{ObservedAt: now, SourceGeneration: simulatedScale, Coverage: consumer.CoverageComplete}
		all := []consumer.SourceIndexEntry{
			{Path: layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/auth/login.go"}, Kind: layeredfs.EntryRegular, Mode: 0o644, Size: 73, ModTime: now, ObservedAt: now, Freshness: fresh},
			{Path: layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/auth/token.go"}, Kind: layeredfs.EntryRegular, Mode: 0o644, Size: 69, ModTime: now, ObservedAt: now, Freshness: fresh},
			{Path: layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/shard_1999999/deep.go"}, Kind: layeredfs.EntryRegular, Mode: 0o644, Size: 128, ModTime: now, ObservedAt: now, Freshness: fresh},
		}
		var filtered []consumer.SourceIndexEntry
		for _, e := range all {
			if q.PathPrefix == "" || strings.HasPrefix(e.Path.Rel, q.PathPrefix) {
				filtered = append(filtered, e)
			}
		}
		return consumer.SourceIndexResult{Entries: filtered, Coverage: consumer.CoverageComplete, ObservedAt: now, SourceGeneration: simulatedScale}, nil
	})

	eng, err := consumer.NewEngine(consumer.EngineConfig{
		ViewID:           "view-2m-turn",
		Roots:            roots,
		MountedRootPaths: map[string]string{"code": mountCode, "archive": mountArchive},
		BackingDir:       filepath.Join(tmp, "backing"),
		Core:             core,
		Index:            indexProvider,
		CheckpointStore:  ckptStore,
		Tracker:          newTestLiveTracker(t, "view-2m-turn", core, roots),
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// 1. Orientation inventory on selected root "code" only.
	orient, err := eng.BuildOrientationInventory(ctx, []string{"code"})
	if err != nil || archiveQueried.Load() || len(orient.Entries) != 3 {
		t.Fatalf("BuildOrientationInventory failed: err=%v archiveQueried=%v entries=%d", err, archiveQueried.Load(), len(orient.Entries))
	}
	for _, entry := range orient.Entries {
		if !entry.IsIndexDerived || entry.ExactContentObserved {
			t.Fatalf("expected index-derived orientation entry without exact content claim: %+v", entry)
		}
	}
	if got := lower.readDirCalls.Load(); got != 0 {
		t.Fatalf("expected 0 lower ReadDir calls after orientation inventory, got %d", got)
	}

	// 2. Scoped branch survey and workspace search inside pkg/auth.
	survey, err := eng.SurveyBranch(ctx, consumer.SurveyOptions{RootIDs: []string{"code"}, PathPrefix: "pkg/auth"})
	if err != nil || len(survey.Entries) != 2 {
		t.Fatalf("SurveyBranch got %d entries, err=%v", len(survey.Entries), err)
	}
	searchRes, err := eng.SearchWorkspace(ctx, consumer.SearchRequest{RootIDs: []string{"code"}, PathPrefix: "pkg/auth", Query: "VerifyToken"})
	if err != nil || len(searchRes.Matches) != 2 {
		t.Fatalf("SearchWorkspace got %d matches, err=%v", len(searchRes.Matches), err)
	}
	for _, m := range searchRes.Matches {
		if !m.ConfirmedViaMount || m.Observation == nil || m.Observation.SHA256 == "" {
			t.Fatalf("expected search match to be confirmed via mount with observation: %+v", m)
		}
	}
	if got := lower.readDirCalls.Load(); got != 0 {
		t.Fatalf("expected 0 lower ReadDir calls after indexed survey + search, got %d", got)
	}

	// 3. Explicit single-directory listing of pkg/auth.
	dirList, err := eng.ListDirectory(ctx, layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/auth"}, "")
	if err != nil || len(dirList) != 2 {
		t.Fatalf("ListDirectory got %d entries, err=%v", len(dirList), err)
	}
	if got := lower.readDirCalls.Load(); got != 1 {
		t.Fatalf("expected 1 lower ReadDir call after explicit ListDirectory, got %d", got)
	}

	// 4. Live evidence read + private modifications.
	loginQP := layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/auth/login.go"}
	ev, err := eng.ReadEvidence(ctx, loginQP)
	if err != nil || ev.Source != consumer.AccessSourceMountedView || ev.Observation.SHA256 == "" {
		t.Fatalf("unexpected ReadEvidence: %+v (err=%v)", ev, err)
	}
	if _, err := core.WriteFile(loginQP, []byte("package auth\n\nfunc Login(user string) bool { return VerifyToken(user) && RequireMFA(user) }\n"), 0o644); err != nil {
		t.Fatalf("WriteFile login.go: %v", err)
	}
	if _, err := core.WriteFile(layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/auth/mfa.go"}, []byte("package auth\n\nfunc RequireMFA(user string) bool { return user != \"\" }\n"), 0o644); err != nil {
		t.Fatalf("WriteFile mfa.go: %v", err)
	}

	// 5. Divergence & checkpoint capture in O(changed nodes) with 0 lower ReadDir calls.
	hasDiv, err := eng.HasWorkspaceDivergence(ctx)
	if err != nil || !hasDiv {
		t.Fatalf("HasWorkspaceDivergence = (%v, %v), want (true, nil)", hasDiv, err)
	}
	div, err := eng.ComputeDivergence(ctx, "")
	if err != nil || len(div.Entries) != 2 {
		t.Fatalf("ComputeDivergence got %d entries, err=%v", len(div.Entries), err)
	}
	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core: core, Store: ckptStore, WorkerID: "w-2m", TaskID: "t-2m", ProjectID: "p-2m", SourceGeneration: "gen-2m", SelectedRoots: roots,
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	pubRes, err := pub.Publish(ctx, checkpoint.PublishRequest{
		CheckpointID: "ckpt-2m-turn", Trigger: checkpoint.TriggerForegroundCommand,
	})
	if err != nil || len(pubRes.Manifest.Entries) != 2 {
		t.Fatalf("Publish checkpoint got %v, err=%v", pubRes, err)
	}
	if got := lower.readDirCalls.Load(); got != 1 {
		t.Fatalf("expected lower ReadDir calls to remain 1 across full worker turn on 2M simulated tree, got %d", got)
	}
}

func TestAbsentAndStaleIndexEntriesResolveViaMountAndReportIncompleteSearch(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	lower := newSimulated2MLower()
	hotfixQP := layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/auth/unindexed_hotfix.go"}
	hotfixData := []byte("package auth\n\nconst HotfixFlag = \"HOTFIX_ACTIVE\"\n")
	lower.realFiles["pkg/auth/unindexed_hotfix.go"] = hotfixData
	lower.realDirs["pkg/auth"] = append(
		lower.realDirs["pkg/auth"],
		layeredfs.LowerDirEntry{Name: "unindexed_hotfix.go", Kind: layeredfs.EntryRegular, Mode: 0o644, Ino: 103},
	)

	mountCode := filepath.Join(tmp, "mount", "code")
	if err := os.MkdirAll(filepath.Join(mountCode, "pkg", "auth"), 0o755); err != nil {
		t.Fatalf("mkdir mount: %v", err)
	}
	for rel, data := range lower.realFiles {
		if err := os.WriteFile(filepath.Join(mountCode, filepath.FromSlash(rel)), data, 0o644); err != nil {
			t.Fatalf("seed mount: %v", err)
		}
	}

	roots := []layeredfs.RootSpec{{ID: "code", Label: "code", IsPrimary: true}}
	core, err := layeredfs.New(layeredfs.Config{
		ViewID: "view-stale-index", BackingDir: filepath.Join(tmp, "backing"), Roots: roots,
	})
	if err != nil {
		t.Fatalf("layeredfs.New: %v", err)
	}
	t.Cleanup(func() { _ = core.ReleaseView() })
	if err := core.RegisterLowerProvider("code", lower); err != nil {
		t.Fatalf("RegisterLowerProvider: %v", err)
	}

	partialIndex := consumer.SourceIndexFunc(func(_ context.Context, _ consumer.SourceIndexQuery) (consumer.SourceIndexResult, error) {
		return consumer.SourceIndexResult{
			Entries: []consumer.SourceIndexEntry{{
				Path: layeredfs.QualifiedPath{RootID: "code", Rel: "pkg/auth/login.go"}, Kind: layeredfs.EntryRegular,
				Mode: 0o644, Size: 10, Freshness: consumer.IndexFreshness{Coverage: consumer.CoveragePartial, Stale: true},
			}},
			Coverage: consumer.CoveragePartial, Truncated: true,
		}, nil
	})

	eng, err := consumer.NewEngine(consumer.EngineConfig{
		ViewID: "view-stale-index", Roots: roots, MountedRootPaths: map[string]string{"code": mountCode},
		Core: core, Index: partialIndex, Tracker: newTestLiveTracker(t, "view-stale-index", core, roots),
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	lookedUp, err := eng.LookupPath(ctx, hotfixQP)
	if err != nil || lookedUp.IsIndexDerived || lookedUp.Size != int64(len(hotfixData)) {
		t.Fatalf("unexpected LookupPath result: %+v (err=%v)", lookedUp, err)
	}
	ev, err := eng.ReadEvidence(ctx, hotfixQP)
	if err != nil || !bytes.Equal(ev.Content, hotfixData) {
		t.Fatalf("ReadEvidence unindexed_hotfix.go failed: err=%v content=%q", err, string(ev.Content))
	}

	idxOnlySearch, err := eng.SearchWorkspace(ctx, consumer.SearchRequest{RootIDs: []string{"code"}, PathPrefix: "pkg/auth", Query: "HOTFIX_ACTIVE"})
	if err != nil || !idxOnlySearch.IncompleteScope || idxOnlySearch.Coverage != consumer.CoveragePartial {
		t.Fatalf("expected IncompleteScope=true and CoveragePartial, got %+v (err=%v)", idxOnlySearch, err)
	}

	scopedSearch, err := eng.SearchWorkspace(ctx, consumer.SearchRequest{
		RootIDs: []string{"code"}, PathPrefix: "pkg/auth", Query: "HOTFIX_ACTIVE", AllowScopedTraversal: true,
	})
	if err != nil || scopedSearch.IncompleteScope || len(scopedSearch.Matches) != 1 || scopedSearch.Matches[0].Path != hotfixQP {
		t.Fatalf("expected scoped traversal to find %v with IncompleteScope=false, got %+v (err=%v)", hotfixQP, scopedSearch, err)
	}
}
