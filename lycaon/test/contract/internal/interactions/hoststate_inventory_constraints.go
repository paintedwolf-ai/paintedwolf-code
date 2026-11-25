package interactions

// readerFactConstraints names, per recipient, the production source that holds
// a reader fact to one value class across every host state.
var readerFactConstraints = map[string]map[string]string{
	RecipientCoordinator: {},
	RecipientWorker:      {},
}
