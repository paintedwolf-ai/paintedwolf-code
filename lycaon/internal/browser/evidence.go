package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// Page evidence bounds. Every stream keeps its newest records; a report shows failures first.
const (
	MaxConsoleLogEntries   = 200
	MaxConsoleLogLineBytes = 16 * 1024
	MaxConsoleLogBytes     = 256 * 1024
	maxRetainedNetwork     = 400
	maxRetainedErrors      = 100
	MaxReportedNetwork     = 60
	MaxReportedErrors      = 20
	maxEvidenceURLBytes    = 512
	maxErrorMessageBytes   = 1024
	maxErrorStackFrames    = 6
	evidenceProjectTimeout = ActionTimeout
	captureUnavailableLine = "[Capture screening unavailable]"
	networkServedByRoute   = "route"
	networkServedByProject = "project_file"
	networkServedByFont    = "font_substitute"
	networkFailureCanceled = "canceled"
)

// NetworkRecord is one request the page made and how it ended.
type NetworkRecord struct {
	Method       string  `json:"method"`
	URL          string  `json:"url"`
	Type         string  `json:"type,omitempty"`
	Status       int     `json:"status,omitempty"`
	Failure      string  `json:"failure,omitempty"`
	DurationMS   float64 `json:"duration_ms,omitempty"`
	Bytes        float64 `json:"bytes,omitempty"`
	ServedBy     string  `json:"served_by,omitempty"`
	Route        *int    `json:"route,omitempty"`
	Pending      bool    `json:"pending,omitempty"`
	AtMS         float64 `json:"at_ms"`
	requestID    proto.NetworkRequestID
	wallMS       float64
	monotonicSec float64
}

func (r NetworkRecord) failed() bool {
	return r.Failure != "" || r.Status >= 400
}

// PageError is an uncaught exception or unhandled rejection.
type PageError struct {
	Message string   `json:"message"`
	Source  string   `json:"source,omitempty"`
	Stack   []string `json:"stack,omitempty"`
	AtMS    float64  `json:"at_ms"`
	wallMS  float64
}

// PageEvidence is what a page did: console output, requests, and uncaught errors.
type PageEvidence struct {
	Log            []string        `json:"log"`
	LogTruncated   bool            `json:"log_truncated,omitempty"`
	Network        []NetworkRecord `json:"network,omitempty"`
	NetworkOmitted int             `json:"network_omitted,omitempty"`
	Errors         []PageError     `json:"errors,omitempty"`
	ErrorsOmitted  int             `json:"errors_omitted,omitempty"`
}

// evidenceMark is a position in the page's evidence streams.
type evidenceMark struct {
	console, network, errors int
}

// pageEvidence records a page's console, network, and exception events for its lifetime.
// Console lines are numbered from 1 as they arrive; a report for a mark covers the lines
// numbered after it, so truncation is a fact about those lines, not about the page's history.
type pageEvidence struct {
	mu            sync.Mutex
	openedWallMS  float64
	screen        func(string) (string, error)
	console       []string
	consoleWallMS []float64
	consoleSeq    int
	consoleBytes  int
	// consoleClipSeq numbers the newest line whose text was cut to fit.
	consoleClipSeq int
	network        []NetworkRecord
	networkSeq     int
	byRequest      map[proto.NetworkRequestID]int
	served         map[proto.NetworkRequestID]servedBy
	errors         []PageError
	errorsSeq      int
}

type servedBy struct {
	by    string
	route *int
}

func nowWallMS() float64 {
	return float64(time.Now().UnixNano()) / 1e6
}

