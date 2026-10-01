package logview

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Filter narrows record lists for the list-style views.
type Filter struct {
	Session string
	Agent   string
	Surface string
	Topic   string
	Limit   int  // keep only the last N rows (0 = all)
	Errors  bool // HTTP: status >= 400 only
}

func (f Filter) matchLLM(r LLMRecord) bool {
	if f.Session != "" && !strings.HasPrefix(r.SessionID, f.Session) {
		return false
	}
	if f.Agent != "" && r.AgentType != f.Agent {
		return false
	}
	if f.Surface != "" && r.Surface != f.Surface {
		return false
	}
	return true
}

func lastN[T any](xs []T, n int) []T {
	if n > 0 && len(xs) > n {
		return xs[len(xs)-n:]
	}
	return xs
}

// LLMRecords applies the LLM filters and row limit; shared by list and --json output.
func (f Filter) LLMRecords(recs []LLMRecord) []LLMRecord {
	var out []LLMRecord
	for _, r := range recs {
		if f.matchLLM(r) {
			out = append(out, r)
		}
	}
	return lastN(out, f.Limit)
}

// HTTPRecords applies the errors filter and row limit.
func (f Filter) HTTPRecords(recs []HTTPRecord) []HTTPRecord {
	var out []HTTPRecord
	for _, r := range recs {
		if f.Errors && r.Status < 400 {
			continue
		}
		out = append(out, r)
	}
	return lastN(out, f.Limit)
}

// SSERecords applies the topic filter and row limit.
func (f Filter) SSERecords(recs []SSERecord) []SSERecord {
	var out []SSERecord
	for _, r := range recs {
		if f.Topic != "" && r.Topic != f.Topic {
			continue
		}
		out = append(out, r)
	}
	return lastN(out, f.Limit)
}

// RenderOverview prints a one-screen dashboard of a capture: topology, LLM/HTTP/SSE
// volume, token totals, and any HTTP errors.
func (d Display) RenderOverview(w io.Writer, c *Capture) error {
	llm, err := c.LLM()
	if err != nil {
		return err
	}
	httpRecs, err := c.HTTP()
	if err != nil {
		return err
	}
	sse, err := c.SSE()
	if err != nil {
		return err
	}
	sessions, err := c.Sessions()
	if err != nil {
		return err
	}

	fmt.Fprintln(w, d.Bold("Capture")+"  "+c.Dir)
	if span := d.timeSpan(llm, httpRecs, sse); span != "" {
		fmt.Fprintln(w, d.Dim("span     ")+span)
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, d.Bold("Sessions"))
	d.renderTopology(w, sessions, true)
	fmt.Fprintln(w)

	fmt.Fprintln(w, d.Bold("LLM"))
	d.renderLLMSummary(w, llm)
	fmt.Fprintln(w)

	fmt.Fprintln(w, d.Bold("HTTP"))
	d.renderHTTPSummary(w, httpRecs)
	fmt.Fprintln(w)

	fmt.Fprintln(w, d.Bold("SSE"))
	d.renderSSESummary(w, sse)
	return nil
}

func (d Display) renderLLMSummary(w io.Writer, recs []LLMRecord) {
	if len(recs) == 0 {
		fmt.Fprintln(w, d.Dim("  (no LLM calls captured)"))
		return
	}
	var prompt, completion int
	bySurface := map[string]int{}
	for _, r := range recs {
		if r.Usage != nil {
			prompt += r.Usage.PromptTokens
			completion += r.Usage.CompletionTokens
		}
		key := orDash(r.Surface) + " · " + orDash(r.AgentType)
		bySurface[key]++
	}
	fmt.Fprintf(w, "  %d calls    %s %s prompt   %s %s completion\n",
		len(recs), d.Cyan("↑"), human(prompt), d.Cyan("↓"), human(completion))
	for _, kv := range sortedCounts(bySurface) {
		fmt.Fprintf(w, "    %s %s\n", d.Dim(fmt.Sprintf("%4d", kv.n)), kv.key)
	}
}

