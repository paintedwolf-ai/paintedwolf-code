package api

// SourceIndexState is the readiness of the process-local project source index.
type SourceIndexState string

const (
	SourceIndexStateWarming SourceIndexState = "warming"
	SourceIndexStateReady   SourceIndexState = "ready"
	SourceIndexStateFailed  SourceIndexState = "failed"
)

// SourceWatchState says whether edits made outside the app under a root reach
// the host as they happen.
type SourceWatchState string

const (
	// SourceWatchLive means every directory under the root is observed.
	SourceWatchLive SourceWatchState = "live"
	// SourceWatchPartial means some directories are not observed; changes
	// there surface on the next inventory pass instead.
	SourceWatchPartial SourceWatchState = "partial"
	// SourceWatchFaulted means the platform stream reported an error and
	// events since then may be missing.
	SourceWatchFaulted SourceWatchState = "faulted"
	// SourceWatchUnwatched means no watcher is bound to the root.
	SourceWatchUnwatched SourceWatchState = "unwatched"
)

// SecretScreenStatus is the lifecycle of the editor-only highlighting pass.
type SecretScreenStatus string

const (
	SecretScreenPending     SecretScreenStatus = "pending"
	SecretScreenComplete    SecretScreenStatus = "complete"
	SecretScreenUnavailable SecretScreenStatus = "unavailable"
)

// SecretSpanState is what the host knows about the bytes under one span.
type SecretSpanState string

const (
	// SecretSpanTracked is a live managed capability.
	SecretSpanTracked SecretSpanState = "tracked"
	// SecretSpanRetired marks evidence without a usable reference.
	SecretSpanRetired SecretSpanState = "retired"
	// SecretSpanDetected is a catalog hit with nothing tracking it.
	SecretSpanDetected SecretSpanState = "detected"
)

// ProjectEventAction identifies project SSE lifecycle actions.
type ProjectEventAction string

const (
	ProjectEventCreated ProjectEventAction = "created"
	ProjectEventUpdated ProjectEventAction = "updated"
	ProjectEventDeleted ProjectEventAction = "deleted"
)
