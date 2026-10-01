package logoutline

import (
	"bytes"
	"strings"
)

const (
	classifyMaxLines = 200
	classifyMaxBytes = 8192
)

func headSampleLines(sample []byte) [][]byte {
	if len(sample) > classifyMaxBytes {
		sample = sample[:classifyMaxBytes]
	}
	var lines [][]byte
	for _, line := range bytes.Split(sample, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		lines = append(lines, line)
		if len(lines) >= classifyMaxLines {
			break
		}
	}
	return lines
}

func lineMatchRatio(lines [][]byte, match func([]byte) bool) float64 {
	if len(lines) == 0 {
		return 0
	}
	var hits int
	for _, line := range lines {
		if match(line) {
			hits++
		}
	}
	return float64(hits) / float64(len(lines))
}

func hasLogKey(fields map[string]string) bool {
	for _, key := range []string{
		"level", "lvl", "severity", "msg", "message",
		"ts", "time", "@timestamp", "timestamp",
	} {
		if _, ok := fields[key]; ok {
			return true
		}
	}
	return false
}

func trimQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
