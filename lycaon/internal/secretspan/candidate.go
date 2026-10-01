package secretspan

import (
	"unicode"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

// Ineligible explains why a selected range cannot be protected.
type Ineligible string

const (
	// IneligibleOutOfRange is a range the document does not contain.
	IneligibleOutOfRange Ineligible = "out_of_range"
	// IneligibleEmpty is a selection that is only quotes and whitespace.
	IneligibleEmpty Ineligible = "empty"
	// IneligibleTooLarge is above the protected-value ceiling.
	IneligibleTooLarge Ineligible = "too_large"

	// IneligibleTooShort is below the exact-match screening floor.
	IneligibleTooShort Ineligible = "too_short"
	// IneligibleAlreadyProtected holds bytes a managed secret already protects.
	IneligibleAlreadyProtected Ineligible = "already_protected"
)

// Candidate describes the captured range and shape without including its bytes.
type Candidate struct {
	// Start and End are rune offsets after trimming.
	Start, End int
	RuneLength int
	ByteLength int
	Shape      string
	// TrimmedLeading and TrimmedTrailing count removed runes.
	TrimmedLeading, TrimmedTrailing int
	Eligible                        bool
	Reason                          Ineligible
}

// quotePairs defines the optional delimiter layer removed during trimming.
var quotePairs = map[rune]rune{'"': '"', '\'': '\'', '`': '`'}

// Propose resolves a selected rune range against document text and reports what
// would be captured. Trim removes surrounding whitespace and one quote layer.
func Propose(text string, start, end int, trim bool) Candidate {
	runes := []rune(text)
	if start < 0 || end > len(runes) || end <= start {
		return Candidate{Reason: IneligibleOutOfRange}
	}
	lead, trail := 0, 0
	if trim {
		start, end, lead, trail = trimRange(runes, start, end)
	}
	if end <= start {
		return Candidate{TrimmedLeading: lead, TrimmedTrailing: trail, Reason: IneligibleEmpty}
	}
	value := string(runes[start:end])
	out := Candidate{
		Start: start, End: end, RuneLength: end - start, ByteLength: len(value),
		Shape: secretmatch.GenericShape(value), TrimmedLeading: lead, TrimmedTrailing: trail,
	}
	switch {
	case out.ByteLength > secretmatch.MaxSecretBytes:
		out.Reason = IneligibleTooLarge
	case out.RuneLength < secretmatch.MinManagedSecretRunes:
		out.Reason = IneligibleTooShort
	default:
		out.Eligible = true
	}
	return out
}

// trimRange removes whitespace around one matched quote layer.
func trimRange(runes []rune, start, end int) (int, int, int, int) {
	origStart, origEnd := start, end
	start, end = trimSpace(runes, start, end)
	if end-start >= 2 {
		if closing, ok := quotePairs[runes[start]]; ok && runes[end-1] == closing {
			start, end = start+1, end-1
			start, end = trimSpace(runes, start, end)
		}
	}
	return start, end, start - origStart, origEnd - end
}

func trimSpace(runes []rune, start, end int) (int, int) {
	for start < end && unicode.IsSpace(runes[start]) {
		start++
	}
	for end > start && unicode.IsSpace(runes[end-1]) {
		end--
	}
	return start, end
}

// Slice reads a rune range from the host document copy.
func Slice(text string, start, end int) (string, bool) {
	if start < 0 || end <= start {
		return "", false
	}
	if !utf8.ValidString(text) {
		return "", false
	}
	runes := []rune(text)
	if end > len(runes) {
		return "", false
	}
	return string(runes[start:end]), true
}
