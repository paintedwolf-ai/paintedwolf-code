package logoutline

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reSyslog5424 = regexp.MustCompile(`^<\d+>\d+\s+\S+\s+\S+\s+\S+\s+\S+\s+\S+\s+`)
	reSyslog3164 = regexp.MustCompile(`^(?:<\d+>)?[A-Z][a-z]{2}\s+\d{1,2}\s+\d{2}:\d{2}:\d{2}\s+\S+`)
	reCEF        = regexp.MustCompile(`^CEF:\d+\|`)
	reLEEF       = regexp.MustCompile(`^LEEF:\d+\.\d+\|`)
	reCLF        = regexp.MustCompile(`^\S+\s+\S+\s+\S+\s+\[[^\]]+\]\s+"[A-Z]+ `)
	reCombined   = regexp.MustCompile(`^\S+\s+\S+\s+\S+\s+\[[^\]]+\]\s+"[A-Z]+ `)
)

type jsonLinesParser struct{}

func (jsonLinesParser) Format() LogFormat { return FormatJSONLines }

func (jsonLinesParser) Parse(line []byte) (Record, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return Record{}, false
	}
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		return Record{}, false
	}
	fields := stringFields(m)
	rec := Record{
		Severity: pickField(fields, levelKeys),
		Message:  pickField(fields, messageKeys),
		Fields:   fields,
	}
	if ts := pickField(fields, timeKeys); ts != "" {
		if t, ok := parseFlexibleTime(ts); ok {
			rec.Time = t
		}
	}
	return rec, true
}

type logfmtParser struct{}

func (logfmtParser) Format() LogFormat { return FormatLogfmt }

func (logfmtParser) Parse(line []byte) (Record, bool) {
	fields, ok := parseLogfmtLine(line)
	if !ok {
		return Record{}, false
	}
	rec := Record{
		Severity: pickField(fields, levelKeys),
		Message:  pickField(fields, messageKeys),
		Fields:   fields,
	}
	if ts := pickField(fields, timeKeys); ts != "" {
		if t, ok := parseFlexibleTime(ts); ok {
			rec.Time = t
		}
	}
	return rec, true
}

type syslog5424Parser struct{}

func (syslog5424Parser) Format() LogFormat { return FormatSyslogRFC5424 }

func (syslog5424Parser) Parse(line []byte) (Record, bool) {
	line = bytes.TrimSpace(line)
	if !reSyslog5424.Match(line) {
		return Record{}, false
	}
	s := string(line)
	afterPRI := s
	if strings.HasPrefix(s, "<") {
		if idx := strings.IndexByte(s, '>'); idx >= 0 {
			afterPRI = s[idx+1:]
		}
	}
	parts := strings.Fields(afterPRI)
	if len(parts) < 7 {
		return Record{}, false
	}
	rec := Record{Fields: map[string]string{
		"version": parts[0],
		"host":    parts[2],
		"app":     parts[3],
		"proc_id": parts[4],
		"msgid":   parts[5],
	}}
	if pri, ok := parsePRI(s); ok {
		rec.Severity = priSeverity(pri)
		rec.Fields["pri"] = strconv.Itoa(pri)
	}
	tsStr := parts[1]
	if t, err := time.Parse(time.RFC3339Nano, tsStr); err == nil {
		rec.Time = t
	} else if t, err := time.Parse(time.RFC3339, tsStr); err == nil {
		rec.Time = t
	}
	rec.Message = strings.Join(parts[6:], " ")
	return rec, true
}

type syslog3164Parser struct{}

func (syslog3164Parser) Format() LogFormat { return FormatSyslogRFC3164 }

func (syslog3164Parser) Parse(line []byte) (Record, bool) {
	line = bytes.TrimSpace(line)
	if !reSyslog3164.Match(line) {
		return Record{}, false
	}
	s := string(line)
	rec := Record{Fields: map[string]string{}}
	if strings.HasPrefix(s, "<") {
		if pri, ok := parsePRI(s); ok {
			rec.Severity = priSeverity(pri)
			rec.Fields["pri"] = strconv.Itoa(pri)
		}
		if idx := strings.Index(s, ">"); idx >= 0 {
			s = strings.TrimSpace(s[idx+1:])
		}
	}
	// Mmm dd hh:mm:ss host tag: msg
	if len(s) < 16 {
		return Record{}, false
	}
	tsPart := s[:15]
	rest := strings.TrimSpace(s[15:])
	if t, ok := parseSyslog3164Time(tsPart); ok {
		rec.Time = t
	}
	if sp := strings.IndexByte(rest, ' '); sp > 0 {
		rec.Fields["host"] = rest[:sp]
		msg := strings.TrimSpace(rest[sp+1:])
		if colon := strings.Index(msg, ": "); colon >= 0 {
			rec.Fields["tag"] = msg[:colon]
			rec.Message = msg[colon+2:]
		} else {
			rec.Message = msg
		}
	}
	return rec, true
}

type cefParser struct{}

func (cefParser) Format() LogFormat { return FormatCEF }

func (cefParser) Parse(line []byte) (Record, bool) {
	line = bytes.TrimSpace(line)
	if !reCEF.Match(line) {
		return Record{}, false
	}
	s := string(line)
	parts := strings.SplitN(s, "|", 8)
	if len(parts) < 8 {
		return Record{}, false
	}
	rec := Record{
		Severity: strings.TrimSpace(parts[6]),
		Fields: map[string]string{
			"vendor":   parts[1],
			"product":  parts[2],
			"version":  parts[3],
			"event_id": parts[4],
			"name":     parts[5],
		},
	}
	ext := parts[7]
	for _, pair := range strings.Fields(ext) {
		if kv := strings.SplitN(pair, "=", 2); len(kv) == 2 {
			rec.Fields[kv[0]] = unescapeCEF(kv[1])
		}
	}
	rec.Message = rec.Fields["msg"]
	if rec.Message == "" {
		rec.Message = rec.Fields["name"]
	}
	if rt := rec.Fields["rt"]; rt != "" {
		if t, ok := parseFlexibleTime(rt); ok {
			rec.Time = t
		}
	}
	return rec, true
}