// attachPageEvidence subscribes to a page's events. Setup is bounded by the request;
// the subscriptions last as long as the page.
//
//nolint:contextcheck // Subscriptions follow the page lifetime after request-bounded setup succeeds.
func attachPageEvidence(
	ctx context.Context,
	page *rod.Page,
	projector *captureprojection.Projector,
	scope captureprojection.Scope,
) *pageEvidence {
	ev := &pageEvidence{
		openedWallMS: nowWallMS(),
		byRequest:    map[proto.NetworkRequestID]int{},
		served:       map[proto.NetworkRequestID]servedBy{},
	}
	listenCtx, cancelListen := context.WithCancel(page.GetContext())
	stopRequest := context.AfterFunc(ctx, cancelListen)
	timer := time.AfterFunc(RasterizeTimeout, cancelListen)
	defer stopRequest()
	defer timer.Stop()
	page = page.Context(listenCtx)
	if projector != nil {
		ev.screen = func(line string) (string, error) {
			projectionCtx, cancel := context.WithTimeout(listenCtx, evidenceProjectTimeout)
			defer cancel()
			projected, err := projector.Text(projectionCtx, scope, "capture.browser.log", line)
			return projected.Value, err
		}
	}
	go page.EachEvent(
		func(e *proto.RuntimeConsoleAPICalled) { ev.appendConsole(consoleLine(e)) },
		func(e *proto.RuntimeExceptionThrown) { ev.appendError(e) },
		func(e *proto.NetworkRequestWillBeSent) { ev.requestSent(e) },
		func(e *proto.NetworkResponseReceived) { ev.responseReceived(e) },
		func(e *proto.NetworkLoadingFinished) { ev.loadingFinished(e) },
		func(e *proto.NetworkLoadingFailed) { ev.loadingFailed(e) },
	)()
	_ = proto.NetworkEnable{}.Call(page)
	_ = proto.RuntimeEnable{}.Call(page)
	_ = proto.PageEnable{}.Call(page)
	return ev
}

func consoleLine(e *proto.RuntimeConsoleAPICalled) string {
	args := make([]string, 0, len(e.Args))
	for _, a := range e.Args {
		switch {
		case !a.Value.Nil():
			args = append(args, a.Value.Str())
		case a.Description != "":
			args = append(args, a.Description)
		case a.UnserializableValue != "":
			args = append(args, string(a.UnserializableValue))
		}
	}
	return fmt.Sprintf("console.%s: %s", e.Type, strings.Join(args, " "))
}

// appendConsole screens a line before the byte limits apply; the newest lines are kept.
func (ev *pageEvidence) appendConsole(line string) {
	if ev.screen == nil {
		line = captureUnavailableLine
	} else if safe, err := ev.screen(line); err != nil {
		line = captureUnavailableLine
	} else {
		line = safe
	}
	line, clipped := truncateUTF8Bytes(line, MaxConsoleLogLineBytes)
	ev.mu.Lock()
	defer ev.mu.Unlock()
	ev.consoleSeq++
	if clipped {
		ev.consoleClipSeq = ev.consoleSeq
	}
	ev.console = append(ev.console, line)
	ev.consoleWallMS = append(ev.consoleWallMS, nowWallMS())
	ev.consoleBytes += len(line)
	// The newest output matters most on a long-lived page, so the oldest lines go first.
	for len(ev.console) > MaxConsoleLogEntries || ev.consoleBytes > MaxConsoleLogBytes {
		ev.consoleBytes -= len(ev.console[0])
		ev.console = ev.console[1:]
		ev.consoleWallMS = ev.consoleWallMS[1:]
	}
}

// consoleTruncatedSince reports whether a line numbered after the mark was cut or dropped.
func (ev *pageEvidence) consoleTruncatedSince(m evidenceMark) bool {
	oldestRetained := ev.consoleSeq - len(ev.console) + 1
	return ev.consoleClipSeq > m.console || oldestRetained > m.console+1
}

// noteHostLine records a host observation, such as a failed idle wait, beside console output.
func (ev *pageEvidence) noteHostLine(line string) {
	ev.appendConsole(line)
}

