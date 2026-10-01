// Package logscli is the shared capture log viewer for pw-logs and lycaon-debug logs.
package logscli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/logview"
	"github.com/lycaon/lycaon/internal/logview/tui"
)

func usage(prog string) string {
	return fmt.Sprintf(`usage: %s [command] [flags]

Human-friendly viewer over a full-debug capture (default: the latest session at
%s/). Run ./task den:sidecar:full-debug to capture, or enable Settings → Full
debug logging.

On an interactive terminal, a bare invocation opens the browser at the list of
all captured sessions; drill in to one session, to each agent (coordinator +
workers with their outcome), to each turn, to the prompt behind it.
Piped/redirected, it prints the capture list (or a session summary when --dir
names one).

commands:
  (none)         browser (TUI) on a TTY, else the capture list
  tui            force the interactive browser
  follow         live tail -f of the running session: tools/results/spawns as
                 they happen (--json to pipe, --agent <type> to filter)
  captures       list every captured session (newest first)
  session        summary for one capture: agents, outcomes, turn/token counts
  timeline       combined narrative: reasoning, tools, nudges, outcomes (chronological)
  problems       everything that went wrong: failures, rejections, errors
  tools          an agent's tool calls — --agent <id|type> (or all agents)
  prompt [N]     full transcript of LLM call N (default: last call)
  llm            compact list of LLM calls
  http           HTTP request log, status-colored
  sse            SSE event stream
  den-perf       Den main-thread stall/perf log
  sessions       coordinator/worker topology tree
  overview       raw volume dashboard (LLM/HTTP/SSE totals)
  dump           annotated synthetic coordinator prompt (templates/fixtures)
  config         view or change settings (get|set|path|edit)

common flags:
  --dir PATH       capture directory or session name (default: latest)
  --file PATH|-    read one stream from a JSONL file or stdin (-) instead of a capture
  --list           list available capture sessions (newest first), then exit
  --color MODE     auto|always|never (default from config)
  --no-color       shorthand for --color never
  --unescape-html  decode HTML entities in captured content
  --json           emit raw JSONL records (llm|http|sse|den-perf), for piping into jq
  -f, --follow     stream newly-captured rows (llm|http|sse|den-perf), tail -f style

filters (llm):     --session ID  --agent NAME  --surface NAME  -n N
prompt flags:      --no-system  --head N  --tools  --raw
http flags:        --errors  -n N
sse flags:         --topic NAME  -n N
dump flags:        --fixture NAME  --session ID  --previous-family FAMILY  --verify`, prog, debugpaths.LatestLabel())
}

// flags holds the parsed common + per-command flags for the logs viewer.
type flags struct {
	dir      string
	file     string
	color    string
	noColor  bool
	unescape bool
	json     bool
	follow   bool
	session  string
	agent    string
	surface  string
	topic    string
	previous string
	fixture  string
	verify   bool
	limit    int
	head     int
	errors   bool
	noSys    bool
	tools    bool
	raw      bool
	list     bool
	pos      []string // positional args (e.g. the prompt index, config key/value)
}

