package actioncard

import (
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// factOrder is the order facts appear on a card.
var factOrder = []api.ActionFactKind{
	api.ActionFactResult, api.ActionFactChanged, api.ActionFactFound, api.ActionFactTook, api.ActionFactSteps,
	api.ActionFactStoppedBy, api.ActionFactNetwork, api.ActionFactWorker, api.ActionFactBudget,
	api.ActionFactProcess, api.ActionFactCaptured, api.ActionFactContainment, api.ActionFactLoadedBy,
}

const maxFacts = 4

// ordered keeps the first fact of each kind, in vocabulary order, at most four.
func ordered(facts []api.ActionFact) []api.ActionFact {
	byKind := make(map[api.ActionFactKind]api.ActionFact, len(facts))
	for _, fact := range facts {
		if _, seen := byKind[fact.Kind]; !seen {
			byKind[fact.Kind] = fact
		}
	}
	out := make([]api.ActionFact, 0, min(len(byKind), maxFacts))
	for _, kind := range factOrder {
		if fact, ok := byKind[kind]; ok && len(out) < maxFacts {
			out = append(out, fact)
		}
	}
	return out
}

func resultFact(outcome string) api.ActionFact {
	return api.ActionFact{Kind: api.ActionFactResult, Outcome: outcome}
}

func exitFact(outcome string, code *int) api.ActionFact {
	fact := resultFact(outcome)
	fact.ExitCode = code
	return fact
}

func stepsFact(count, failed int, unit string) api.ActionFact {
	return api.ActionFact{Kind: api.ActionFactSteps, Count: count, Failed: failed, Unit: unit}
}

func stoppedByFact(by, code string) api.ActionFact {
	return api.ActionFact{Kind: api.ActionFactStoppedBy, By: by, Code: code}
}

func processFact(state string) api.ActionFact {
	return api.ActionFact{Kind: api.ActionFactProcess, State: state}
}

// tookFact is the elapsed time of a settled action. Descriptors are pure, so
// a running action carries no duration.
func tookFact(started time.Time, settled *time.Time) (api.ActionFact, bool) {
	if started.IsZero() || settled == nil || settled.Before(started) {
		return api.ActionFact{}, false
	}
	return api.ActionFact{Kind: api.ActionFactTook, DurationMs: settled.Sub(started).Milliseconds()}, true
}

func invocationTook(invocation *api.InvocationReceipt) (api.ActionFact, bool) {
	if invocation == nil {
		return api.ActionFact{}, false
	}
	return tookFact(invocation.StartedAt, invocation.SettledAt)
}
