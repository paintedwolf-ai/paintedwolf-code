package native

import (
	"context"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// agentStreamStage owns one reviewed stream through publication and settlement.
type agentStreamStage struct {
	request        agentStreamRequest
	mutation       agentMutation
	captured       revisionCapture
	journal        sourceeffect.Pending
	delivery       *secretcap.PreparedDelivery
	replacement    *fseffect.Replacement
	copyBuffer     []byte
	releaseWorking func()
	releaseBefore  func()
	releasePlan    func()
	batchPrepared  bool
}

func (s *agentStreamStage) Close() {
	if s.replacement != nil {
		s.replacement.Close()
	}
	s.captured.Close()
	if s.releasePlan != nil {
		s.releasePlan()
		s.releasePlan = nil
	}
	if s.releaseBefore != nil {
		s.releaseBefore()
		s.releaseBefore = nil
	}
	if s.releaseWorking != nil {
		s.releaseWorking()
		s.releaseWorking = nil
	}
	s.copyBuffer = nil
}

// A revision body is retained only if the entire stream fits the capture limit.
type revisionCapture struct {
	buffer   *BufferedBytes
	ctx      context.Context
	memory   *toolcontract.ProgramMemory
	overflow bool
}

func (c *revisionCapture) Write(raw []byte) (int, error) {
	if c.overflow {
		return len(raw), nil
	}
	if c.buffer == nil {
		ctx := c.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		buffer, err := (ExecutionRevisions{Memory: c.memory}).NewBufferedBytes(ctx, sourceledger.MaxRevisionContentBytes, "source_revision_bytes")
		if err != nil {
			return 0, err
		}
		c.buffer = buffer
	}
	if len(c.buffer.Bytes)+len(raw) > sourceledger.MaxRevisionContentBytes {
		c.overflow = true
		c.buffer.Close()
		return len(raw), nil
	}
	return c.buffer.Write(raw)
}

func (c *revisionCapture) bytes() []byte {
	if c.overflow {
		return nil
	}
	if c.buffer == nil || c.buffer.Bytes == nil {
		return []byte{}
	}
	return c.buffer.Bytes
}

func (c *revisionCapture) Close() {
	if c.buffer != nil {
		c.buffer.Close()
	}
}

// publish commits credential delivery only after the filesystem effect committed,
// including a committed effect whose later synchronization reports an error.
func (s *agentStreamStage) publish(ctx context.Context) (fseffect.Result, error) {
	result, err := s.replacement.Commit()
	if result.Publication.Committed() {
		s.delivery.Commit(ctx)
	}
	return result, err
}
