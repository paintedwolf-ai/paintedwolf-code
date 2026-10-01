package tools

// GateRepeatLedger records reason-keyed repeat counts for approval cards.
// Implemented by session.GateRepeatLedger via a thin adapter.
type GateRepeatLedger interface {
	NoteAsk(chatSessionID, reasonKey, subject string) GateRepeatSnapshot
	NoteSuppressed(chatSessionID, reasonKey, subject string)
}

// GateRepeatSnapshot is the outcome of recording a minted ask.
type GateRepeatSnapshot struct {
	ReasonKey         string
	Count             int
	Asks              int
	Subjects          []string
	SubjectsTruncated bool
	Suppressed        int
}
