package logoutline

import (
	"bytes"
	"strconv"
	"strings"
	"time"
)

func parseFlexibleTime(s string) (time.Time, bool) {
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
		"Jan 2 15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, true
		}
	}
	if unix, err := strconv.ParseInt(s, 10, 64); err == nil {
		if len(s) >= 13 {
			return time.UnixMilli(unix), true
		}
		return time.Unix(unix, 0), true
	}
	return time.Time{}, false
}

func parseSyslog3164Time(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if len(s) != 15 {
		return time.Time{}, false
	}
	year := time.Now().Year()
	t, err := time.Parse("Jan 2 15:04:05", s)
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(year, t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.Local), true
}

func parseCLFTime(s string) (time.Time, bool) {
	t, err := time.Parse("02/Jan/2006:15:04:05 -0700", s)
	return t, err == nil
}

func parsePRI(s string) (int, bool) {
	if !strings.HasPrefix(s, "<") {
		return 0, false
	}
	end := strings.IndexByte(s, '>')
	if end <= 1 {
		return 0, false
	}
	n, err := strconv.Atoi(s[1:end])
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

var priSeverityNames = []string{
	"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug",
}

func priSeverity(pri int) string {
	sev := pri % 8
	if sev < 0 || sev >= len(priSeverityNames) {
		return strconv.Itoa(sev)
	}
	return priSeverityNames[sev]
}

func parseLogfmtLine(line []byte) (map[string]string, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, false
	}
	fields := make(map[string]string)
	i := 0
	for i < len(line) {
		eq := bytes.IndexByte(line[i:], '=')
		if eq < 0 {
			break
		}
		key := string(bytes.TrimSpace(line[i : i+eq]))
		if key == "" {
			return nil, false
		}
		i += eq + 1
		if i >= len(line) {
			fields[key] = ""
			break
		}
		if line[i] == '"' {
			i++
			var val strings.Builder
			for i < len(line) {
				if line[i] == '\\' && i+1 < len(line) {
					val.WriteByte(line[i+1])
					i += 2
					continue
				}
				if line[i] == '"' {
					i++
					break
				}
				val.WriteByte(line[i])
				i++
			}
			fields[key] = val.String()
		} else {
			sp := bytes.IndexByte(line[i:], ' ')
			if sp < 0 {
				fields[key] = string(line[i:])
				break
			}
			fields[key] = string(line[i : i+sp])
			i += sp + 1
		}
	}
	if len(fields) == 0 {
		return nil, false
	}
	return fields, true
}

func unescapeCEF(s string) string {
	s = strings.ReplaceAll(s, `\=`, "=")
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = strings.ReplaceAll(s, `\r`, "\r")
	s = strings.ReplaceAll(s, `\\`, `\`)
	return s
}
