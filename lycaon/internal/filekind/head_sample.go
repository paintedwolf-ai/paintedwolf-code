package filekind

import (
	"bytes"
	"strings"
)

const (
	headSampleMaxBytes  = 8192
	maxParseCandidates  = 4
	parseConfidenceTieE = 0.02
)

func truncateHeadSample(sample []byte) []byte {
	if len(sample) <= headSampleMaxBytes {
		return sample
	}
	return sample[:headSampleMaxBytes]
}

func firstLine(sample []byte) string {
	sample = truncateHeadSample(sample)
	if len(sample) == 0 {
		return ""
	}
	line, _, _ := strings.Cut(string(sample), "\n")
	return strings.TrimSpace(line)
}

func nonEmptyLineCount(sample []byte) int {
	sample = truncateHeadSample(sample)
	if len(sample) == 0 {
		return 0
	}
	n := 0
	for _, line := range bytes.Split(sample, []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			n++
		}
	}
	return n
}
