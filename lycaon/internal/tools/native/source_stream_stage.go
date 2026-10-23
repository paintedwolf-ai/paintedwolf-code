package native

import (
	"context"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/tools"
)

// agentStreamStage owns one reviewed stream through publication and settlement.
type agentStreamStage struct {
	ctx            context.Context
	tc             tools.ToolContext
	request        agentStreamRequest
	mutation       agentMutation
	captured       revisionCapture
	journal        sourceeffect.Pending
	replacement    *fseffect.Replacement
	copyBuffer     []byte
	releaseWorking func()
	releaseBefore  func()
	batchPrepared  bool
}

func (s *agentStreamStage) Close() {
	if s.replacement != nil {
		s.replacement.Close()
	}
	s.captured.Close()
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
