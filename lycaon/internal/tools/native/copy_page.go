package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

const copyPageEntries = 64

const (
	copySettlementRecords = 512
	copySettlementBytes   = 1 << 20
)

type copyPageEntry struct {
	pair   FilePair
	source string
	stage  *agentStreamStage
	bytes  int64
}

// copyPage bounds held filesystem capabilities and intent/attribution storage.
// It owns no alternate mutation path: all streams use the shared write door.
type copyPage struct {
	operation      *CopyOperation
	ctx            context.Context
	tc             tools.ToolContext
	result         *CopyResult
	entries        []copyPageEntry
	bytes          int64
	release        func()
	buffers        [copyPageEntries][]byte
	bufferReleases [copyPageEntries]func()
}

func newCopyPage(ctx context.Context, tc tools.ToolContext, operation *CopyOperation, result *CopyResult) (*copyPage, error) {
	release, err := operation.memory.ReserveProgramState(ctx, copyPageEntries*256)
	if err != nil {
		return nil, err
	}
	return &copyPage{operation: operation, ctx: ctx, tc: tc, result: result, entries: make([]copyPageEntry, 0, copyPageEntries), release: release}, nil
}

func (p *copyPage) Add(pair FilePair, maximum int64) error {
	from, err := assertMutationFileSource(p.ctx, p.operation.tool.Boundary, p.tc, pair.From, p.tc.ProfileID(), "copy", "COPY_NOT_FOUND", "COPY_IS_DIRECTORY", "COPY_SYMLINK_ESCAPE", assertCopyWritePath)
	if err != nil {
		return err
	}
	to, err := assertCopyWritePath(p.ctx, p.operation.tool.Boundary, p.tc, pair.To, p.tc.ProfileID())
	if err != nil {
		return err
	}
	return p.AddResolved(pair, from, to, maximum, false)
}

func (p *copyPage) AddResolved(pair FilePair, from, to projectpaths.Resolved, maximum int64, guaranteedNew bool) error {
	if !guaranteedNew {
		if _, err := os.Lstat(to.Abs); err == nil {
			if err := p.Flush(); err != nil {
				return err
			}
			before := len(p.result.Copied)
			err = p.operation.copyFile(p.ctx, p.tc, pair, maximum, p.result)
			if len(p.result.Copied) > before {
				p.operation.used.Bytes += p.result.Copied[len(p.result.Copied)-1].Bytes
			}
			return err
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	buffer, err := p.streamBuffer()
	if err != nil {
		return err
	}
	staged, result, err := p.operation.stageCopyFileWithinLimit(p.ctx, p.tc, from, resolvedMutationTarget(to), maximum, buffer, true)
	if err != nil {
		return err
	}
	if staged.mutation.Op != api.SourceChangeOpCreate {
		staged.Close()
		return errMutationSnapshotMoved
	}
	if err := p.operation.retainResult(p.ctx, pair); err != nil {
		staged.Close()
		return err
	}
	p.entries = append(p.entries, copyPageEntry{pair: pair, source: from.Abs, stage: staged, bytes: result.Bytes})
	p.bytes += result.Bytes
	if len(p.entries) == copyPageEntries || p.bytes >= 1<<20 {
		return p.Flush()
	}
	return nil
}

// Each live stage gets its own admitted slot. Closing a page's stages makes
// their scratch reusable by the next page without allocating another arena.
func (p *copyPage) streamBuffer() ([]byte, error) {
	index := len(p.entries)
	if p.buffers[index] == nil {
		release, err := p.operation.memory.Reserve(p.ctx, 32<<10)
		if err != nil {
			return nil, err
		}
		p.bufferReleases[index] = release
		p.buffers[index] = make([]byte, 32<<10)
	}
	return p.buffers[index], nil
}

func (p *copyPage) Flush() error {
	if len(p.entries) == 0 {
		return nil
	}
	defer p.clear()
	var retainedBytes int64
	for _, entry := range p.entries {
		retainedBytes += int64(len(entry.stage.mutation.Before) + len(entry.stage.mutation.After))
	}
	if err := p.operation.beforePublication(p.ctx, p.tc, len(p.entries), retainedBytes); err != nil {
		return err
	}
	streams := make([]*agentStreamStage, len(p.entries))
	for i, entry := range p.entries {
		streams[i] = entry.stage
	}
	completions, pending, err := publishAgentStreams(p.ctx, p.tc, p.operation.memory, streams)
	if pending != nil {
		p.operation.pending = append(p.operation.pending, pending)
		p.operation.pendingRecords += len(p.entries)
		p.operation.pendingBytes += retainedBytes
	}
	for _, entry := range p.entries {
		entry.stage.Close()
	}
	// Metadata effects need the same path reservation after content settlement.
	if p.operation.input.Preserve || p.operation.pendingRecords >= copySettlementRecords || p.operation.pendingBytes >= copySettlementBytes || err != nil {
		err = errors.Join(err, p.operation.settlePending(p.ctx, p.tc))
	}
	committedPaths := make([]string, 0, len(completions))
	for i, completion := range completions {
		if !completion.Result.Publication.Committed() {
			continue
		}
		p.result.Effects++
		entry := p.entries[i]
		toSlash := filepath.ToSlash(entry.pair.To)
		p.result.Copied = append(p.result.Copied, copyResult{From: filepath.ToSlash(entry.pair.From), To: toSlash, Bytes: entry.bytes})
		p.operation.used.Bytes += entry.bytes
		committedPaths = append(committedPaths, toSlash)
		if p.operation.input.Preserve {
			preserveErr := preserveMetadata(p.ctx, p.tc, entry.source, entry.stage.request.Target)
			err = errors.Join(err, preserveErr)
		}
	}
	if len(committedPaths) > 0 {
		afterSuccessfulMutation(p.ctx, p.tc, committedPaths...)
	}
	return err
}

func (op *CopyOperation) beforePublication(ctx context.Context, tc tools.ToolContext, records int, bytes int64) error {
	if len(op.pending) > 0 && (op.pendingRecords+records > copySettlementRecords || op.pendingBytes+bytes > copySettlementBytes) {
		if err := op.settlePending(ctx, tc); err != nil {
			return err
		}
	}
	if op.pendingRelease == nil {
		release, err := op.memory.ReserveProgramState(ctx, copySettlementRecords*32)
		if err != nil {
			return err
		}
		op.pendingRelease = release
		op.pending = make([]sourceeffect.PendingEffects, 0, copySettlementRecords)
	}
	return nil
}

func (op *CopyOperation) settlePending(ctx context.Context, tc tools.ToolContext) error {
	if len(op.pending) == 0 {
		return nil
	}
	defer func() {
		for i, pending := range op.pending {
			pending.Close()
			op.pending[i] = nil
		}
		op.pending = op.pending[:0]
		op.pendingRecords, op.pendingBytes = 0, 0
	}()
	return tc.Source.SourceMutations.SettleEffects(context.WithoutCancel(ctx), op.pending)
}

func (p *copyPage) clear() {
	for i := range p.entries {
		p.entries[i].stage.Close()
		p.entries[i] = copyPageEntry{}
	}
	p.entries = p.entries[:0]
	p.bytes = 0
}

func (p *copyPage) Close() {
	p.clear()
	for i, release := range p.bufferReleases {
		if release != nil {
			release()
		}
		p.bufferReleases[i], p.buffers[i] = nil, nil
	}
	if p.release != nil {
		p.release()
		p.release = nil
	}
}
