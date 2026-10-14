package messageview

// MessageWindow selects a bounded tail or an ascending ordinal suffix.
type MessageWindow struct {
	AfterOrd int64
	Limit    int
	After    bool
}

// SourceTurn identifies a permanent human turn and its canonical opener.
type SourceTurn struct {
	Number           int
	OpeningMessageID string
}
