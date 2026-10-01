package logview

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

// LogRecord is one parsed line of the sidecar's slog (logfmt) output.
type LogRecord struct {
	Time  time.Time
	Level string // INFO, DEBUG, WARN, ERROR
	Msg   string
	Attrs string // remaining key=value pairs
}

// reLogfmt captures the fixed slog TextHandler prefix: time, level, msg, rest.
var reLogfmt = regexp.MustCompile(`^time=(\S+) level=(\S+) msg=("(?:[^"\\]|\\.)*"|\S*)(.*)$`)

// LogRecords parses the sidecar.log for a capture. A missing file yields nil.
func (c *Capture) LogRecords() ([]LogRecord, error) {
	f, err := os.Open(c.LogPath) // #nosec G304 -- capture file path from the resolved debug session dir
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out []LogRecord
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scan.Scan() {
		line := scan.Text()
		if line == "" {
			continue
		}
		out = append(out, parseLogLine(line))
	}
	return out, scan.Err()
}

func parseLogLine(line string) LogRecord {
	m := reLogfmt.FindStringSubmatch(line)
	if m == nil {
		return LogRecord{Msg: line}
	}
	rec := LogRecord{Level: strings.ToUpper(m[2]), Msg: unquoteLogValue(m[3]), Attrs: strings.TrimSpace(m[4])}
	if t, err := time.Parse(time.RFC3339, m[1]); err == nil {
		rec.Time = t
	}
	return rec
}

func unquoteLogValue(s string) string {
	if len(s) >= 2 && s[0] == '"' {
		s = s[1 : len(s)-1]
		s = strings.ReplaceAll(s, `\"`, `"`)
		s = strings.ReplaceAll(s, `\\`, `\`)
	}
	return s
}

// logLevelRank orders levels for filtering (higher = more severe).
func logLevelRank(level string) int {
	switch strings.ToUpper(level) {
	case "ERROR":
		return 3
	case "WARN", "WARNING":
		return 2
	case "INFO":
		return 1
	default: // DEBUG, unknown
		return 0
	}
}

// FilterLogs keeps records at or above minRank (e.g. 2 for warn/error only).
func FilterLogs(recs []LogRecord, minRank int) []LogRecord {
	out := make([]LogRecord, 0, len(recs))
	for _, r := range recs {
		if logLevelRank(r.Level) >= minRank {
			out = append(out, r)
		}
	}
	return out
}

// CountLogsAtLeast returns how many records are at or above minRank.
func CountLogsAtLeast(recs []LogRecord, minRank int) int {
	n := 0
	for _, r := range recs {
		if logLevelRank(r.Level) >= minRank {
			n++
		}
	}
	return n
}

func (d Display) logLevel(level string) string {
	switch strings.ToUpper(level) {
	case "ERROR":
		return d.Red("ERROR")
	case "WARN", "WARNING":
		return d.Yellow("WARN ")
	case "INFO":
		return d.Green("INFO ")
	case "":
		return d.Dim("·    ")
	default:
		return d.Dim(fmt.Sprintf("%-5s", strings.ToUpper(level)))
	}
}

// LogRow renders one slog line for the list.
func (d Display) LogRow(r LogRecord) string {
	return fmt.Sprintf("%s %s %s", d.Dim(d.time(r.Time)), d.logLevel(r.Level), d.plainText(r.Msg))
}

// LogDetail renders one slog line in full, including its attributes.
func (d Display) LogDetail(w io.Writer, r LogRecord) {
	fmt.Fprintf(w, "%s  %s\n\n", d.logLevel(r.Level), d.Dim(d.time(r.Time)))
	fmt.Fprintln(w, d.indentWrap(d.plainText(r.Msg), "  ", 0))
	if r.Attrs != "" {
		fmt.Fprintf(w, "\n%s\n%s\n", d.Dim("attrs"), d.indentWrap(r.Attrs, "  ", 0))
	}
}
