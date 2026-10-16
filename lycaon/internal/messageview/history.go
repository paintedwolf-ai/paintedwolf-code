package messageview

// MessageWindow selects a bounded tail, backward page, or ascending ordinal suffix.
type MessageWindow struct {
	AfterOrd  int64
	BeforeOrd int64
	Limit     int
	After     bool
}

// SourceTurn identifies a permanent human turn and its canonical opener.
type SourceTurn struct {
	Number           int
	OpeningMessageID string
}
