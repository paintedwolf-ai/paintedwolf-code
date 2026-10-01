package gotreesitter

type materializeReason uint8

const (
	materializeForParentReduce materializeReason = iota
	materializeForFinalTree
	materializeForNormalization
	materializeForRecovery
	materializeForQuery
	materializeForCursor
	materializeForParentAPI
	materializeForEdit
	materializeForCheckpointRebuild
)