// Run is the logs viewer entrypoint. prog is the argv0 shown in usage.
func Run(prog string, args []string) error {
	u := usage(prog)
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		_, _ = fmt.Fprintln(os.Stdout, u)
		return nil
	}

	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}

	f, err := parseFlags(args)
	if err != nil {
		return err
	}

	cfg, err := logview.LoadConfig()
	if err != nil {
		return err
	}

	if sub == "config" {
		return runConfig(prog, f, cfg)
	}
	if f.list {
		return listCaptures()
	}

	d := f.display(cfg)
	out := bufio.NewWriter(os.Stdout)
	defer func() { _ = out.Flush() }()

	// dump renders synthetic prompts from templates and needs no capture.
	if sub == "dump" {
		return runDump(out, f)
	}

	switch sub {
	case "":
		if logview.IsTTY(os.Stdout) {
			return tui.Run(f.tuiConfig(cfg), f.dir)
		}
		if f.dir != "" {
			capt, err := logview.Resolve(f.dir)
			if err != nil {
				return err
			}
			return runSession(out, capt, d)
		}
		return runCaptures(out, d)
	case "tui":
		return tui.Run(f.tuiConfig(cfg), f.dir)
	case "captures":
		return runCaptures(out, d)
	case "session":
		capt, err := logview.Resolve(f.dir)
		if err != nil {
			return err
		}
		return runSession(out, capt, d)
	case "problems":
		capt, err := logview.Resolve(f.dir)
		if err != nil {
			return err
		}
		tree, err := capt.SessionTree()
		if err != nil {
			return err
		}
		httpRecs, err := capt.HTTP()
		if err != nil {
			return err
		}
		denPerf, err := capt.DenPerf()
		if err != nil {
			return err
		}
		return d.RenderProblems(out, logview.CollectProblems(tree, httpRecs, denPerf))
	case "overview":
		capt, err := logview.Resolve(f.dir)
		if err != nil {
			return err
		}
		return d.RenderOverview(out, capt)
	case "follow":
		return runFollow(out, f, d)
	case "timeline":
		return runTimeline(out, f, d)
	case "tools":
		return runTools(out, f, d)
	case "prompt":
		return runPrompt(out, f, d)
	case "llm":
		return runLLM(out, f, d)
	case "http":
		return runHTTP(out, f, d)
	case "sse":
		return runSSE(out, f, d)
	case "den-perf":
		return runDenPerf(out, f, d)
	case "sessions":
		capt, err := logview.Resolve(f.dir)
		if err != nil {
			return err
		}
		recs, err := capt.Sessions()
		if err != nil {
			return err
		}
		return d.RenderSessions(out, recs)
	default:
		return fmt.Errorf("unknown logs command %q\n\n%s", sub, u)
	}
}

func runLLM(out *bufio.Writer, f flags, d logview.Display) error {
	if f.follow && f.file == "" {
		return followLLM(out, f, d)
	}
	recs, err := loadStream[logview.LLMRecord](f, func(c *logview.Capture) ([]logview.LLMRecord, error) { return c.LLM() })
	if err != nil {
		return err
	}
	if f.json {
		return writeJSONL(out, f.filter().LLMRecords(recs))
	}
	return d.RenderLLMList(out, recs, f.filter())
}

func runHTTP(out *bufio.Writer, f flags, d logview.Display) error {
	if f.follow && f.file == "" {
		path, err := resolveCapturePath(f, func(c *logview.Capture) string { return c.HTTPPath })
		if err != nil {
			return err
		}
		return followRows(out, path, d.HTTPRow)
	}
	recs, err := loadStream[logview.HTTPRecord](f, func(c *logview.Capture) ([]logview.HTTPRecord, error) { return c.HTTP() })
	if err != nil {
		return err
	}
	if f.json {
		return writeJSONL(out, f.filter().HTTPRecords(recs))
	}
	return d.RenderHTTP(out, recs, f.filter())
}

func runSSE(out *bufio.Writer, f flags, d logview.Display) error {
	if f.follow && f.file == "" {
		path, err := resolveCapturePath(f, func(c *logview.Capture) string { return c.SSEPath })
		if err != nil {
			return err
		}
		return followRows(out, path, d.SSERow)
	}
	recs, err := loadStream[logview.SSERecord](f, func(c *logview.Capture) ([]logview.SSERecord, error) { return c.SSE() })
	if err != nil {
		return err
	}
	if f.json {
		return writeJSONL(out, f.filter().SSERecords(recs))
	}
	return d.RenderSSE(out, recs, f.filter())
}

func runDenPerf(out *bufio.Writer, f flags, d logview.Display) error {
	if f.follow && f.file == "" {
		path, err := resolveCapturePath(f, func(c *logview.Capture) string { return c.DenPerfPath })
		if err != nil {
			return err
		}
		return followRows(out, path, d.DenPerfRow)
	}
	recs, err := loadStream[logview.DenPerfRecord](f, func(c *logview.Capture) ([]logview.DenPerfRecord, error) { return c.DenPerf() })
	if err != nil {
		return err
	}
	if f.json {
		return writeJSONL(out, recs)
	}
	return d.RenderDenPerf(out, recs, 0)
}

