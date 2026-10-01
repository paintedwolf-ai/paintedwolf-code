package logview

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Time formats a timestamp per the configured time format; exported for the TUI.
func (d Display) Time(ts time.Time) string { return d.time(ts) }

// HTTPDetail renders the full inspection of one request for the TUI detail pane.
func (d Display) HTTPDetail(w io.Writer, r HTTPRecord) {
	fmt.Fprintf(w, "%s %s\n\n", d.Status(r.Status), d.Bold(r.Method+" "+r.Path))
	fmt.Fprintf(w, "  %s %s\n", d.Dim("time      "), d.time(r.TS))
	fmt.Fprintf(w, "  %s %s\n", d.Dim("duration  "), dur(r.DurationMS))
	fmt.Fprintf(w, "  %s %s\n", d.Dim("request_id"), orDash(r.RequestID))
}

// SSEDetail renders the full inspection of one SSE event, pretty-printing the payload.
func (d Display) SSEDetail(w io.Writer, r SSERecord) {
	fmt.Fprintf(w, "%s %s\n\n", d.Topic(orDash(r.Topic)), d.Dim(d.time(r.TS)))
	if op := r.Envelope.Op(); op != "" {
		fmt.Fprintf(w, "  %s %s\n", d.Dim("op       "), op)
	}
	fmt.Fprintf(w, "  %s %s\n", d.Dim("session  "), orDash(r.SessionID))
	if r.ProjectID != "" {
		fmt.Fprintf(w, "  %s %s\n", d.Dim("project  "), r.ProjectID)
	}
	if data := prettyJSON(r.Envelope.Data); data != "" {
		fmt.Fprintf(w, "\n%s\n%s\n", d.Dim("data"), d.indentWrap(d.hlJSON(data), "  ", 0))
	}
}

// DenPerfDetail renders one Den main-thread stall/perf line for the TUI detail pane.
func (d Display) DenPerfDetail(w io.Writer, r DenPerfRecord) {
	fmt.Fprintf(w, "%s %s\n\n", d.Bold(orDash(r.Event)), d.Dim(d.time(r.TS)))
	if ms := r.StallMS(); ms > 0 {
		fmt.Fprintf(w, "  %s %dms\n", d.Dim("stall     "), ms)
	}
	if r.Channel != "" {
		fmt.Fprintf(w, "  %s %s\n", d.Dim("channel   "), r.Channel)
	}
	if recent := r.Recent(); recent != "" {
		fmt.Fprintf(w, "  %s %s\n", d.Dim("recent    "), d.plainText(recent))
	}
	if len(r.Detail) > 0 {
		raw, err := json.Marshal(r.Detail)
		if err == nil {
			if data := prettyJSON(raw); data != "" {
				fmt.Fprintf(w, "\n%s\n%s\n", d.Dim("detail"), d.indentWrap(d.hlJSON(data), "  ", 0))
			}
		}
	}
}
