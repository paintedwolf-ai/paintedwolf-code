package drafts

// Actor identifies the trusted writer of a session workflow.
type Actor string

const (
	User        Actor = "user"
	Coordinator Actor = "coordinator"
)

// IsActor admits the explicit user and coordinator writer identities.
func IsActor(actor Actor) bool { return actor == User || actor == Coordinator }
