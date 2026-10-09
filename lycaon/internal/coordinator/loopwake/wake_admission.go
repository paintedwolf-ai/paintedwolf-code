package loopwake

func (pending pendingLoopWake) actionableInput(sessionID string) HostWakeActionableInput {
	return HostWakeActionableInput{
		SessionID: sessionID, Wake: pending.wake, Inform: pending.inform,
		CompletingJobID: pending.completingJobID, Env: pending.env, PostTurnDrain: pending.postTurnDrain,
	}
}
