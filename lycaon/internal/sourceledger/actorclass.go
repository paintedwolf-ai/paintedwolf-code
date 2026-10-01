package sourceledger

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// ActorClass expresses provenance relative to the viewing session.
// Unavailable provenance is ActorUnknown.
type ActorClass string

const (
	// ActorYou is text attributed to the viewing session.
	ActorYou ActorClass = "you"
	// ActorAgent is an agent-origin effect from a different session or worker job.
	ActorAgent ActorClass = "agent"
	// ActorUser is a human action in the app.
	ActorUser ActorClass = "user"
	// ActorExternal is an observed filesystem change with no attributed actor.
	ActorExternal ActorClass = "external"
	// ActorUnknown is recorded provenance the vocabulary cannot place.
	ActorUnknown ActorClass = "unknown"
	// ActorMixed preserves publications with more than one author class.
	ActorMixed ActorClass = "mixed"
)

// ClassifyActor places one recorded effect relative to the session viewing it.
func ClassifyActor(origin api.SourceChangeOrigin, effectSessionID, viewerSessionID string) ActorClass {
	switch origin {
	case api.SourceChangeOriginAgent:
		effectSessionID = strings.TrimSpace(effectSessionID)
		if effectSessionID != "" && effectSessionID == strings.TrimSpace(viewerSessionID) {
			return ActorYou
		}
		return ActorAgent
	case api.SourceChangeOriginUser:
		return ActorUser
	case api.SourceChangeOriginExternal:
		return ActorExternal
	default:
		return ActorUnknown
	}
}

// ActorClassFor classifies this effect for one viewing session.
func (e Effect) ActorClassFor(viewerSessionID string) ActorClass {
	if len(e.Contributors) == 0 {
		return ClassifyActor(e.Origin, e.SessionID, viewerSessionID)
	}
	actor := ClassifyActor(e.Contributors[0].Origin, e.Contributors[0].SessionID, viewerSessionID)
	for _, contributor := range e.Contributors[1:] {
		if ClassifyActor(contributor.Origin, contributor.SessionID, viewerSessionID) != actor {
			return ActorMixed
		}
	}
	return actor
}

// ActorDisplay names foreign contributors and explains mixed publications.
func (e Effect) ActorDisplay(viewerSessionID string) string {
	classification := e.ActorClassFor(viewerSessionID)
	if classification != ActorMixed && classification != ActorAgent {
		return ""
	}
	if len(e.Contributors) > 0 {
		labels := make([]string, 0, len(e.Contributors))
		seen := make(map[string]bool)
		for _, c := range e.Contributors {
			label := string(ClassifyActor(c.Origin, c.SessionID, viewerSessionID))
			if label == string(ActorAgent) {
				if classification == ActorMixed {
					label += ": " + ActorDetail(c.ActorLabel, c.JobID)
				} else {
					label = ActorDetail(c.ActorLabel, c.JobID)
				}
			}
			if !seen[label] {
				seen[label] = true
				labels = append(labels, label)
			}
		}
		return strings.Join(labels, "; ")
	}
	return ActorDetail(e.ActorLabel, e.JobID)
}

// ActorDetail names a foreign agent from its recorded fields.
func ActorDetail(actorLabel, jobID string) string {
	if label := strings.TrimSpace(actorLabel); label != "" {
		return label
	}
	if jobID != "" {
		return "worker job"
	}
	return "another session"
}

// AuthoredTurn is present only when the contribution authors agree on one turn.
func (e Effect) AuthoredTurn() int {
	if len(e.Contributors) == 0 {
		if e.Origin == api.SourceChangeOriginAgent {
			return e.Turn
		}
		return 0
	}
	first := e.Contributors[0]
	if first.Origin != api.SourceChangeOriginAgent {
		return 0
	}
	for _, c := range e.Contributors[1:] {
		if c.Origin != first.Origin || c.SessionID != first.SessionID || c.Turn != first.Turn {
			return 0
		}
	}
	return first.Turn
}
