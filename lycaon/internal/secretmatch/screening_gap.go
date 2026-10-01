package secretmatch

// ScreeningGap names why part of an outbound payload could not be read by the
// screen. An alert carrying a gap asks about content the host could not check,
// with or without a rule match.
type ScreeningGap string

const (
	// GapOCRUnavailable means this device has no text recognition for images.
	GapOCRUnavailable ScreeningGap = "ocr_unavailable"
	// GapOCRFailed means text recognition ran and returned an error.
	GapOCRFailed ScreeningGap = "ocr_failed"
	// GapEmbeddedReference means an SVG draws an image the screen could not
	// read: a file the renderer loads, or an embedded image it cannot decode.
	GapEmbeddedReference ScreeningGap = "embedded_reference"
)

// UnscreenedFingerprint is the stable subject of a gap alert. Release grants
// bind it with the reviewed recipients, so one answer covers every later
// unscreened send to the same destination on the same surface. The prefix
// keeps it disjoint from keyed value fingerprints.
const UnscreenedFingerprint SecretFingerprint = "gap1_unscreened_content"

// UnscreenedRuleID is the rule identity of an alert raised only because
// content could not be screened. Quiet and grant subjects key on it.
const UnscreenedRuleID = "unscreened_content"

// Label is the sentence-case reason shown on the card.
func (g ScreeningGap) Label() string {
	switch g {
	case GapOCRUnavailable:
		return "Image text not screened: text recognition is unavailable on this device"
	case GapOCRFailed:
		return "Image text not screened: text recognition failed for this image"
	case GapEmbeddedReference:
		return "Image not fully screened: it draws in another image the host could not read"
	default:
		return ""
	}
}

// RedactionNote explains why the redacted send is unavailable.
func (g ScreeningGap) RedactionNote() string {
	switch g {
	case GapOCRUnavailable, GapOCRFailed, GapEmbeddedReference:
		return "Redaction is not offered here: the host could not read the text in this image, so it cannot find or mask a credential in it."
	default:
		return ""
	}
}

// Valid reports whether g is a known gap.
func (g ScreeningGap) Valid() bool {
	return g == GapOCRUnavailable || g == GapOCRFailed || g == GapEmbeddedReference
}
