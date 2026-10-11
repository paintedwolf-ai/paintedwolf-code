package loopwake

import (
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
)

func isStaleBatchSeq(live batch.State, env anchor.Envelope) bool {
	if !env.BatchSeqSet || env.BatchSeq <= 0 {
		return false
	}
	return env.BatchSeq < live.Seq
}

func phaseAdvancedWake(in HostWakeActionableInput) bool {
	return in.Wake == anchor.PhaseAdvanced || in.Inform == anchor.PhaseAdvanced
}
