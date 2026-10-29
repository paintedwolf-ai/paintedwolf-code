package patchapply

import "github.com/lycaon/lycaon/internal/tools/surveyjson"

// Failure names one hunk that did not apply.
type Failure struct {
	File   string `json:"file"`
	Hunk   int    `json:"hunk"`
	Header string `json:"header"`
	Reason string `json:"reason"`
}

// EvidenceKind labels a receipt's failed-hunk evidence.
const EvidenceKind = "patch_failed_hunks"

// EvidenceBytes keeps failed-hunk evidence within what program feedback
// carries per reference.
const EvidenceBytes = 512

// EvidenceRef encodes failures as compact JSON no longer than EvidenceBytes:
// {"failed":[...],"more":N}. Failures that do not fit are counted in more,
// so the receipt always states how many hunks failed.
func EvidenceRef(failures []Failure) string {
	type record struct {
		Failed []Failure `json:"failed"`
		More   int       `json:"more,omitempty"`
	}
	for kept := len(failures); kept >= 0; kept-- {
		data, err := surveyjson.Marshal(record{Failed: append([]Failure{}, failures[:kept]...), More: len(failures) - kept})
		if err == nil && len(data) <= EvidenceBytes {
			return string(data)
		}
	}
	return ""
}

// Failures lists the hunks of one file section that did not apply.
func (t Text) Failures(file string) []Failure {
	var out []Failure
	for _, hunk := range t.Hunks {
		if !hunk.Applied {
			out = append(out, Failure{File: file, Hunk: hunk.Number, Header: hunk.Header, Reason: hunk.Reason})
		}
	}
	return out
}
