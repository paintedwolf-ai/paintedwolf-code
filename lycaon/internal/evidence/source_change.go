package evidence

import "encoding/json"

// KindSourceChange is a change the source ledger recorded and the host stated in
// a turn's opening brief. It shows the change was recorded, not what it contains.
const KindSourceChange = "source_change"

// SourceChangeMint turns the kind's next free ordinal into a turn's brief and the
// records its rows cite. The store calls it only for a turn with no brief, inside
// the transaction that writes both.
type SourceChangeMint func(firstOrdinal int) (briefJSON string, records []Record, err error)

// NewSourceChangeRecord builds the record for one stated row. It stays pathless so
// a change nobody read cannot count as an observation of the file: a row joins
// neither the path index, the working set, nor a bare path citation. The body is
// the row's facts, the only thing an excerpt can match.
func NewSourceChangeRecord(ordinal int, row any) (Record, error) {
	body, err := json.Marshal(row)
	if err != nil {
		return Record{}, err
	}
	return Record{
		Handle:   FormatHandle(KindSourceChange, ordinal),
		Kind:     KindSourceChange,
		Shape:    ShapeStructuredEvent,
		Fidelity: FidelityStructured,
		Body:     []string{string(body)},
	}, nil
}
