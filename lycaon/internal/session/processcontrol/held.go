package processcontrol

import (
	"context"
	"fmt"
	"strings"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/watch"
	"github.com/lycaon/lycaon/pkg/api"
)

// HandleHeldCallSettled wakes a wait subscribed to a held call that settled.
func (m *Service) HandleHeldCallSettled(sessionID, handle string) {
	if m == nil || m.Held == nil {
		return
	}
	status, err := m.Held.Status(sessionID, handle)
	if err != nil || status.Settled == nil {
		return
	}
	digest := fmt.Sprintf("handle=%s tool=%s mode=held outcome=%s elapsed=%s",
		status.Handle, status.Tool, status.Settled.Outcome, status.Elapsed.Round(time.Millisecond))
	env := anchor.Envelope{CommandCompletionDigest: digest, Vars: map[string]any{
		"held_call_handle": status.Handle, "held_call_tool": status.Tool,
		"held_call_outcome": string(status.Settled.Outcome),
	}}
	occID := "held_done:" + strings.TrimSpace(status.Handle)
	publishedAt := time.Now().UTC()
	m.processReports.publish(sessionID, handle, processReport{
		occurrenceID: occID,
		outcome:      string(status.Settled.Outcome),
		digest:       digest,
		publishedAt:  publishedAt,
	})
	awaitstore.EmitPhaseRecord(context.Background(), awaitstore.PhaseRecord{
		SessionID:  sessionID,
		Handle:     strings.TrimSpace(handle),
		Tool:       status.Tool,
		Phase:      "process_report_published",
		Outcome:    string(status.Settled.Outcome),
		RecordedAt: publishedAt,
	})
	observeProcessReportDelivery(sessionID, strings.TrimSpace(handle), occID, "held:"+strings.TrimSpace(handle), publishedAt)
	if m.watches != nil {
		_, _ = m.watches.RecordProcessDone(context.Background(), sessionID, watch.ProcessCompletionFact{
			OccurrenceID: occID,
			Handle:       strings.TrimSpace(status.Handle),
			Outcome:      string(status.Settled.Outcome),
			Report:       digest,
		})
	}
	m.nudgeProcessWait(context.Background(), sessionID, handle, anchor.ProcessFinished, env)
}

// CompletedReport returns any retained completion report for handle.
func (m *Service) CompletedReport(sessionID, handle string) (watch.ProcessCompletionFact, bool) {
	if m == nil {
		return watch.ProcessCompletionFact{}, false
	}
	rep, ok := m.processReports.lookup(sessionID, handle)
	if !ok {
		return watch.ProcessCompletionFact{}, false
	}
	occID := rep.occurrenceID
	if occID == "" {
		occID = "process_done:" + strings.TrimSpace(handle)
	}
	outcome := rep.outcome
	if outcome == "" {
		outcome = "satisfied"
	}
	return watch.ProcessCompletionFact{
		OccurrenceID: occID,
		Handle:       strings.TrimSpace(handle),
		Outcome:      outcome,
		Code:         rep.code,
		Report:       rep.digest,
	}, true
}

// HandleState answers for a background handle of either kind.
func (m *Service) HandleState(sessionID, handle string) (known, running bool) {
	if m == nil {
		return false, false
	}
	if known, running = m.Background.State(sessionID, handle); known {
		return known, running
	}
	return m.Held.State(sessionID, handle)
}

// HandleSubject returns the readable command or tool target for a handle.
func (m *Service) HandleSubject(sessionID, handle string) string {
	if m == nil {
		return ""
	}
	if m.Background != nil {
		if cmd, err := m.Background.CommandLine(sessionID, handle); err == nil && cmd != "" {
			return cmd
		}
	}
	if m.Held != nil {
		if status, err := m.Held.Status(sessionID, handle); err == nil {
			if status.DisplayTitle != "" {
				return status.DisplayTitle
			}
			return strings.ReplaceAll(status.Tool, "_", " ")
		}
	}
	return ""
}

// HandlesRunning reports whether a listed handle still runs or awaits report publication.
func (m *Service) HandlesRunning(sessionID string, handles []string) bool {
	if m == nil {
		return false
	}
	if m.Background.HasRunningHandles(sessionID, handles) || m.Held.HasRunningHandles(sessionID, handles) {
		return true
	}
	for _, h := range bgprocess.DeliverableFinishedHandles(m.Background, sessionID, handles) {
		if _, published := m.processReports.lookup(sessionID, h); !published {
			return true
		}
	}
	return false
}

// heldCallOutput describes a held call for a client that asks for its output.
func (m *Service) heldCallOutput(sessionID, handle string) (*api.BackgroundProcessOutput, bool) {
	status, err := m.Held.Status(sessionID, handle)
	if err != nil {
		return nil, false
	}
	output := &api.BackgroundProcessOutput{ProcessID: status.Handle, Running: status.Running, Chunks: []api.BackgroundProcessChunk{}}
	if status.Settled != nil {
		code := 0
		if status.Settled.Outcome != api.ToolResultOutcomeCompleted {
			code = 1
		}
		output.ExitCode = &code
	}
	return output, true
}

func isHeldHandle(handle string) bool { return strings.HasPrefix(strings.TrimSpace(handle), "held-") }
