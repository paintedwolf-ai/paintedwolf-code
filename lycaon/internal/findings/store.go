package findings

import (
	"context"
	"errors"
	"strings"
	"time"
)

// SummaryMaxChars bounds automatic peer announcements.
const SummaryMaxChars = 320

// BodyMaxBytes bounds retained detail, read through pack_board.
const BodyMaxBytes = 8192

// DefaultListCap is the default max findings returned for worklog projection.
const DefaultListCap = 20

const defaultHistoryLimit = 200

var ErrNotFound = errors.New("finding not found")

// Finding is one cross-worker note keyed by root session.
type Finding struct {
	HasBody bool
	ID      int64
	Agent   string
	Body    string
	Summary string
	Ref     string
	TS      time.Time
}

// Store records worker findings keyed by root session.
type Store interface {
	// Append reports whether it inserted a new finding.
	Append(context.Context, string, string, string, string, string) (bool, error)
	Delivery(context.Context, string) (Delivery, error)
	CommitDelivery(context.Context, string, string, Delivery) error
	Get(context.Context, string, int64) (Finding, error)
	Recent(context.Context, string, string, int64, int, time.Time) ([]Finding, int64, error)
	List(context.Context, string, int) ([]Finding, error)
}

func normalizeFindingFields(agent, summary, ref string) (string, string, string) {
	return strings.TrimSpace(agent), trimSummary(summary), strings.TrimSpace(ref)
}

func findingMatches(f Finding, agent, summary, ref string) bool {
	a, s, r := normalizeFindingFields(agent, summary, ref)
	return f.Agent == a && f.Summary == s && f.Ref == r
}

func filterSince(all []Finding, since time.Time) []Finding {
	if since.IsZero() || len(all) == 0 {
		return all
	}
	out := make([]Finding, 0, len(all))
	for _, f := range all {
		if f.TS.After(since) {
			out = append(out, f)
		}
	}
	return out
}

func trimSummary(summary string) string {
	return strings.TrimSpace(summary)
}

// Delivery retains the bounded peer context used by a successful model request.
type Delivery struct {
	Cursor int64
	Notes  []Finding
}