func runPrompt(out *bufio.Writer, f flags, d logview.Display) error {
	recs, err := loadStream[logview.LLMRecord](f, func(c *logview.Capture) ([]logview.LLMRecord, error) { return c.LLM() })
	if err != nil {
		return err
	}
	index := 0
	if len(f.pos) > 0 {
		n, err := strconv.Atoi(f.pos[0])
		if err != nil {
			return fmt.Errorf("prompt index must be a number, got %q", f.pos[0])
		}
		index = n
	}
	rec, resolved, err := logview.SelectCall(recs, index)
	if err != nil {
		return err
	}
	if f.raw || f.json {
		return logview.WriteRawCall(out, rec)
	}
	return d.RenderPrompt(out, rec, resolved, logview.PromptOptions{
		HideSystem: f.noSys,
		HeadLines:  f.head,
		ShowTools:  f.tools,
	})
}

func runSession(out *bufio.Writer, capt *logview.Capture, d logview.Display) error {
	tree, err := capt.SessionTree()
	if err != nil {
		return err
	}
	return d.RenderSessionSummary(out, tree)
}

// runFollow streams a unified, live event tail of the newest running session —
// session/spawn/tool/result/done/reject events as they happen — re-attaching to the
// newest capture if the sidecar restarts. Ctrl-C to stop. --json emits one event
// per line; --agent filters to one agent type.
func runFollow(out *bufio.Writer, f flags, d logview.Display) error {
	enc := json.NewEncoder(out)
	eng := logview.NewFollowEngine()
	var (
		curDir   string
		llmTail  *logview.Tailer[logview.LLMRecord]
		sessTail *logview.Tailer[logview.SessionRecord]
	)
	for {
		if newest, err := logview.NewestCapture(); err == nil && newest.Dir != curDir {
			curDir = newest.Dir
			llmTail = logview.NewTailer[logview.LLMRecord](newest.LLMPath, false)
			sessTail = logview.NewTailer[logview.SessionRecord](newest.SessionsPath, false)
			eng = logview.NewFollowEngine()
			fmt.Fprintf(os.Stderr, "— following %s (Ctrl-C to stop) —\n", filepathBaseCLI(newest.Dir))
		}

		var events []logview.Event
		if sessTail != nil {
			if recs, err := sessTail.Poll(); err == nil {
				events = append(events, eng.SessionEvents(recs)...)
			}
			if recs, err := llmTail.Poll(); err == nil {
				events = append(events, eng.LLMEvents(recs)...)
			}
		}
		sort.SliceStable(events, func(i, j int) bool { return events[i].Time.Before(events[j].Time) })
		for _, ev := range events {
			if f.agent != "" && ev.Agent != f.agent {
				continue
			}
			if f.json {
				if err := enc.Encode(ev); err != nil {
					return err
				}
			} else {
				fmt.Fprintln(out, d.EventLine(ev))
			}
		}
		_ = out.Flush()
		time.Sleep(500 * time.Millisecond)
	}
}

