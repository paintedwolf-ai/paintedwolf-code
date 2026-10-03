package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// The recorder fails a selected invocation to model a partial scan.
type chunkRecorder struct {
	mu      sync.Mutex
	batches [][]string
	failOn  int
}

func (r *chunkRecorder) ID() string { return "chunky" }
func (r *chunkRecorder) Categories() []api.ScanCategory {
	return []api.ScanCategory{api.ScanCategorySAST}
}
func (r *chunkRecorder) Run(_ context.Context, req scanbase.ScanRequest) (*scanoutput.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches = append(r.batches, append([]string(nil), req.Paths...))
	if r.failOn > 0 && len(r.batches) == r.failOn {
		return nil, errors.New("engine crashed")
	}
	findings := make([]api.SecurityFinding, 0, len(req.Paths))
	for _, p := range req.Paths {
		findings = append(findings, api.SecurityFinding{RuleID: "r", Locations: []api.SecurityFindingLocation{{URI: p}}})
	}
	return &scanoutput.Result{ScannedPaths: req.Paths, FindingsCount: len(findings), Findings: findings, Raw: map[string]any{"engine": "chunky"}}, nil
}

func newChunkRunner(t *testing.T, engine *chunkRecorder, chunk int) (*Runner, *api.CodeScan) {
	t.Helper()
	store := scanbase.NewSQLStore(testdbfixture.Open(t, "chunks.db"))
	reg := &scanbase.MockRegistry{Scanners: []scanbase.CodeScanner{engine}}
	runner := NewRunner(store, reg, nil, scancfg.DefaultRunnerConfig(), nil)
	runner.ChunkFiles = chunk
	runner.DataDir = t.TempDir()
	job := &api.CodeScan{ID: "scan-1", CanonicalPath: t.TempDir(), ScannerID: engine.ID(), Categories: engine.Categories(), ClaimToken: "token", Status: api.CodeScanStatusRunning}
	testutil.FailErr(t, "insert scan", store.Insert(t.Context(), *job, nil, ""))
	return runner, job
}

func paths(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, fmt.Sprintf("src/f%03d.go", i))
	}
	return out
}

func TestRunInChunksMergesBoundedInvocations(t *testing.T) {
	engine := &chunkRecorder{}
	runner, job := newChunkRunner(t, engine, 4)
	result, err := runner.runInChunks(t.Context(), job, scanbase.ScanRequest{ProjectDir: job.CanonicalPath, ScannerID: engine.ID(), Paths: paths(10)})
	testutil.FailErr(t, "run in chunks", err)
	if len(engine.batches) != 3 || len(engine.batches[0]) != 4 || len(engine.batches[2]) != 2 {
		t.Fatalf("batches = %v, want 4,4,2", engine.batches)
	}
	if result.FindingsCount != 10 || len(result.Findings) != 10 || len(result.ScannedPaths) != 10 {
		t.Fatalf("merged result = %+v", result)
	}
	if result.Raw != nil {
		t.Fatal("engine raw output does not merge across invocations")
	}
	if _, err := os.Stat(runner.chunkDir(t.Context(), job)); !os.IsNotExist(err) {
		t.Fatal("kept chunks are removed once the scan completes")
	}
}

func TestRunInChunksResumesFromKeptChunksAfterAFailure(t *testing.T) {
	engine := &chunkRecorder{failOn: 2}
	runner, job := newChunkRunner(t, engine, 4)
	req := scanbase.ScanRequest{ProjectDir: job.CanonicalPath, ScannerID: engine.ID(), Paths: paths(10)}
	if _, err := runner.runInChunks(t.Context(), job, req); err == nil {
		t.Fatal("the second invocation was told to fail")
	}
	kept, err := os.ReadDir(runner.chunkDir(t.Context(), job))
	testutil.FailErr(t, "list kept chunks", err)
	if len(kept) != 1 || kept[0].Name() != "0.json" {
		t.Fatalf("kept = %v, want the first chunk only", kept)
	}
	engine.failOn = 0
	result, err := runner.runInChunks(t.Context(), job, req)
	testutil.FailErr(t, "resume", err)
	if len(engine.batches) != 4 {
		t.Fatalf("invocations = %d, want 4: the first chunk was not run again", len(engine.batches))
	}
	if result.FindingsCount != 10 {
		t.Fatalf("resumed result findings = %d, want 10", result.FindingsCount)
	}
}

func TestRunInChunksRecordsProgressOnTheScan(t *testing.T) {
	engine := &chunkRecorder{}
	runner, job := newChunkRunner(t, engine, 5)
	_, err := runner.runInChunks(t.Context(), job, scanbase.ScanRequest{ProjectDir: job.CanonicalPath, ScannerID: engine.ID(), Paths: paths(10)})
	testutil.FailErr(t, "run in chunks", err)
	if job.Progress == nil || job.Progress.Chunks != 2 || job.Progress.Completed != 2 || job.Progress.Files != 10 {
		t.Fatalf("progress = %+v", job.Progress)
	}
	if _, err := os.Stat(filepath.Join(runner.DataDir)); err != nil {
		t.Fatalf("data dir: %v", err)
	}
}

func TestChunkPathsSplitsEvenly(t *testing.T) {
	if got := chunkPaths(paths(7), 3); len(got) != 3 || len(got[2]) != 1 {
		t.Fatalf("chunks = %v", got)
	}
	if got := chunkPaths(nil, 3); len(got) != 1 || len(got[0]) != 0 {
		t.Fatalf("empty chunks = %v", got)
	}
}

func TestChunkResumeKeepsProjectPathsAcrossExecutionTrees(t *testing.T) {
	engine := &chunkRecorder{failOn: 2}
	runner, job := newChunkRunner(t, engine, 1)
	first := t.TempDir()
	_, err := runner.runInChunks(t.Context(), job, scanbase.ScanRequest{ProjectDir: first, Paths: []string{filepath.Join(first, "a.go"), filepath.Join(first, "b.go")}})
	if err == nil {
		t.Fatal("fixture did not interrupt after the first chunk")
	}
	engine.failOn = 0
	second := t.TempDir()
	got, err := runner.runInChunks(t.Context(), job, scanbase.ScanRequest{ProjectDir: second, Paths: []string{filepath.Join(second, "a.go"), filepath.Join(second, "b.go")}})
	testutil.FailErr(t, "resume with another execution tree", err)
	if len(got.Findings) != 2 || got.Findings[0].Locations[0].URI != "a.go" || got.Findings[1].Locations[0].URI != "b.go" {
		t.Fatalf("temporary paths survived resume: %+v", got)
	}
}
