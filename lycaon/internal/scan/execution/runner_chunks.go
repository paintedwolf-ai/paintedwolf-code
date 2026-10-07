package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/lycaon/lycaon/internal/fseffect"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// chunkSpillDir retains completed chunks for resumption after a restart.
const chunkSpillDir = "chunks-v2"

// chunkRecord retains private identity fingerprints omitted from public results.
type chunkRecord struct {
	Paths            []string                    `json:"paths"`
	Result           *scanoutput.Result          `json:"result"`
	SecretIdentities []scanoutput.SecretIdentity `json:"secret_identities,omitempty"`
}

// runInChunks executes the request as bounded invocations and merges them.
// A request the chunk size does not split runs as one invocation.
func (r *Runner) runInChunks(ctx context.Context, job *api.CodeScan, req scanbase.ScanRequest) (*scanoutput.Result, error) {
	size := r.chunkFiles()
	if size <= 0 || len(req.Paths) <= size {
		return r.runScanner(ctx, job, req)
	}
	chunks := chunkPaths(req.Paths, size)
	dir := r.chunkDir(ctx, job)
	merged := &scanoutput.Result{Categories: append([]api.ScanCategory(nil), job.Categories...)}
	resumed := 0
	for i, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		inputs := chunkInputPaths(req.ProjectDir, chunk)
		result, kept, err := loadChunk(dir, i, inputs)
		if err != nil {
			return nil, err
		}
		if kept {
			resumed++
		} else {
			part := req
			part.Paths = chunk
			result, err = r.runScanner(ctx, job, part)
			if err != nil {
				return nil, fmt.Errorf("chunk %d of %d: %w", i+1, len(chunks), err)
			}
			scanoutput.NormalizeResultPaths(result, req.ProjectDir)
			if err := saveChunk(dir, i, inputs, result); err != nil {
				slog.WarnContext(ctx, "keep scan chunk", "scan_id", job.ID, "chunk", i, "error", err)
			}
		}
		mergeScanResult(merged, result)
		if err := r.Store.MarkProgress(ctx, job, api.ScanProgress{Chunks: len(chunks), Completed: i + 1, Files: len(req.Paths)}); err != nil {
			slog.WarnContext(ctx, "record scan progress", "scan_id", job.ID, "error", err)
		}
	}
	if resumed > 0 {
		slog.InfoContext(ctx, "scan resumed from kept chunks", "scan_id", job.ID, "resumed", resumed, "chunks", len(chunks))
	}
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
	return merged, nil
}

func (r *Runner) chunkFiles() int {
	if r == nil {
		return 0
	}
	return r.ChunkFiles
}

// chunkDir is where this scan keeps finished chunks; empty when the scan has
// no evidence root, in which case chunks are not resumable.
func (r *Runner) chunkDir(ctx context.Context, job *api.CodeScan) string {
	root := r.hostDataDir(ctx, job)
	if root == "" {
		return ""
	}
	return filepath.Join(root, scanbase.ScanResultSpillDir, job.ID, chunkSpillDir)
}

func chunkPaths(paths []string, size int) [][]string {
	if size <= 0 || len(paths) == 0 {
		return [][]string{paths}
	}
	out := make([][]string, 0, (len(paths)+size-1)/size)
	for start := 0; start < len(paths); start += size {
		end := min(start+size, len(paths))
		out = append(out, paths[start:end])
	}
	return out
}

func chunkFile(dir string, index int) string {
	return filepath.Join(dir, strconv.Itoa(index)+".json")
}

func loadChunk(dir string, index int, inputs []string) (*scanoutput.Result, bool, error) {
	if dir == "" {
		return nil, false, nil
	}
	raw, err := os.ReadFile(chunkFile(dir, index)) // #nosec G304 -- host-managed chunk under the scan's evidence root
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var record chunkRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		// Re-run chunks that cannot be decoded.
		return nil, false, nil //nolint:nilerr // intentional: an unreadable kept chunk is simply run again
	}
	if record.Result == nil || !slices.Equal(record.Paths, inputs) {
		return nil, false, nil
	}
	record.Result.SecretIdentities = record.SecretIdentities
	return record.Result, true, nil
}

func saveChunk(dir string, index int, inputs []string, result *scanoutput.Result) error {
	if dir == "" || result == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := surveyjson.Marshal(chunkRecord{Paths: inputs, Result: result, SecretIdentities: result.SecretIdentities})
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: dir, Rel: strconv.Itoa(index) + ".json"},
		Source:   bytes.NewReader(raw), Mode: 0o600, DirMode: 0o700,
	})
	return err
}

// mergeScanResult combines structured chunk results and drops unmergeable raw output.
func mergeScanResult(into, part *scanoutput.Result) {
	if part == nil {
		return
	}
	into.ScannedPaths = append(into.ScannedPaths, part.ScannedPaths...)
	into.FindingsCount += part.FindingsCount
	for _, identity := range part.SecretIdentities {
		identity.FindingIndex += len(into.Findings)
		into.SecretIdentities = append(into.SecretIdentities, identity)
	}
	into.Findings = append(into.Findings, part.Findings...)
	into.Warnings = append(into.Warnings, part.Warnings...)
	if len(into.Categories) == 0 {
		into.Categories = append([]api.ScanCategory(nil), part.Categories...)
	}
}

// Chunk identity uses source-relative inputs, independent of the execution tree.
func chunkInputPaths(root string, paths []string) []string {
	out := make([]string, len(paths))
	for i, path := range paths {
		if filepath.IsAbs(path) {
			if rel, err := filepath.Rel(root, path); err == nil {
				path = rel
			}
		}
		out[i] = filepath.ToSlash(filepath.Clean(path))
	}
	return out
}