func (d Display) renderHTTPSummary(w io.Writer, recs []HTTPRecord) {
	if len(recs) == 0 {
		fmt.Fprintln(w, d.Dim("  (no HTTP requests captured)"))
		return
	}
	var errs []HTTPRecord
	worst := 200
	for _, r := range recs {
		if r.Status >= 400 {
			errs = append(errs, r)
			if r.Status > worst {
				worst = r.Status
			}
		}
	}
	label := fmt.Sprintf("%d errors", len(errs))
	switch {
	case len(errs) == 0:
		label = d.Green(label)
	case worst >= 500:
		label = d.Red(label)
	default:
		label = d.Yellow(label)
	}
	fmt.Fprintf(w, "  %d requests    %s\n", len(recs), label)
	for _, r := range lastN(errs, 10) {
		fmt.Fprintf(w, "    %s %s %s %s\n", d.Dim(d.time(r.TS)), d.Status(r.Status), r.Method, r.Path)
	}
}

func (d Display) renderSSESummary(w io.Writer, recs []SSERecord) {
	if len(recs) == 0 {
		fmt.Fprintln(w, d.Dim("  (no SSE events captured)"))
		return
	}
	byTopic := map[string]int{}
	for _, r := range recs {
		byTopic[r.Topic]++
	}
	fmt.Fprintf(w, "  %d events\n", len(recs))
	for _, kv := range sortedCounts(byTopic) {
		fmt.Fprintf(w, "    %s %s\n", d.Dim(fmt.Sprintf("%5d", kv.n)), d.Topic(kv.key))
	}
}

// RenderLLMList prints one compact row per LLM call with a content snippet, applying
// filters but keeping each row's stable global index so `logs prompt N` matches.
func (d Display) RenderLLMList(w io.Writer, recs []LLMRecord, f Filter) error {
	rows := make([]string, 0, len(recs))
	for i, r := range recs {
		if !f.matchLLM(r) {
			continue
		}
		rows = append(rows, d.LLMRow(i+1, r))
	}
	rows = lastN(rows, f.Limit)
	if len(rows) == 0 {
		fmt.Fprintln(w, d.Dim("no matching LLM calls"))
		return nil
	}
	for _, row := range rows {
		fmt.Fprintln(w, row)
	}
	return nil
}

// LLMRow formats one call as a single compact line; reused by the TUI list.
func (d Display) LLMRow(idx int, r LLMRecord) string {
	tok := "—"
	if r.Usage != nil {
		tok = fmt.Sprintf("↑%d ↓%d", r.Usage.PromptTokens, r.Usage.CompletionTokens)
	}
	return fmt.Sprintf("%s %s  %s %s %s  %s  %s  %s",
		d.Dim(fmt.Sprintf("#%-4d", idx)),
		d.Dim(d.time(r.TS)),
		d.Cyan(orDash(r.Surface)),
		d.BoldCyan(orDash(r.AgentType)),
		d.Dim(fmt.Sprintf("i%d", r.Iteration)),
		d.Dim(tok),
		d.Dim("tools="+strconv.Itoa(len(r.ToolNames))),
		d.Dim(d.lastContentSnippet(r, 60)),
	)
}

// lastContentSnippet summarizes a call by its final transcript turn — a tool call
// the model just requested, or the trailing message text.
func (d Display) lastContentSnippet(r LLMRecord, max int) string {
	if len(r.Messages) == 0 {
		return ""
	}
	last := r.Messages[len(r.Messages)-1]
	if len(last.ToolCalls) > 0 {
		names := make([]string, 0, len(last.ToolCalls))
		for _, tc := range last.ToolCalls {
			names = append(names, tc.Name)
		}
		return "→ " + strings.Join(names, ", ")
	}
	return truncate(oneLine(d.text(last.Content)), max)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return runeclamp.ClampBytes(s, max)
}

