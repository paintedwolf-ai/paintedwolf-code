package loopwake

import "github.com/lycaon/lycaon/pkg/api"

func (pending pendingLoopWake) actionableInput(sessionID string) HostWakeActionableInput {
	return HostWakeActionableInput{
		SessionID: sessionID, Wake: pending.wake, Inform: pending.inform,
		CompletingJobID: pending.completingJobID, Env: pending.env, PostTurnDrain: pending.postTurnDrain,
	}
}

func (p pendingLoopWake) recordPolicySkip(reason api.WakeReason) {
	state := api.WakeStateSkipped
	if reason == api.WakeReasonStaleBatch {
		state = api.WakeStateDropped
	}
	p.ticket.Record(state, reason, "", "")
}

func (p pendingLoopWake) recordLeaseRefusal(err error) {
	reason := api.WakeReasonTurnSettled
	if err != nil {
		reason = api.WakeReasonLookupFailed
	}
	p.ticket.Record(api.WakeStateDropped, reason, "", "")
}
