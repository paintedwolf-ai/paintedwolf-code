package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const copyPageEntries = 32

type copyPageEntry struct {
	pair   FilePair
	source string
	stage  *agentStreamStage
	bytes  int64
}

// copyPage bounds held filesystem capabilities and intent/attribution storage.
// It owns no alternate mutation path: all streams use the shared write door.
type copyPage struct {
	operation *CopyOperation
	ctx       context.Context
	tc        tools.ToolContext
	result    *CopyResult
	entries   []copyPageEntry
	bytes     int64
	release   func()
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
	staged, result, err := p.operation.stageCopyFileWithinLimit(p.ctx, p.tc, from, resolvedMutationTarget(to), maximum)
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

func (p *copyPage) Flush() error {
	if len(p.entries) == 0 {
		return nil
	}
	defer p.clear()
	streams := make([]*agentStreamStage, len(p.entries))
	for i, entry := range p.entries {
		streams[i] = entry.stage
	}
	completions, err := publishAgentStreams(p.ctx, p.tc, streams)
	for i, completion := range completions {
		if !completion.Result.Publication.Committed() {
			continue
		}
		p.result.Effects++
		entry := p.entries[i]
		p.result.Copied = append(p.result.Copied, copyResult{From: filepath.ToSlash(entry.pair.From), To: filepath.ToSlash(entry.pair.To), Bytes: entry.bytes})
		p.operation.used.Bytes += entry.bytes
		afterSuccessfulMutation(p.ctx, p.tc, filepath.ToSlash(entry.pair.To))
		if p.operation.input.Preserve {
			preserveErr := preserveMetadata(p.ctx, p.tc, entry.source, entry.stage.request.Target)
			err = errors.Join(err, preserveErr)
		}
	}
	return err
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
	if p.release != nil {
		p.release()
		p.release = nil
	}
}
