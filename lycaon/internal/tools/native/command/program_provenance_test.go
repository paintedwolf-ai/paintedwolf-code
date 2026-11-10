package command

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type fakeProvenanceStore struct {
	mu             sync.Mutex
	custody        map[string][]string
	commits        []evidence.ProgramHTTPProvenance
	commitSessions []string
}

func newFakeProvenanceStore() *fakeProvenanceStore {
	return &fakeProvenanceStore{custody: map[string][]string{}}
}

func (f *fakeProvenanceStore) seed(sessionID, path string, urls ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.custody[sessionID+"\x00"+path] = append([]string(nil), urls...)
}

func (f *fakeProvenanceStore) Get(context.Context, string) (*api.Session, error) {
	return nil, nil
}

func (f *fakeProvenanceStore) CommitProgramHTTPProvenance(_ context.Context, sessionID, _ string, prov evidence.ProgramHTTPProvenance) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits = append(f.commits, prov)
	f.commitSessions = append(f.commitSessions, sessionID)
	return "http_response#1", nil
}

func (f *fakeProvenanceStore) ExternalCustodyURLsForPaths(_ context.Context, sessionID, _ string, paths []string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var urls []string
	for _, p := range paths {
		for _, u := range f.custody[sessionID+"\x00"+p] {
			if !slices.Contains(urls, u) {
				urls = append(urls, u)
			}
		}
	}
	return urls, nil
}

func TestOpTrackerContextAndNilSafety(t *testing.T) {
	if got := opTrackerFromContext(nil); got != nil {
		t.Fatalf("opTrackerFromContext(nil) = %v, want nil", got)
	}
	if got := opTrackerFromContext(context.Background()); got != nil {
		t.Fatalf("tracker-less context returned %v, want nil", got)
	}
	base := context.Background()
	if got := withOpTracker(base, nil); got != base {
		t.Fatalf("withOpTracker with nil tracker must return the context unchanged")
	}
	tracker := &opProvenanceTracker{}
	ctx := withOpTracker(base, tracker)
	if got := opTrackerFromContext(ctx); got != tracker {
		t.Fatalf("opTrackerFromContext lost the tracker: %v", got)
	}

	var nilTracker *opProvenanceTracker
	nilTracker.addExchange(evidence.ExchangeProvenance{External: true})
	nilTracker.addReadPath("a.txt")

	tracker.addReadPath("")
	tracker.addReadPath("a.txt")
	tracker.addReadPath("a.txt")
	if len(tracker.readPaths) != 1 || tracker.readPaths[0] != "a.txt" {
		t.Fatalf("readPaths = %v, want single deduped a.txt", tracker.readPaths)
	}
	tracker.addExchange(evidence.ExchangeProvenance{External: true, ObservedURLs: []string{"https://x.example"}})
	if len(tracker.exchanges) != 1 {
		t.Fatalf("exchanges = %d, want 1", len(tracker.exchanges))
	}
}

func TestProgramRunProjectDirAndPathNormalization(t *testing.T) {
	var nilRun *programRun
	if got := nilRun.projectDir(); got != "" {
		t.Fatalf("nil run projectDir = %q, want empty", got)
	}

	dirA := t.TempDir()
	dirB := t.TempDir()
	run := &programRun{tc: tools.ToolContext{Source: tools.InvocationSource{
		Roots: []projectroot.RootRef{
			{ID: "root-a", Path: dirA},
			{ID: "root-b", Path: dirB},
		},
		ActiveRootID: "root-b",
	}}}
	if got := run.projectDir(); got != dirB {
		t.Fatalf("projectDir = %q, want active root %q", got, dirB)
	}
	run.tc.Source.ActiveRootID = ""
	if got := run.projectDir(); got != dirA {
		t.Fatalf("projectDir without active root = %q, want first root %q", got, dirA)
	}
	run.tc.Source.Roots = nil
	if got := run.projectDir(); got != "" {
		t.Fatalf("projectDir without roots = %q, want empty", got)
	}

	bare := &programRun{}
	if got := bare.normalizeProvenancePath("", "  "); got != "" {
		t.Fatalf("blank path normalized = %q, want empty", got)
	}
	if got := bare.normalizeProvenancePath("", "sub/file.txt"); got != "sub/file.txt" {
		t.Fatalf("bare run normalization = %q, want sub/file.txt", got)
	}

	rooted := &programRun{tc: tools.ToolContext{Source: tools.InvocationSource{
		Roots:        []projectroot.RootRef{{ID: "root-b", Path: dirB}},
		ActiveRootID: "root-b",
	}}}
	if got := rooted.normalizeProvenancePath("root-b", "sub/file.txt"); got != "sub/file.txt" {
		t.Fatalf("root-qualified normalization = %q, want sub/file.txt", got)
	}
	if got := rooted.normalizeProvenancePath("root-unknown", filepath.Join(dirB, "sub", "file.txt")); got != "sub/file.txt" {
		t.Fatalf("unknown-root normalization = %q, want sub/file.txt", got)
	}
	if got := rooted.normalizeProvenancePath("", "outside.txt"); got != "outside.txt" {
		t.Fatalf("relative path normalization = %q, want outside.txt", got)
	}
	if got := rooted.normalizeProvenancePath("", filepath.Join(dirA, "outside.txt")); got != filepath.ToSlash(filepath.Clean(filepath.Join(dirA, "outside.txt"))) {
		t.Fatalf("absolute path outside projectDir normalized = %q, want the cleaned fallback path", got)
	}
}

