package cost

// Caller bucket tags for UsageEvent (normalized at RecordUsage).
const (
	CallerCoordinator = "coordinator"
	CallerWorker      = "worker"
	CallerSummarizer  = "summarizer"
)

// normalizeCaller defaults an untagged event to the coordinator bucket.
func normalizeCaller(caller string) string {
	if caller == "" {
		return CallerCoordinator
	}
	return caller
}