// RenderHTTP prints colored request rows, optionally errors-only.
func (d Display) RenderHTTP(w io.Writer, recs []HTTPRecord, f Filter) error {
	rows := f.HTTPRecords(recs)
	if len(rows) == 0 {
		fmt.Fprintln(w, d.Dim("no matching HTTP requests"))
		return nil
	}
	for _, r := range rows {
		fmt.Fprintln(w, d.HTTPRow(r))
	}
	return nil
}

// HTTPRow formats one request line; reused by the TUI list.
func (d Display) HTTPRow(r HTTPRecord) string {
	return fmt.Sprintf("%s %s %-6s %-7s %s",
		d.Dim(d.time(r.TS)), d.Status(r.Status), r.Method, d.Dim(dur(r.DurationMS)), r.Path)
}

// RenderDenPerf prints Den main-thread stall/perf rows.
func (d Display) RenderDenPerf(w io.Writer, recs []DenPerfRecord, minMS int) error {
	rows := filterDenPerf(recs, minMS)
	if len(rows) == 0 {
		fmt.Fprintln(w, d.Dim("no matching Den perf events"))
		return nil
	}
	for _, r := range rows {
		fmt.Fprintln(w, d.DenPerfRow(r))
	}
	return nil
}

// DenPerfRow formats one stall/perf line; reused by the TUI list.
func (d Display) DenPerfRow(r DenPerfRecord) string {
	ms := r.StallMS()
	msCol := "—"
	if ms > 0 {
		msCol = fmt.Sprintf("%dms", ms)
	}
	line := fmt.Sprintf("%s %-12s %-8s %s",
		d.Dim(d.time(r.TS)), r.Event, d.Dim(msCol), truncate(r.Recent(), 72))
	if r.IsStall() && ms >= 1000 {
		return d.Red(line)
	}
	if r.IsStall() && ms >= 500 {
		return d.Yellow(line)
	}
	return line
}

func filterDenPerf(recs []DenPerfRecord, minMS int) []DenPerfRecord {
	if minMS <= 0 {
		return recs
	}
	out := make([]DenPerfRecord, 0, len(recs))
	for _, r := range recs {
		if r.StallMS() >= minMS {
			out = append(out, r)
		}
	}
	return out
}

// RenderSSE prints colored event rows, optionally filtered to one topic.
func (d Display) RenderSSE(w io.Writer, recs []SSERecord, f Filter) error {
	rows := f.SSERecords(recs)
	if len(rows) == 0 {
		fmt.Fprintln(w, d.Dim("no matching SSE events"))
		return nil
	}
	for _, r := range rows {
		fmt.Fprintln(w, d.SSERow(r))
	}
	return nil
}

// SSERow formats one event line; reused by the TUI list.
func (d Display) SSERow(r SSERecord) string {
	op := r.Envelope.Op()
	if op != "" {
		op = " " + d.Dim(op)
	}
	return fmt.Sprintf("%s %-9s%s %s",
		d.Dim(d.time(r.TS)), d.Topic(orDash(r.Topic)), op, d.Dim(shortID(r.SessionID)))
}

// RenderSessions prints the coordinator/worker topology tree.
func (d Display) RenderSessions(w io.Writer, recs []SessionRecord) error {
	if len(recs) == 0 {
		fmt.Fprintln(w, d.Dim("no sessions captured"))
		return nil
	}
	d.renderTopology(w, recs, false)
	return nil
}