func TestObserveProvenanceFileReadTrackers(t *testing.T) {
	var nilRun *programRun
	nilRun.observeProvenanceFileRead(context.Background(), "", "a.txt")

	run := &programRun{}
	ctxTracker := &opProvenanceTracker{}
	ctx := withOpTracker(context.Background(), ctxTracker)
	run.observeProvenanceFileRead(ctx, "", "a.txt")
	if !slices.Equal(ctxTracker.readPaths, []string{"a.txt"}) {
		t.Fatalf("ctx tracker readPaths = %v, want [a.txt]", ctxTracker.readPaths)
	}

	active := run.beginOpTracker()
	run.observeProvenanceFileRead(ctx, "", "b.txt")
	if !slices.Equal(active.readPaths, []string{"b.txt"}) {
		t.Fatalf("active tracker readPaths = %v, want [b.txt]", active.readPaths)
	}
	if !slices.Contains(ctxTracker.readPaths, "b.txt") {
		t.Fatalf("ctx tracker missing b.txt: %v", ctxTracker.readPaths)
	}
	if len(run.provenance.activeTrackers) != 1 {
		t.Fatalf("activeTrackers = %d, want 1", len(run.provenance.activeTrackers))
	}
}

func TestNoteInputRedirectReadRoutesCustody(t *testing.T) {
	var nilRun *programRun
	nilRun.noteInputRedirectRead(nil, "")

	run := &programRun{}
	run.noteInputRedirectRead(nil, "")

	tracker := &opProvenanceTracker{}
	run.noteInputRedirectRead(tracker, "a.txt")
	if !slices.Equal(tracker.readPaths, []string{"a.txt"}) {
		t.Fatalf("tracker readPaths = %v, want [a.txt]", tracker.readPaths)
	}

	active := run.beginOpTracker()
	run.noteInputRedirectRead(nil, "b.txt")
	if !slices.Equal(active.readPaths, []string{"b.txt"}) {
		t.Fatalf("active tracker readPaths = %v, want [b.txt]", active.readPaths)
	}
	if run.provenance.pipedExternalOutput {
		t.Fatal("active tracker short-circuit must not mark piped external output")
	}

	store := newFakeProvenanceStore()
	store.seed("sess-1", "alpha.txt", "https://ext.example/a.txt")
	storeRun := &programRun{
		tc:       tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "sess-1", RootSessionID: "chat-9"}},
		sessions: store,
	}
	storeRun.noteInputRedirectRead(nil, "alpha.txt")
	if !storeRun.provenance.pipedExternalOutput {
		t.Fatal("custody-backed redirect read must mark piped external output")
	}
	if !slices.Equal(storeRun.provenance.externalURLs, []string{"https://ext.example/a.txt"}) {
		t.Fatalf("externalURLs = %v, want [https://ext.example/a.txt]", storeRun.provenance.externalURLs)
	}
}

func TestWrapProducerStreamsCountsAndFinishes(t *testing.T) {
	run := &programRun{}
	tracker := run.beginOpTracker()
	var outBuf, errBuf bytes.Buffer
	streams := commandprogram.Streams{Output: &outBuf, Error: &errBuf}
	wrapped, finish := run.wrapProducerStreams(context.Background(), tracker,
		outputStreamClass{topLevel: true}, outputStreamClass{piped: true}, streams)
	if _, err := wrapped.Output.Write([]byte("hello")); err != nil {
		t.Fatalf("wrapped output write: %v", err)
	}
	if _, err := wrapped.Error.Write([]byte("oops")); err != nil {
		t.Fatalf("wrapped error write: %v", err)
	}
	finish()
	if len(run.provenance.activeTrackers) != 0 {
		t.Fatalf("activeTrackers after finish = %d, want 0", len(run.provenance.activeTrackers))
	}
}