func (ev *pageEvidence) appendError(e *proto.RuntimeExceptionThrown) {
	record := PageError{Message: "uncaught", wallMS: nowWallMS()}
	if d := e.ExceptionDetails; d != nil {
		record.Message = d.Text
		if d.Exception != nil && d.Exception.Description != "" {
			record.Message = d.Exception.Description
		}
		if d.URL != "" {
			record.Source = fmt.Sprintf("%s:%d:%d", clipURL(d.URL), d.LineNumber+1, d.ColumnNumber+1)
		}
		if d.StackTrace != nil {
			for _, f := range d.StackTrace.CallFrames {
				if len(record.Stack) == maxErrorStackFrames {
					break
				}
				name := f.FunctionName
				if name == "" {
					name = "(anonymous)"
				}
				record.Stack = append(record.Stack, fmt.Sprintf("%s (%s:%d:%d)", name, clipURL(f.URL), f.LineNumber+1, f.ColumnNumber+1))
			}
		}
	}
	// A description carries the stack as text; the frames above hold it structurally.
	record.Message, _ = truncateUTF8Bytes(firstLine(record.Message), maxErrorMessageBytes)
	ev.mu.Lock()
	defer ev.mu.Unlock()
	ev.errorsSeq++
	ev.errors = append(ev.errors, record)
	if len(ev.errors) > maxRetainedErrors {
		ev.errors = ev.errors[len(ev.errors)-maxRetainedErrors:]
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func clipURL(u string) string {
	out, _ := truncateUTF8Bytes(u, maxEvidenceURLBytes)
	return out
}

// noteServed attributes a request the page's fetch handler answered itself.
func (ev *pageEvidence) noteServed(id proto.NetworkRequestID, by string, route *int) {
	if id == "" {
		return
	}
	ev.mu.Lock()
	defer ev.mu.Unlock()
	ev.served[id] = servedBy{by: by, route: route}
	if i, ok := ev.byRequest[id]; ok {
		ev.network[i].ServedBy, ev.network[i].Route = by, route
	}
}

func (ev *pageEvidence) requestSent(e *proto.NetworkRequestWillBeSent) {
	if e.Request == nil || strings.HasPrefix(e.Request.URL, "data:") || strings.HasPrefix(e.Request.URL, "blob:") {
		return
	}
	ev.mu.Lock()
	defer ev.mu.Unlock()
	wallMS := float64(e.WallTime) * 1000
	// A redirect ends the previous hop with its redirect status.
	if i, ok := ev.byRequest[e.RequestID]; ok && e.RedirectResponse != nil {
		ev.network[i].Status = e.RedirectResponse.Status
		ev.network[i].Pending = false
		ev.network[i].DurationMS = roundTenth((float64(e.Timestamp) - ev.network[i].monotonicSec) * 1000)
	}
	record := NetworkRecord{
		Method: e.Request.Method, URL: clipURL(e.Request.URL), Type: strings.ToLower(string(e.Type)),
		Pending: true, requestID: e.RequestID, wallMS: wallMS, monotonicSec: float64(e.Timestamp),
	}
	if s, ok := ev.served[e.RequestID]; ok {
		record.ServedBy, record.Route = s.by, s.route
	}
	ev.networkSeq++
	ev.network = append(ev.network, record)
	if len(ev.network) > maxRetainedNetwork {
		drop := len(ev.network) - maxRetainedNetwork
		ev.network = ev.network[drop:]
		for id, i := range ev.byRequest {
			if i < drop {
				delete(ev.byRequest, id)
				delete(ev.served, id)
			} else {
				ev.byRequest[id] = i - drop
			}
		}
	}
	ev.byRequest[e.RequestID] = len(ev.network) - 1
}

func (ev *pageEvidence) update(id proto.NetworkRequestID, apply func(*NetworkRecord)) {
	ev.mu.Lock()
	defer ev.mu.Unlock()
	if i, ok := ev.byRequest[id]; ok {
		apply(&ev.network[i])
	}
}

func (ev *pageEvidence) responseReceived(e *proto.NetworkResponseReceived) {
	ev.update(e.RequestID, func(r *NetworkRecord) {
		if e.Response != nil {
			r.Status = e.Response.Status
		}
	})
}

func (ev *pageEvidence) loadingFinished(e *proto.NetworkLoadingFinished) {
	ev.update(e.RequestID, func(r *NetworkRecord) {
		r.Pending = false
		r.DurationMS = roundTenth((float64(e.Timestamp) - r.monotonicSec) * 1000)
		r.Bytes = e.EncodedDataLength
	})
}

func (ev *pageEvidence) loadingFailed(e *proto.NetworkLoadingFailed) {
	ev.update(e.RequestID, func(r *NetworkRecord) {
		r.Pending = false
		r.DurationMS = roundTenth((float64(e.Timestamp) - r.monotonicSec) * 1000)
		switch {
		case e.Canceled:
			r.Failure = networkFailureCanceled
		case e.BlockedReason != "":
			r.Failure = "blocked:" + string(e.BlockedReason)
		default:
			r.Failure = e.ErrorText
		}
	})
}

func roundTenth(v float64) float64 {
	return math.Round(v*10) / 10
}

func (ev *pageEvidence) mark() evidenceMark {
	ev.mu.Lock()
	defer ev.mu.Unlock()
	return evidenceMark{console: ev.consoleSeq, network: ev.networkSeq, errors: ev.errorsSeq}
}

// since reports evidence recorded after a mark; the zero mark reports everything retained.
func (ev *pageEvidence) since(m evidenceMark) PageEvidence {
	ev.mu.Lock()
	defer ev.mu.Unlock()
	out := PageEvidence{Log: tail(ev.console, ev.consoleSeq-m.console), LogTruncated: ev.consoleTruncatedSince(m)}
	network := tail(ev.network, ev.networkSeq-m.network)
	out.Network, out.NetworkOmitted = reportNetwork(network, ev.openedWallMS)
	errs := tail(ev.errors, ev.errorsSeq-m.errors)
	if len(errs) > MaxReportedErrors {
		out.ErrorsOmitted = len(errs) - MaxReportedErrors
		errs = errs[len(errs)-MaxReportedErrors:]
	}
	for _, e := range errs {
		e.AtMS = roundTenth(e.wallMS - ev.openedWallMS)
		out.Errors = append(out.Errors, e)
	}
	return out
}

// timedLine is one console line and when it was written.
type timedLine struct {
	wallMS float64
	line   string
}

// recordedSince returns everything retained after a mark with its wall-clock times, for a timeline.
func (ev *pageEvidence) recordedSince(m evidenceMark) ([]timedLine, []NetworkRecord, []PageError) {
	ev.mu.Lock()
	defer ev.mu.Unlock()
	lines := tail(ev.console, ev.consoleSeq-m.console)
	walls := tail(ev.consoleWallMS, ev.consoleSeq-m.console)
	console := make([]timedLine, len(lines))
	for i := range lines {
		console[i] = timedLine{wallMS: walls[i], line: lines[i]}
	}
	return console, tail(ev.network, ev.networkSeq-m.network), tail(ev.errors, ev.errorsSeq-m.errors)
}

// tail returns the last n items still retained; items dropped from retention are gone.
func tail[T any](items []T, n int) []T {
	if n <= 0 {
		return nil
	}
	if n > len(items) {
		n = len(items)
	}
	return append([]T(nil), items[len(items)-n:]...)
}

// reportNetwork keeps every failure it can, then the most recent successes, in request order.
func reportNetwork(records []NetworkRecord, baseWallMS float64) ([]NetworkRecord, int) {
	if len(records) <= MaxReportedNetwork {
		return stamp(records, baseWallMS), 0
	}
	keep := make([]int, 0, MaxReportedNetwork)
	for i := len(records) - 1; i >= 0 && len(keep) < MaxReportedNetwork; i-- {
		if records[i].failed() {
			keep = append(keep, i)
		}
	}
	for i := len(records) - 1; i >= 0 && len(keep) < MaxReportedNetwork; i-- {
		if !records[i].failed() {
			keep = append(keep, i)
		}
	}
	sort.Ints(keep)
	out := make([]NetworkRecord, 0, len(keep))
	for _, i := range keep {
		out = append(out, records[i])
	}
	return stamp(out, baseWallMS), len(records) - len(keep)
}

func stamp(records []NetworkRecord, baseWallMS float64) []NetworkRecord {
	for i := range records {
		records[i].AtMS = roundTenth(records[i].wallMS - baseWallMS)
	}
	return records
}

// projectEvidence screens URLs, messages, and stack frames before they leave the host.
func projectEvidence(ctx context.Context, projector *captureprojection.Projector, scope captureprojection.Scope, ev PageEvidence) (PageEvidence, error) {
	if projector == nil {
		return PageEvidence{}, captureprojection.ErrUnavailable
	}
	for _, field := range []struct {
		channel string
		value   any
	}{{"capture.browser.network", &ev.Network}, {"capture.browser.errors", &ev.Errors}} {
		raw, err := surveyjson.Marshal(field.value)
		if err != nil {
			return PageEvidence{}, err
		}
		if string(raw) == "null" {
			continue
		}
		safe, _, err := projector.JSON(ctx, scope, field.channel, raw)
		if err != nil {
			return PageEvidence{}, fmt.Errorf("screen page evidence: %w", err)
		}
		if err := json.Unmarshal(safe, field.value); err != nil {
			return PageEvidence{}, fmt.Errorf("decode screened page evidence: %w", err)
		}
	}
	return ev, nil
}

func truncateUTF8Bytes(value string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", value != ""
	}
	if len(value) <= maxBytes {
		return value, false
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end], true
}
