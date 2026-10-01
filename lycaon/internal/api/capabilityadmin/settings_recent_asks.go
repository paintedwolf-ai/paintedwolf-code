package capabilityadmin

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/hostscope"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// ApprovalDecisionReader is the ledger's read side for the recent-asks view.
type ApprovalDecisionReader interface {
	ListApprovalDecisionsSince(ctx context.Context, since time.Time) ([]authzcontext.Event, error)
}

const (
	recentAsksDefaultDays = 7
	recentAsksMaxDays     = 90
	recentAsksSubjectCap  = 8
)

// HandleListApprovalAsks groups recent decisions for display.
// These counts do not participate in approval decisions.
func (s *Handler) HandleListApprovalAsks(w http.ResponseWriter, r *http.Request) {
	days := recentAsksDefaultDays
	if raw := strings.TrimSpace(r.URL.Query().Get("days")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > recentAsksMaxDays {
			s.responses.InvalidQueryParam(w, "days", "must be between 1 and 90")
			return
		}
		days = n
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	since := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	events, err := s.ApprovalDecisions.ListApprovalDecisionsSince(r.Context(), since)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	sessions := map[string]bool{}
	if projectID != "" {
		for _, ev := range events {
			if _, seen := sessions[ev.SessionID]; seen {
				continue
			}
			sess, err := s.Store.Get(r.Context(), ev.SessionID)
			sessions[ev.SessionID] = err == nil && sess != nil && strings.TrimSpace(sess.ProjectID) == projectID
		}
	}
	rows := aggregateRecentAsks(events, func(sessionID string) bool {
		if projectID == "" {
			return true
		}
		return sessions[sessionID]
	})
	httpio.WriteJSON(w, http.StatusOK, wire.ApprovalRecentAsksResponse{WindowDays: days, SinceAt: since, Asks: rows})
}

type recentAskAccumulator struct {
	row      wire.ApprovalRecentAskRow
	subjects []string
	hosts    []string
	hostOnly bool
}

// aggregateRecentAsks groups decisions by primary gate, newest first.
// Events without a recorded gate belong to incomplete_facts.
func aggregateRecentAsks(events []authzcontext.Event, include func(sessionID string) bool) []wire.ApprovalRecentAskRow {
	byGate := map[wire.ApprovalGate]*recentAskAccumulator{}
	for _, ev := range events {
		if include != nil && !include(ev.SessionID) {
			continue
		}
		detail := decodeEventDetail(ev.DetailJSON)
		gateID := wire.ApprovalGate(strings.TrimSpace(detail.Gate))
		if gateID == "" {
			gateID = wire.GateIncompleteFacts
		}
		acc := byGate[gateID]
		if acc == nil {
			acc = &recentAskAccumulator{row: wire.ApprovalRecentAskRow{Gate: gateID}, hostOnly: true}
			byGate[gateID] = acc
		}
		acc.row.Asks++
		if ev.Outcome == authzcontext.EventOutcomeAllowed {
			acc.row.Allowed++
		} else {
			acc.row.Denied++
		}
		at := ev.RecordedAt
		if acc.row.LastAskAt == nil || at.After(*acc.row.LastAskAt) {
			acc.row.LastAskAt = &at
		}
		if subject := strings.TrimSpace(detail.SubjectTitle); subject != "" {
			acc.subjects = append(acc.subjects, subject)
		}
		host := recentAskHost(detail)
		if host == "" {
			acc.hostOnly = false
		} else {
			acc.hosts = append(acc.hosts, host)
		}
	}
	out := make([]wire.ApprovalRecentAskRow, 0, len(byGate))
	for _, acc := range byGate {
		row := acc.row
		// Most recent first, bounded.
		for i := len(acc.subjects) - 1; i >= 0 && len(row.Subjects) < recentAsksSubjectCap; i-- {
			row.Subjects = append(row.Subjects, acc.subjects[i])
		}
		if acc.hostOnly && len(acc.hosts) > 0 {
			row.HostPattern = sharedHostPattern(acc.hosts)
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Asks != out[j].Asks {
			return out[i].Asks > out[j].Asks
		}
		return out[i].Gate < out[j].Gate
	})
	return out
}

func decodeEventDetail(raw string) authzcontext.EventDetail {
	var detail authzcontext.EventDetail
	if strings.TrimSpace(raw) == "" {
		return detail
	}
	_ = json.Unmarshal([]byte(raw), &detail)
	return detail
}

// recentAskHost is the host a network decision was about, with its tunnel port
// when the subject named one. Empty for every other kind of subject.
func recentAskHost(detail authzcontext.EventDetail) string {
	if detail.ToolName != "network" {
		return ""
	}
	subject := strings.TrimSpace(detail.SubjectTitle)
	const prefix = "Allow network: "
	if !strings.HasPrefix(subject, prefix) {
		return ""
	}
	host := strings.TrimSpace(strings.TrimPrefix(subject, prefix))
	if host == "" || strings.Contains(host, " ") {
		return ""
	}
	return host
}

// sharedHostPattern is the one family pattern every host in the row falls
// under, or empty when they do not share a registrable site and port.
func sharedHostPattern(hosts []string) string {
	pattern := ""
	for _, hostport := range hosts {
		host, port := splitHostPort(hostport)
		candidate := hostscope.Pattern(host)
		if port != 0 {
			candidate = hostscope.TunnelPattern(host, port)
		}
		if candidate == "" {
			return ""
		}
		if pattern == "" {
			pattern = candidate
			continue
		}
		if pattern != candidate {
			return ""
		}
	}
	return pattern
}

func splitHostPort(hostport string) (string, uint16) {
	site, port := hostscope.SplitTunnelPattern(hostport)
	return site, port
}
