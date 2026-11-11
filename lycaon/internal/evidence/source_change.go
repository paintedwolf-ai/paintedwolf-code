package evidence

// KindSourceChange is a change the source ledger recorded and the host stated in
// a turn's opening brief. It shows the change was recorded, not what it contains.
const KindSourceChange = "source_change"

// SourceChangeMint turns the kind's next free ordinal into a turn's brief and the
// records its rows cite. The store calls it only for a turn with no brief, inside
// the transaction that writes both.
type SourceChangeMint func(firstOrdinal int) (briefJSON string, records []Record, err error)

// NewSourceChangeRecord builds the record behind one stated row. It has no path, so
// a change nobody read cannot count as an observation of the file: a row joins
// neither the path index, the working set, nor a bare path citation. It has no body
// because the brief holds the row's facts, so no excerpt can match it.
func NewSourceChangeRecord(ordinal int) Record {
	return Record{
		Handle:   FormatHandle(KindSourceChange, ordinal),
		Kind:     KindSourceChange,
		Shape:    ShapeStructuredEvent,
		Fidelity: FidelityStructured,
	}
}