func TestFinishOpTrackerRouting(t *testing.T) {
	store := newFakeProvenanceStore()
	store.seed("sess-1", "alpha.txt", "https://ext.example/a.txt")
	run := &programRun{
		tc:       tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "sess-1"}},
		sessions: store,
	}

	tracker := run.beginOpTracker()
	tracker.addExchange(evidence.ExchangeProvenance{
		External:     true,
		ObservedURLs: []string{"https://ext.example/dl.txt"},
		WroteStdout:  true,
		WrittenFiles: []string{"dl.txt"},
	})
	tracker.addExchange(evidence.ExchangeProvenance{
		External:     false,
		WrittenFiles: []string{"local.txt"},
	})
	tracker.addReadPath("alpha.txt")
	run.finishOpTracker(context.Background(), tracker, outputStreamClass{topLevel: true}, true, outputStreamClass{}, false)

	if len(run.provenance.activeTrackers) != 0 {
		t.Fatalf("activeTrackers after finish = %d, want 0", len(run.provenance.activeTrackers))
	}
	if !run.provenance.directExternalOutput {
		t.Fatal("external stdout exchange must mark direct external output")
	}
	wantURLs := []string{"https://ext.example/dl.txt", "https://ext.example/a.txt"}
	if !slices.Equal(run.provenance.externalURLs, wantURLs) {
		t.Fatalf("externalURLs = %v, want %v", run.provenance.externalURLs, wantURLs)
	}
	if got := run.provenance.custodyFiles["dl.txt"]; !slices.Equal(got, []string{"https://ext.example/dl.txt"}) {
		t.Fatalf("custodyFiles[dl.txt] = %v, want the exchange URLs", got)
	}
	if run.provenance.clearedFiles["local.txt"] != true {
		t.Fatalf("clearedFiles = %v, want local.txt cleared by non-external exchange", run.provenance.clearedFiles)
	}
}

func TestFinishOpTrackerRedirectFallback(t *testing.T) {
	run := &programRun{}
	tracker := run.beginOpTracker()
	redirect := outputStreamClass{redirectPath: "out.txt", redirectOperator: ">"}

	run.finishOpTracker(context.Background(), tracker, redirect, true, outputStreamClass{}, false)
	if run.provenance.clearedFiles["out.txt"] != true {
		t.Fatalf("clearedFiles = %v, want out.txt cleared by > redirect", run.provenance.clearedFiles)
	}

	run2 := &programRun{}
	run2.markPipedExternalOutput([]string{"https://piped.example/x.txt"})
	tracker2 := run2.beginOpTracker()
	run2.finishOpTracker(context.Background(), tracker2, redirect, true, outputStreamClass{}, false)
	if got := run2.provenance.custodyFiles["out.txt"]; !slices.Equal(got, []string{"https://piped.example/x.txt"}) {
		t.Fatalf("custodyFiles[out.txt] = %v, want piped URLs attached", got)
	}

	run3 := &programRun{}
	tracker3 := run3.beginOpTracker()
	run3.finishOpTracker(context.Background(), tracker3, redirect, false, outputStreamClass{}, false)
	if _, ok := run3.provenance.clearedFiles["out.txt"]; ok {
		t.Fatal("silent redirect must not clear custody")
	}
}

func TestRouteExternalStreamClasses(t *testing.T) {
	run := &programRun{}
	run.routeExternalStream(outputStreamClass{}, []string{"https://x.example"})
	if run.provenance.directExternalOutput || run.provenance.pipedExternalOutput || len(run.provenance.custodyFiles) != 0 {
		t.Fatal("unclassified stream with URLs must not route anywhere")
	}

	run.routeExternalStream(outputStreamClass{topLevel: true}, []string{"https://x.example/top"})
	if !run.provenance.directExternalOutput || !slices.Equal(run.provenance.externalURLs, []string{"https://x.example/top"}) {
		t.Fatalf("top-level routing = direct:%v urls:%v", run.provenance.directExternalOutput, run.provenance.externalURLs)
	}

	run.routeExternalStream(outputStreamClass{redirectPath: "out.bin"}, []string{"https://x.example/bin"})
	if got := run.provenance.custodyFiles["out.bin"]; !slices.Equal(got, []string{"https://x.example/bin"}) {
		t.Fatalf("redirect routing custody = %v", got)
	}

	run.routeExternalStream(outputStreamClass{piped: true}, []string{"https://x.example/pipe"})
	if !run.provenance.pipedExternalOutput {
		t.Fatal("piped routing must mark piped external output")
	}
}

