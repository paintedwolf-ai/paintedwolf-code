package pythondocuments

import (
	"context"
	"errors"
	"io/fs"
	"sort"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

type survivingMutation struct {
	key              CanonicalKey
	seq              uint64
	op               string // "create", "write", "delete", "move", "mkdir"
	target           pythonruntime.CanonicalTarget
	source           *pythonruntime.CanonicalTarget
	data             []byte
	hasModifiedBytes bool
	mode             fs.FileMode
	isDir            bool
	writeOriginFrame *pythonruntime.DeclaredFrame
	flushCloseFrame  *pythonruntime.DeclaredFrame
}

// Settle publishes surviving net workspace mutations in stable recorded
// mutation order when cleanCompletion is true, or discards all unpublished
// overlay mutations with zero host publication calls when cleanCompletion is
// false.
func (a *OverlayAdapter) Settle(ctx context.Context, cleanCompletion bool) (*pythonruntime.SettlementReport, error) {
	a.mu.Lock()
	mutations := a.collectSurvivingMutationsLocked()
	if !cleanCompletion {
		report := &pythonruntime.SettlementReport{
			CleanCompletion: false,
		}
		for _, m := range mutations {
			report.Discarded = append(report.Discarded, m.target)
		}
		clear(a.overlay)
		a.mu.Unlock()
		return report, nil
	}
	a.mu.Unlock()

	report := &pythonruntime.SettlementReport{
		CleanCompletion: true,
	}
	if a.store == nil || len(mutations) == 0 {
		a.mu.Lock()
		clear(a.overlay)
		a.mu.Unlock()
		return report, nil
	}

	for _, m := range mutations {
		res, baseRev, err := a.publishOne(ctx, m)
		if err != nil {
			var conflictErr *RevisionConflictError
			conflict := res.Conflict
			if conflict == nil && errors.As(err, &conflictErr) {
				c := conflictErr.Conflict
				conflict = &c
			}
			if conflict != nil {
				report.Conflicts = append(report.Conflicts, *conflict)
			}
			report.PartialFailure = err
			a.recordPublication(m, baseRev, nil, 0, conflict, len(report.Published) > 0)
			return report, err
		}

		committed := res.CommittedRevision
		change := pythonruntime.PublishedChange{
			Target:            m.target,
			Operation:         m.op,
			PreviousTarget:    m.source,
			BaseRevision:      baseRev.Identity(),
			CommittedRevision: committed.Identity(),
			BytesWritten:      res.BytesWritten,
		}
		report.Published = append(report.Published, change)
		a.recordPublication(m, baseRev, &committed, res.BytesWritten, nil, false)

		if err := a.pins.AdvanceAfterCommit(m.target, committed); err != nil {
			report.PartialFailure = err
			return report, err
		}
		if m.source != nil {
			_ = a.pins.AdvanceAfterCommit(*m.source, DocumentRevision{
				RootID: m.source.RootID,
				Branch: m.source.Branch,
				Absent: true,
			})
		}

		// Clear the committed entry from the overlay so continuing contexts
		// observe the committed state fresh.
		a.mu.Lock()
		delete(a.overlay, m.key)
		if m.source != nil {
			delete(a.overlay, CanonicalKeyOf(*m.source))
		}
		a.mu.Unlock()
	}

	return report, nil
}

func (a *OverlayAdapter) collectSurvivingMutationsLocked() []survivingMutation {
	// Identify host source paths subsumed by a surviving move so we do not
	// emit a redundant delete alongside PublishMove.
	subsumedSources := make(map[CanonicalKey]bool)
	for _, n := range a.overlay {
		if n.kind != nodeTombstone && n.originTarget != nil {
			subsumedSources[CanonicalKeyOf(*n.originTarget)] = true
		}
	}

	out := make([]survivingMutation, 0, len(a.overlay))
	for k, n := range a.overlay {
		if k.Namespace != pythonruntime.NamespaceWorkspace {
			continue
		}
		n.mu.Lock()
		kind := n.kind
		target := n.target
		data := append([]byte(nil), n.data...)
		mode := n.mode
		origin := n.originTarget
		modified := n.contentModified
		seq := n.mutationSeq
		wFrame := n.writeOriginFrame
		fcFrame := n.flushCloseFrame
		n.mu.Unlock()

		switch kind {
		case nodeTombstone:
			if subsumedSources[k] {
				continue
			}
			out = append(out, survivingMutation{
				key:              k,
				seq:              seq,
				op:               "delete",
				target:           target,
				isDir:            mode&fs.ModeDir != 0,
				writeOriginFrame: wFrame,
				flushCloseFrame:  fcFrame,
			})
		case nodeDir:
			if origin != nil {
				src := *origin
				out = append(out, survivingMutation{
					key:              k,
					seq:              seq,
					op:               "move",
					target:           target,
					source:           &src,
					mode:             mode,
					isDir:            true,
					writeOriginFrame: wFrame,
					flushCloseFrame:  fcFrame,
				})
				continue
			}
			if n.createdInOverlay {
				out = append(out, survivingMutation{
					key:              k,
					seq:              seq,
					op:               "mkdir",
					target:           target,
					mode:             mode,
					isDir:            true,
					writeOriginFrame: wFrame,
					flushCloseFrame:  fcFrame,
				})
			}
		case nodeFile, nodeSymlink:
			if origin != nil {
				src := *origin
				out = append(out, survivingMutation{
					key:              k,
					seq:              seq,
					op:               "move",
					target:           target,
					source:           &src,
					data:             data,
					hasModifiedBytes: modified,
					mode:             mode,
					writeOriginFrame: wFrame,
					flushCloseFrame:  fcFrame,
				})
				continue
			}
			if !modified && !n.createdInOverlay {
				continue
			}
			op := "write"
			if pin, ok := a.pins.Get(target); ok && pin.BaseRevision.Absent {
				op = "create"
			}
			out = append(out, survivingMutation{
				key:              k,
				seq:              seq,
				op:               op,
				target:           target,
				data:             data,
				hasModifiedBytes: true,
				mode:             mode,
				writeOriginFrame: wFrame,
				flushCloseFrame:  fcFrame,
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].seq != out[j].seq {
			return out[i].seq < out[j].seq
		}
		return out[i].key.RelPath < out[j].key.RelPath
	})
	return out
}

func (a *OverlayAdapter) pinnedBase(target pythonruntime.CanonicalTarget) DocumentRevision {
	if pin, ok := a.pins.Get(target); ok {
		return pin.BaseRevision
	}
	return DocumentRevision{
		RootID: target.RootID,
		Branch: target.Branch,
		Absent: true,
	}
}

func (a *OverlayAdapter) publishOne(ctx context.Context, m survivingMutation) (PublishResult, DocumentRevision, error) {
	baseRev := a.pinnedBase(m.target)
	switch m.op {
	case "create", "write":
		res, err := a.store.PublishWrite(ctx, PublishWriteRequest{
			Target:       m.target,
			BaseRevision: baseRev,
			Bytes:        m.data,
			Mode:         m.mode,
			Create:       m.op == "create",
		})
		return res, baseRev, err
	case "delete":
		res, err := a.store.PublishDelete(ctx, PublishDeleteRequest{
			Target:       m.target,
			BaseRevision: baseRev,
			IsDir:        m.isDir,
		})
		return res, baseRev, err
	case "move":
		srcBase := a.pinnedBase(*m.source)
		res, err := a.store.PublishMove(ctx, PublishMoveRequest{
			Source:             *m.source,
			SourceBaseRevision: srcBase,
			Target:             m.target,
			TargetBaseRevision: baseRev,
			HasModifiedBytes:   m.hasModifiedBytes,
			Bytes:              m.data,
			Mode:               m.mode,
		})
		return res, baseRev, err
	case "mkdir":
		res, err := a.store.PublishMkdir(ctx, PublishMkdirRequest{
			Target:       m.target,
			BaseRevision: baseRev,
			Mode:         m.mode,
		})
		return res, baseRev, err
	default:
		return PublishResult{}, baseRev, errors.New("unsupported settlement mutation operation: " + m.op)
	}
}

func (a *OverlayAdapter) recordPublication(m survivingMutation, baseRev DocumentRevision, committed *DocumentRevision, bytesWritten int64, conflict *pythonruntime.RevisionConflict, partialFailure bool) {
	if a.evidence == nil {
		return
	}
	a.mu.Lock()
	opID := a.allocOpIDLocked()
	a.mu.Unlock()
	var prevCopy *pythonruntime.CanonicalTarget
	if m.source != nil {
		c := *m.source
		prevCopy = &c
	}
	a.evidence.RecordPublication(PublicationRecord{
		ExecutionID:       a.executionID,
		OperationID:       opID,
		Target:            m.target,
		Operation:         m.op,
		PreviousTarget:    prevCopy,
		BaseRevision:      baseRev,
		CommittedRevision: committed,
		BytesWritten:      bytesWritten,
		Conflict:          conflict,
		PartialFailure:    partialFailure,
		WriteOriginFrame:  m.writeOriginFrame,
		FlushCloseFrame:   m.flushCloseFrame,
		RecordedAt:        a.now().UTC(),
	})
}
