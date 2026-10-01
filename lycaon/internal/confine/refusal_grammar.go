package confine

import (
	"encoding/json"
	"strconv"
	"strings"
)

// refusalTagPrefix leads the message every tagged deny rule carries, so a
// kernel report names the action whose profile refused the operation.
const refusalTagPrefix = "pwc1 "

// refusalMarkPrefix leads the host's own marker messages in the stream.
const refusalMarkPrefix = "pwc1-mark "

// truncatedReportSuffix ends a report the kernel cut at its log size limit;
// the rule message after a long target is lost with the rest.
const truncatedReportSuffix = "<…>"

// These delimit the macOS Sandbox report grammar. parseStreamLine authenticates
// the kernel sender before this grammar can produce a refusal fact.
const (
	kernelReportPrefix    = "Sandbox: "
	kernelDuplicateReport = " for " + kernelReportPrefix
	kernelDenyDelimiter   = ") deny("
)

// kernelRefusal is one kernel sandbox report, attributed by its rule tag, or
// by process when the kernel truncated the report before the tag.
type kernelRefusal struct {
	Tag       string
	PID       int
	Process   string
	Operation string
	Target    string
	Count     int
	// Truncated reports a target the kernel cut short.
	Truncated bool
}

// streamEvent is the part of one ndjson log event the watch reads.
type streamEvent struct {
	EventType        string `json:"eventType"`
	EventMessage     string `json:"eventMessage"`
	ProcessImagePath string `json:"processImagePath"`
	SenderImagePath  string `json:"senderImagePath"`
	ProcessID        int    `json:"processID"`
}

// sandboxSenderSuffix is the kernel extension that writes refusal reports. A
// process cannot log with another image as its sender.
const sandboxSenderSuffix = "/Sandbox.kext/Contents/MacOS/Sandbox"

// streamLine is one decoded line of the log stream.
type streamLine struct {
	refusal kernelRefusal
	mark    string
	// lost reports messages the log dropped before delivery.
	lost bool
}

// parseStreamLine reads one ndjson event. Only the kernel's sandbox reports
// and this process's own markers count; everything else is ignored.
func parseStreamLine(line []byte, selfPID int) (streamLine, bool) {
	var ev streamEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return streamLine{}, false
	}
	switch {
	case ev.EventType == "lossEvent":
		return streamLine{lost: true}, true
	case ev.EventType != "logEvent":
		return streamLine{}, false
	case ev.ProcessImagePath == "/kernel" && strings.HasSuffix(ev.SenderImagePath, sandboxSenderSuffix):
		refusal, ok := parseKernelRefusal(ev.EventMessage)
		return streamLine{refusal: refusal}, ok
	case ev.ProcessID == selfPID && strings.HasPrefix(ev.EventMessage, refusalMarkPrefix):
		return streamLine{mark: strings.TrimSpace(strings.TrimPrefix(ev.EventMessage, refusalMarkPrefix))}, true
	default:
		return streamLine{}, false
	}
}

// parseKernelRefusal reads the kernel's report grammar:
//
//	[<n> duplicate report(s) for ]Sandbox: <process>(<pid>) deny(<n>) <operation>[ <target>]
//	<rule message>
//
// A report cut at the kernel's size limit ends in "<…>" and carries no rule
// message; it names its action only through its process.
func parseKernelRefusal(message string) (kernelRefusal, bool) {
	out := kernelRefusal{Count: 1}
	report, ruleMessage, found := strings.Cut(message, "\n")
	switch {
	case found:
		tag, ok := strings.CutPrefix(strings.TrimSpace(ruleMessage), refusalTagPrefix)
		if !ok || !validRefusalTag(tag) {
			return kernelRefusal{}, false
		}
		out.Tag = tag
	case strings.HasSuffix(message, truncatedReportSuffix):
		out.Truncated = true
		report = strings.TrimSuffix(message, truncatedReportSuffix)
	default:
		return kernelRefusal{}, false
	}
	if head, rest, dup := strings.Cut(report, kernelDuplicateReport); dup {
		fields := strings.Fields(head)
		if len(fields) != 3 || !strings.HasPrefix(fields[1], "duplicate") {
			return kernelRefusal{}, false
		}
		n, err := strconv.Atoi(fields[0])
		if err != nil || n < 1 {
			return kernelRefusal{}, false
		}
		out.Count = n
		report = rest
	} else if rest, plain := strings.CutPrefix(report, kernelReportPrefix); plain {
		report = rest
	} else {
		return kernelRefusal{}, false
	}
	// The first verdict ends the process name; a target may contain anything.
	open := strings.Index(report, kernelDenyDelimiter)
	if open < 0 {
		return kernelRefusal{}, false
	}
	paren := strings.LastIndex(report[:open], "(")
	if paren <= 0 {
		return kernelRefusal{}, false
	}
	pid, err := strconv.Atoi(report[paren+1 : open])
	if err != nil || pid <= 0 {
		return kernelRefusal{}, false
	}
	out.Process, out.PID = report[:paren], pid
	_, rest, found := strings.Cut(report[open+len(kernelDenyDelimiter):], ") ")
	if !found {
		return kernelRefusal{}, false
	}
	operation, target, _ := strings.Cut(strings.TrimSpace(rest), " ")
	if operation == "" {
		return kernelRefusal{}, false
	}
	out.Operation = operation
	out.Target = strings.TrimSpace(target)
	return out, true
}

// validRefusalTag accepts the hex tag BindAction mints.
func validRefusalTag(tag string) bool {
	if len(tag) != refusalTagBytes*2 {
		return false
	}
	for _, c := range tag {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