// renderTopology prints the session tree. In compact mode (the dashboard) runs of
// same-typed worker children are collapsed to a count; full mode prints every
// session with its task line.
func (d Display) renderTopology(w io.Writer, recs []SessionRecord, compact bool) {
	if len(recs) == 0 {
		fmt.Fprintln(w, d.Dim("  (no sessions captured)"))
		return
	}
	children := map[string][]SessionRecord{}
	byID := map[string]SessionRecord{}
	var roots []SessionRecord
	for _, r := range recs {
		byID[r.SessionID] = r
		if r.ParentSessionID == "" {
			roots = append(roots, r)
		} else {
			children[r.ParentSessionID] = append(children[r.ParentSessionID], r)
		}
	}
	// A child whose parent was not itself captured is promoted to a root so it
	// still shows up rather than vanishing.
	for _, r := range recs {
		if r.ParentSessionID != "" {
			if _, ok := byID[r.ParentSessionID]; !ok {
				roots = append(roots, r)
			}
		}
	}
	for _, root := range roots {
		d.printSession(w, root, children, "  ", compact)
	}
}

func (d Display) printSession(w io.Writer, r SessionRecord, children map[string][]SessionRecord, indent string, compact bool) {
	label := d.BoldCyan(orDash(r.AgentType))
	meta := d.Dim(shortID(r.SessionID))
	if r.Surface != "" {
		meta += d.Dim(" · " + r.Surface)
	}
	fmt.Fprintf(w, "%s%s %s\n", indent, label, meta)
	kids := children[r.SessionID]
	if !compact {
		if task := cleanTask(r.Task); task != "" {
			fmt.Fprintf(w, "%s%s\n", indent+"  ", d.Dim(truncate(task, 100)))
		}
		for _, child := range kids {
			d.printSession(w, child, children, indent+"    ", compact)
		}
		return
	}
	// Compact: collapse same-typed leaf workers into one counted line.
	for _, grp := range groupByAgent(kids) {
		if grp.n == 1 {
			d.printSession(w, grp.sample, children, indent+"    ", compact)
			continue
		}
		fmt.Fprintf(w, "%s%s %s\n", indent+"    ",
			d.BoldCyan(orDash(grp.sample.AgentType)), d.Dim(fmt.Sprintf("× %d", grp.n)))
	}
}

type agentGroup struct {
	sample SessionRecord
	n      int
}

func groupByAgent(recs []SessionRecord) []agentGroup {
	order := []string{}
	groups := map[string]*agentGroup{}
	for _, r := range recs {
		g, ok := groups[r.AgentType]
		if !ok {
			g = &agentGroup{sample: r}
			groups[r.AgentType] = g
			order = append(order, r.AgentType)
		}
		g.n++
	}
	out := make([]agentGroup, 0, len(order))
	for _, k := range order {
		out = append(out, *groups[k])
	}
	return out
}

// cleanTask strips the leading worker-assignment HTML marker comment so the task
// summary reads as prose.
func cleanTask(task string) string {
	t := strings.TrimSpace(task)
	if strings.HasPrefix(t, "<!--") {
		if end := strings.Index(t, "-->"); end >= 0 {
			t = strings.TrimSpace(t[end+3:])
		}
	}
	return oneLine(t)
}

type countKV struct {
	key string
	n   int
}

func sortedCounts(m map[string]int) []countKV {
	out := make([]countKV, 0, len(m))
	for k, v := range m {
		out = append(out, countKV{key: k, n: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].n != out[j].n {
			return out[i].n > out[j].n
		}
		return out[i].key < out[j].key
	})
	return out
}

func (d Display) timeSpan(llm []LLMRecord, httpRecs []HTTPRecord, sse []SSERecord) string {
	var first, last string
	consider := func(c string) {
		if c == "" {
			return
		}
		if first == "" || c < first {
			first = c
		}
		if c > last {
			last = c
		}
	}
	for _, r := range llm {
		consider(d.time(r.TS))
	}
	for _, r := range httpRecs {
		consider(d.time(r.TS))
	}
	for _, r := range sse {
		consider(d.time(r.TS))
	}
	if first == "" {
		return ""
	}
	return first + " → " + last
}

// human formats a token count with thousands separators.
func human(n int) string {
	s := strconv.Itoa(n)
	if n < 1000 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, ",")
}