func filepathBaseCLI(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// runTimeline prints the combined session timeline — every agent's reasoning,
// tools, nudges, and outcomes in chronological order (the static form of `follow`).
func runTimeline(out *bufio.Writer, f flags, d logview.Display) error {
	capt, err := logview.Resolve(f.dir)
	if err != nil {
		return err
	}
	sessions, err := capt.Sessions()
	if err != nil {
		return err
	}
	llm, err := capt.LLM()
	if err != nil {
		return err
	}
	denPerf, err := capt.DenPerf()
	if err != nil {
		return err
	}
	events := logview.BuildEventLog(sessions, llm, denPerf)
	if f.json {
		enc := json.NewEncoder(out)
		for _, ev := range events {
			if err := enc.Encode(ev); err != nil {
				return err
			}
		}
		return nil
	}
	for _, ev := range events {
		fmt.Fprintln(out, d.EventLine(ev))
	}
	return nil
}

// runTools lists an agent's tool calls. Select the agent with --agent <id|type>
// (session-id prefix, or agent type); omit it to list every agent's tools.
func runTools(out *bufio.Writer, f flags, d logview.Display) error {
	capt, err := logview.Resolve(f.dir)
	if err != nil {
		return err
	}
	tree, err := capt.SessionTree()
	if err != nil {
		return err
	}
	sel := strings.TrimSpace(f.agent)
	if sel == "" {
		sel = strings.TrimSpace(f.session)
	}
	agents := selectToolAgents(tree, sel)
	if len(agents) == 0 {
		return fmt.Errorf("no agent matches %q; try the sessions command", sel)
	}

	if f.json {
		enc := json.NewEncoder(out)
		for _, a := range agents {
			for _, e := range a.ToolEvents() {
				if err := enc.Encode(toolJSON{AgentType: a.AgentType, SessionID: a.SessionID, ToolEvent: e}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for i, a := range agents {
		if i > 0 {
			fmt.Fprintln(out)
		}
		if err := d.RenderTools(out, a, a.ToolEvents()); err != nil {
			return err
		}
	}
	return nil
}

type toolJSON struct {
	AgentType string `json:"agent_type"`
	SessionID string `json:"session_id"`
	logview.ToolEvent
}

// selectToolAgents resolves the --agent selector: empty = all agents, a session-id
// prefix = that one agent, otherwise an agent-type match.
func selectToolAgents(tree *logview.SessionTree, sel string) []*logview.Agent {
	if sel == "" {
		return tree.Agents
	}
	var byType []*logview.Agent
	for _, a := range tree.Agents {
		if strings.HasPrefix(a.SessionID, sel) {
			return []*logview.Agent{a}
		}
		if a.AgentType == sel {
			byType = append(byType, a)
		}
	}
	return byType
}

func runCaptures(out *bufio.Writer, d logview.Display) error {
	summaries, err := logview.ListCaptureSummaries()
	if err != nil {
		return err
	}
	return d.RenderCaptureList(out, summaries)
}

// loadStream returns records either from --file/stdin or from the resolved capture.
func loadStream[T any](f flags, fromCapture func(*logview.Capture) ([]T, error)) ([]T, error) {
	if f.file != "" {
		rc, err := openInput(f.file)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rc.Close() }()
		return logview.ReadJSONL[T](rc)
	}
	capt, err := logview.Resolve(f.dir)
	if err != nil {
		return nil, err
	}
	return fromCapture(capt)
}

func resolveCapturePath(f flags, pick func(*logview.Capture) string) (string, error) {
	capt, err := logview.Resolve(f.dir)
	if err != nil {
		return "", err
	}
	return pick(capt), nil
}

func openInput(path string) (io.ReadCloser, error) {
	if path == "-" {
		return io.NopCloser(os.Stdin), nil
	}
	return os.Open(path) // #nosec G304 -- operator names the JSONL file to view
}

func writeJSONL[T any](out *bufio.Writer, recs []T) error {
	enc := json.NewEncoder(out)
	for _, r := range recs {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}

// followLLM streams existing then newly-captured LLM rows, numbering them globally.
func followLLM(out *bufio.Writer, f flags, d logview.Display) error {
	path, err := resolveCapturePath(f, func(c *logview.Capture) string { return c.LLMPath })
	if err != nil {
		return err
	}
	recs, err := logview.ReadJSONLFile[logview.LLMRecord](path)
	if err != nil {
		return err
	}
	n := len(recs)
	for i, r := range recs {
		fmt.Fprintln(out, d.LLMRow(i+1, r))
	}
	_ = out.Flush()
	t := logview.NewTailer[logview.LLMRecord](path, false)
	for {
		fresh, err := t.Poll()
		if err != nil {
			return err
		}
		for _, r := range fresh {
			n++
			fmt.Fprintln(out, d.LLMRow(n, r))
		}
		_ = out.Flush()
		time.Sleep(600 * time.Millisecond)
	}
}

// followRows streams existing then newly-captured rows for a single-line stream.
func followRows[T any](out *bufio.Writer, path string, render func(T) string) error {
	recs, err := logview.ReadJSONLFile[T](path)
	if err != nil {
		return err
	}
	for _, r := range recs {
		fmt.Fprintln(out, render(r))
	}
	_ = out.Flush()
	t := logview.NewTailer[T](path, false)
	for {
		fresh, err := t.Poll()
		if err != nil {
			return err
		}
		for _, r := range fresh {
			fmt.Fprintln(out, render(r))
		}
		_ = out.Flush()
		time.Sleep(600 * time.Millisecond)
	}
}

// listCaptures prints the available capture session names newest-first; each can
// be passed to any logs command via --dir.
func listCaptures() error {
	names, err := logview.SessionDirs()
	if err != nil {
		return err
	}
	if len(names) == 0 {
		_, _ = fmt.Fprintln(os.Stdout, "no captures found — run ./task den:sidecar:full-debug first")
		return nil
	}
	for _, n := range names {
		_, _ = fmt.Fprintln(os.Stdout, n)
	}
	return nil
}

// display builds the render context from config with flag overrides applied.
func (f flags) display(cfg logview.Config) logview.Display {
	d := logview.NewDisplay(cfg, f.colorEnabled(cfg))
	if f.unescape {
		d.UnescapeHTML = true
	}
	return d
}

// tuiConfig folds flag overrides into the config the TUI launches with.
func (f flags) tuiConfig(cfg logview.Config) logview.Config {
	if f.unescape {
		cfg.UnescapeHTML = true
	}
	if f.follow {
		cfg.Follow = true
	}
	return cfg
}

func (f flags) colorEnabled(cfg logview.Config) bool {
	mode := cfg.Color
	if f.color != "" {
		mode = f.color
	}
	if f.noColor {
		mode = "never"
	}
	switch mode {
	case "always":
		return true
	case "never":
		return false
	default:
		return logview.IsTTY(os.Stdout)
	}
}

func (f flags) filter() logview.Filter {
	return logview.Filter{
		Session: f.session,
		Agent:   f.agent,
		Surface: f.surface,
		Topic:   f.topic,
		Limit:   f.limit,
		Errors:  f.errors,
	}
}

func parseFlags(args []string) (flags, error) {
	f := flags{}
	needValue := func(i int, name string) (string, error) {
		if i+1 >= len(args) {
			return "", fmt.Errorf("%s requires a value", name)
		}
		return args[i+1], nil
	}
	str := func(dst *string, i *int, name string) error {
		v, err := needValue(*i, name)
		if err != nil {
			return err
		}
		*dst, *i = v, *i+1
		return nil
	}
	num := func(dst *int, i *int, name string) error {
		v, err := needValue(*i, name)
		if err != nil {
			return err
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%s requires a number, got %q", name, v)
		}
		*dst, *i = n, *i+1
		return nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		var err error
		switch a {
		case "--dir":
			err = str(&f.dir, &i, a)
		case "--file":
			err = str(&f.file, &i, a)
		case "--color":
			err = str(&f.color, &i, a)
		case "--session":
			err = str(&f.session, &i, a)
		case "--agent":
			err = str(&f.agent, &i, a)
		case "--surface":
			err = str(&f.surface, &i, a)
		case "--topic":
			err = str(&f.topic, &i, a)
		case "--previous-family":
			err = str(&f.previous, &i, a)
		case "--fixture":
			err = str(&f.fixture, &i, a)
		case "--verify":
			f.verify = true
		case "-n", "--limit":
			err = num(&f.limit, &i, a)
		case "--head":
			err = num(&f.head, &i, a)
		case "--no-color":
			f.noColor = true
		case "--unescape-html", "--unescape":
			f.unescape = true
		case "--json":
			f.json = true
		case "-f", "--follow":
			f.follow = true
		case "--list":
			f.list = true
		case "--errors":
			f.errors = true
		case "--no-system":
			f.noSys = true
		case "--tools":
			f.tools = true
		case "--raw":
			f.raw = true
		default:
			if strings.HasPrefix(a, "-") {
				return f, fmt.Errorf("unknown flag %q", a)
			}
			f.pos = append(f.pos, a)
		}
		if err != nil {
			return f, err
		}
	}
	return f, nil
}