type leefParser struct{}

func (leefParser) Format() LogFormat { return FormatLEEF }

func (leefParser) Parse(line []byte) (Record, bool) {
	line = bytes.TrimSpace(line)
	if !reLEEF.Match(line) {
		return Record{}, false
	}
	s := string(line)
	headerEnd := strings.Index(s, "|")
	if headerEnd < 0 {
		return Record{}, false
	}
	// LEEF:1.0|Vendor|Product|Version|EventID| ext fields tab-separated
	pipeParts := strings.Split(s, "|")
	if len(pipeParts) < 5 {
		return Record{}, false
	}
	rec := Record{Fields: map[string]string{
		"vendor":   pipeParts[1],
		"product":  pipeParts[2],
		"version":  pipeParts[3],
		"event_id": pipeParts[4],
	}}
	ext := ""
	if len(pipeParts) > 5 {
		ext = pipeParts[5]
	}
	for _, pair := range strings.Split(ext, "\t") {
		if kv := strings.SplitN(pair, "=", 2); len(kv) == 2 {
			rec.Fields[kv[0]] = kv[1]
		}
	}
	rec.Severity = rec.Fields["sev"]
	rec.Message = rec.Fields["cat"]
	if dev := rec.Fields["devTime"]; dev != "" {
		if t, ok := parseFlexibleTime(dev); ok {
			rec.Time = t
		}
	}
	return rec, true
}

type clfParser struct{}

func (clfParser) Format() LogFormat { return FormatCLF }

func (clfParser) Parse(line []byte) (Record, bool) {
	return parseAccessLog(line, false)
}

type combinedParser struct{}

func (combinedParser) Format() LogFormat { return FormatCombined }

func (combinedParser) Parse(line []byte) (Record, bool) {
	return parseAccessLog(line, true)
}

func parseAccessLog(line []byte, combined bool) (Record, bool) {
	line = bytes.TrimSpace(line)
	re := reCLF
	if combined {
		re = reCombined
	}
	if !re.Match(line) {
		return Record{}, false
	}
	// host ident auth [time] "request" status bytes [referer user-agent]
	s := string(line)
	bracketStart := strings.Index(s, "[")
	bracketEnd := strings.Index(s, "]")
	if bracketStart < 0 || bracketEnd <= bracketStart {
		return Record{}, false
	}
	rec := Record{Fields: map[string]string{}}
	hostPart := strings.TrimSpace(s[:bracketStart])
	hostFields := strings.Fields(hostPart)
	if len(hostFields) >= 1 {
		rec.Fields["host"] = hostFields[0]
	}
	if len(hostFields) >= 2 {
		rec.Fields["ident"] = hostFields[1]
	}
	if len(hostFields) >= 3 {
		rec.Fields["auth"] = hostFields[2]
	}
	tsRaw := s[bracketStart+1 : bracketEnd]
	if t, ok := parseCLFTime(tsRaw); ok {
		rec.Time = t
	}
	rest := strings.TrimSpace(s[bracketEnd+1:])
	if len(rest) == 0 || rest[0] != '"' {
		return Record{}, false
	}
	closeQuote := strings.IndexByte(rest[1:], '"')
	if closeQuote < 0 {
		return Record{}, false
	}
	closeQuote++
	request := rest[1:closeQuote]
	rec.Fields["request"] = request
	tail := strings.TrimSpace(rest[closeQuote+1:])
	tailFields := strings.Fields(tail)
	if len(tailFields) >= 1 {
		rec.Fields["status"] = tailFields[0]
	}
	if len(tailFields) >= 2 {
		rec.Fields["bytes"] = tailFields[1]
	}
	if combined && len(tailFields) >= 4 {
		rec.Fields["referer"] = trimQuotes(tailFields[2])
		rec.Fields["user_agent"] = trimQuotes(strings.Join(tailFields[3:], " "))
	}
	rec.Message = request
	return rec, true
}

func matchJSONLines(line []byte) bool {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return false
	}
	if !json.Valid(line) {
		return false
	}
	var m map[string]any
	if json.Unmarshal(line, &m) != nil {
		return false
	}
	return hasLogKey(stringFields(m))
}

func matchLogfmt(line []byte) bool {
	fields, ok := parseLogfmtLine(line)
	if !ok || len(fields) < 2 {
		return false
	}
	return hasLogKey(fields)
}

func matchSyslog5424(line []byte) bool { return reSyslog5424.Match(bytes.TrimSpace(line)) }
func matchSyslog3164(line []byte) bool { return reSyslog3164.Match(bytes.TrimSpace(line)) }
func matchCEF(line []byte) bool        { return reCEF.Match(bytes.TrimSpace(line)) }
func matchLEEF(line []byte) bool       { return reLEEF.Match(bytes.TrimSpace(line)) }
func matchCLF(line []byte) bool        { return reCLF.Match(bytes.TrimSpace(line)) }
func matchCombined(line []byte) bool {
	line = bytes.TrimSpace(line)
	if !reCombined.Match(line) {
		return false
	}
	s := string(line)
	firstQuote := strings.IndexByte(s, '"')
	if firstQuote < 0 {
		return false
	}
	secondQuote := strings.IndexByte(s[firstQuote+1:], '"')
	if secondQuote < 0 {
		return false
	}
	tail := strings.TrimSpace(s[firstQuote+1+secondQuote+1:])
	return len(strings.Fields(tail)) >= 4
}
