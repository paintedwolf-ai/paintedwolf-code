package sourceledger

import (
	"errors"
	"math"
	"strings"

	"github.com/lycaon/lycaon/internal/textfile"
)

func attributionText(text string) string { return strings.ReplaceAll(text, "\r\n", "\n") }

func decodeTextSnapshot(raw []byte) (string, bool) {
	doc, _, err := textfile.Open(raw, textfile.LimitsForRaw(MaxRevisionContentBytes))
	if err != nil {
		return "", false
	}
	return doc.Text(), true
}

func attributedTextLength(text string) (uint32, error) {
	var length uint32
	for _, r := range text {
		units := uint32(1)
		if r > 0xffff {
			units = 2
		}
		if length > math.MaxUint32-units {
			return 0, errors.New("source text exceeds attribution capacity")
		}
		length += units
	}
	return length, nil
}
