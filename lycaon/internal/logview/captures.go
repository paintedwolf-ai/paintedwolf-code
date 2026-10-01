package logview

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/debugpaths"
)

// CaptureSummary is a lightweight overview of one capture for the picker. It is
// built from the small sessions.jsonl alone (plus a cheap den-perf peek for the
// stall badge), never parsing large LLM payloads, so listing stays fast.
type CaptureSummary struct {
	Name        string // capture dir name (a timestamp)
	Dir         string // full path
	Headline    string // the user's request that started the session
	AgentCount  int    // total sessions (coordinator + workers)
	WorkerCount int
	StallCount  int       // Den main-thread stalls in den-perf.jsonl
	WorstLagMS  int       // worst stall duration (ms)
	When        time.Time // parsed from the capture name
}

// ListCaptureSummaries returns a summary per capture, newest first.
func ListCaptureSummaries() ([]CaptureSummary, error) {
	names, err := SessionDirs()
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	out := make([]CaptureSummary, 0, len(names))
	for _, name := range names {
		dir := debugpaths.SessionDir(name)
		sessions, err := decodeJSONL[SessionRecord](filepath.Join(dir, debugpaths.Name(debugpaths.KindSessions)))
		if err != nil || len(sessions) == 0 {
			continue
		}
		s := summarizeCapture(name, dir, sessions)
		enrichStallBadge(&s, preferCaptureFile(dir, debugpaths.KindDenPerf))
		out = append(out, s)
	}
	return out, nil
}

func summarizeCapture(name, dir string, sessions []SessionRecord) CaptureSummary {
	s := CaptureSummary{Name: name, Dir: dir, AgentCount: len(sessions), When: parseCaptureTime(name)}
	for _, rec := range sessions {
		if rec.ParentSessionID != "" {
			s.WorkerCount++
			continue
		}
		if s.Headline == "" {
			s.Headline = cleanTask(rec.Task)
		}
	}
	if s.Headline == "" && len(sessions) > 0 {
		s.Headline = oneLine(cleanTask(sessions[0].Task))
	}
	if s.Headline == "" {
		s.Headline = "(session)"
	}
	return s
}

func enrichStallBadge(s *CaptureSummary, denPerfPath string) {
	recs, err := loadOrEmpty[DenPerfRecord](denPerfPath)
	if err != nil || len(recs) == 0 {
		return
	}
	for _, r := range recs {
		if !r.IsStall() {
			continue
		}
		s.StallCount++
		if ms := r.StallMS(); ms > s.WorstLagMS {
			s.WorstLagMS = ms
		}
	}
}

// parseCaptureTime decodes the capture dir name (e.g. 20260622T203110Z) into a time;
// a zero time signals an unparseable name.
func parseCaptureTime(name string) time.Time {
	t, err := debugpaths.ParseSessionStamp(name)
	if err != nil {
		return time.Time{}
	}
	return t
}

// CaptureLabel formats a capture dir name as a short local timestamp for display.
func CaptureLabel(name string) string {
	if t := parseCaptureTime(name); !t.IsZero() {
		return t.Local().Format("Jan 02 15:04")
	}
	return name
}

// CaptureRow renders one capture as a single line for the picker (TUI + CLI).
func (d Display) CaptureRow(s CaptureSummary) string {
	when := "—"
	if !s.When.IsZero() {
		when = s.When.Local().Format("Jan 02 15:04")
	}
	agents := fmt.Sprintf("%d agents", s.AgentCount)
	if s.WorkerCount == 0 {
		agents = "no workers"
	}
	line := fmt.Sprintf("%s  %s  %s",
		d.Dim(when), d.BoldCyan(d.plainText(s.Headline)), d.Dim(agents))
	if s.StallCount > 0 {
		badge := fmt.Sprintf("%d stalls", s.StallCount)
		if s.WorstLagMS > 0 {
			badge = fmt.Sprintf("%d stalls · worst %dms", s.StallCount, s.WorstLagMS)
		}
		line = fmt.Sprintf("%s  %s", line, d.Yellow(badge))
	}
	return line
}

// RenderCaptureList prints all captures as plain text (the CLI captures view).
func (d Display) RenderCaptureList(w io.Writer, summaries []CaptureSummary) error {
	if len(summaries) == 0 {
		fmt.Fprintln(w, d.Dim("no captures found — run ./task den:sidecar:full-debug first"))
		return nil
	}
	fmt.Fprintln(w, d.Bold("Captured sessions")+d.Dim(fmt.Sprintf(" (%d)", len(summaries))))
	fmt.Fprintln(w)
	for _, s := range summaries {
		fmt.Fprintln(w, d.CaptureRow(s))
	}
	return nil
}
