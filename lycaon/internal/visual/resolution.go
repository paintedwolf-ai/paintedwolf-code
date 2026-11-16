package visual

import (
	"github.com/lycaon/lycaon/pkg/api"
)

// AbsenceReason gives display copy for an artifact without bytes.
type AbsenceReason string

const (
	// AbsenceUnknown means no artifact by that id is known in this scope.
	AbsenceUnknown AbsenceReason = "unknown"
	// AbsenceForeign means the id belongs to a different session tree.
	AbsenceForeign AbsenceReason = "foreign"
	// AbsenceUnavailable means a record exists without readable content.
	AbsenceUnavailable AbsenceReason = "unavailable"
	// AbsenceDeleted means the artifact has a durable tombstone.
	AbsenceDeleted AbsenceReason = "deleted"
)

// AbsenceReasons returns every reason.
func AbsenceReasons() []AbsenceReason {
	return []AbsenceReason{AbsenceUnknown, AbsenceForeign, AbsenceUnavailable, AbsenceDeleted}
}

// Note returns display copy.
func (r AbsenceReason) Note() string {
	switch r {
	case AbsenceForeign:
		return "That artifact belongs to a different chat."
	case AbsenceUnavailable:
		return "That artifact's content is unavailable."
	case AbsenceDeleted:
		return "That artifact was deleted."
	case AbsenceUnknown:
		return "That artifact is not available."
	}
	return AbsenceUnknown.Note()
}

// Resolution is either present with bytes or absent with a reason.
type Resolution struct {
	present bool
	meta    api.VisualArtifact
	bytes   []byte
	reason  AbsenceReason
}

// Present requires non-empty content.
func Present(meta api.VisualArtifact, raw []byte) Resolution {
	if len(raw) == 0 {
		return Absent(AbsenceUnavailable)
	}
	meta.StoreRef = true
	return Resolution{present: true, meta: meta, bytes: raw}
}

// Absent normalizes unknown reasons.
func Absent(reason AbsenceReason) Resolution {
	switch reason {
	case AbsenceForeign, AbsenceUnavailable, AbsenceDeleted, AbsenceUnknown:
	default:
		reason = AbsenceUnknown
	}
	return Resolution{reason: reason}
}

func (r Resolution) IsPresent() bool { return r.present }

// Meta is the artifact wire object; zero when absent.
func (r Resolution) Meta() api.VisualArtifact { return r.meta }

// Bytes are the artifact content; nil when absent.
func (r Resolution) Bytes() []byte { return r.bytes }

// Reason is empty for a present artifact.
func (r Resolution) Reason() AbsenceReason {
	if r.present {
		return ""
	}
	if r.reason == "" {
		return AbsenceUnknown
	}
	return r.reason
}

// Note is the human sentence for an absent resolution, empty when present.
func (r Resolution) Note() string {
	if r.present {
		return ""
	}
	return r.Reason().Note()
}