func TestCustodyFileStateAndLookup(t *testing.T) {
	run := &programRun{}
	run.addCustodyFile("", []string{"https://x.example"})
	run.addCustodyFile("f.txt", nil)
	if len(run.provenance.custodyFiles) != 0 {
		t.Fatalf("empty-path/URL adds must be no-ops, got %v", run.provenance.custodyFiles)
	}

	run.addCustodyFile("f.txt", []string{"https://x.example/1"})
	run.addCustodyFile("f.txt", []string{"https://x.example/1", " https://x.example/2 "})
	if got := run.provenance.custodyFiles["f.txt"]; !slices.Equal(got, []string{"https://x.example/1", "https://x.example/2"}) {
		t.Fatalf("custodyFiles[f.txt] = %v, want deduped trimmed URLs", got)
	}

	run.clearCustodyFile("")
	run.clearCustodyFile("f.txt")
	if _, ok := run.provenance.custodyFiles["f.txt"]; ok {
		t.Fatal("cleared file must leave custodyFiles")
	}
	if run.provenance.clearedFiles["f.txt"] != true {
		t.Fatal("cleared file must be recorded in clearedFiles")
	}
	run.addCustodyFile("f.txt", []string{"https://x.example/3"})
	if run.provenance.clearedFiles["f.txt"] {
		t.Fatal("re-adding custody must clear the cleared flag")
	}

	if got := run.lookupCustodyURLs(context.Background(), []string{"f.txt", ""}); !slices.Equal(got, []string{"https://x.example/3"}) {
		t.Fatalf("in-process lookup = %v, want [https://x.example/3]", got)
	}

	store := newFakeProvenanceStore()
	store.seed("sess-1", "alpha.txt", "https://ext.example/a.txt")
	store.seed("chat-9", "beta.txt", "https://ext.example/b.txt")
	storeRun := &programRun{
		tc:       tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "sess-1", RootSessionID: "chat-9"}},
		sessions: store,
	}
	got := storeRun.lookupCustodyURLs(context.Background(), []string{"alpha.txt", "beta.txt"})
	if !slices.Equal(got, []string{"https://ext.example/a.txt", "https://ext.example/b.txt"}) {
		t.Fatalf("store-backed lookup = %v, want both session and chat custody", got)
	}

	cleared := &programRun{sessions: store}
	cleared.clearCustodyFile("alpha.txt")
	if got := cleared.lookupCustodyURLs(context.Background(), []string{"alpha.txt"}); len(got) != 0 {
		t.Fatalf("cleared path lookup = %v, want empty (cleared paths skip the store)", got)
	}
}

func TestNoteNativeFileMutationCustody(t *testing.T) {
	var nilRun *programRun
	nilRun.noteNativeFileMutation(context.Background(), "rm", nil)

	store := newFakeProvenanceStore()
	store.seed("sess-1", "src.txt", "https://ext.example/src.txt")
	rmRun := &programRun{
		tc:       tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "sess-1"}},
		sessions: store,
	}
	rmRun.noteNativeFileMutation(context.Background(), "rm", []commandprogram.PathTarget{{Path: "src.txt"}})
	if rmRun.provenance.clearedFiles["src.txt"] != true {
		t.Fatal("rm must clear custody for the removed path")
	}

	cpRun := &programRun{
		tc:       tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "sess-1"}},
		sessions: store,
	}
	cpRun.noteNativeFileMutation(context.Background(), "cp", []commandprogram.PathTarget{
		{Path: "src.txt"}, {Path: "copy.txt", Write: true},
	})
	if got := cpRun.provenance.custodyFiles["copy.txt"]; !slices.Equal(got, []string{"https://ext.example/src.txt"}) {
		t.Fatalf("cp custody = %v, want source URLs carried to the destination", got)
	}

	cpRun.noteNativeFileMutation(context.Background(), "mv", []commandprogram.PathTarget{
		{Path: "copy.txt"}, {Path: "moved.txt", Write: true},
	})
	if got := cpRun.provenance.custodyFiles["moved.txt"]; !slices.Equal(got, []string{"https://ext.example/src.txt"}) {
		t.Fatalf("mv custody = %v, want source URLs carried to the destination", got)
	}
	if cpRun.provenance.clearedFiles["copy.txt"] != true {
		t.Fatal("mv must clear custody at the source path")
	}

	cpRun.noteNativeFileMutation(context.Background(), "cp", []commandprogram.PathTarget{
		{Path: "clean.txt"}, {Path: "fresh.txt", Write: true},
	})
	if cpRun.provenance.clearedFiles["fresh.txt"] != true {
		t.Fatal("cp from a clean source must clear destination custody")
	}

	cpRun.noteNativeFileMutation(context.Background(), "cp", []commandprogram.PathTarget{
		{Path: "one.txt"}, {Path: "two.txt"}, {Path: "three.txt"},
	})
	if cpRun.provenance.clearedFiles["three.txt"] != true {
		t.Fatal("multi-target cp without write flags must treat the last path as destination")
	}
}

func TestAppendUniqueProvenanceStrings(t *testing.T) {
	got := appendUniqueProvenanceStrings(nil, " a ", "a", "", "b", "a")
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("appendUniqueProvenanceStrings = %v, want [a b]", got)
	}
}
