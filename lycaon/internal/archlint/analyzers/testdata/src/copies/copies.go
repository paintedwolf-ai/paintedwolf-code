package copies

import "upstream"

type Record struct {
	ID   string
	Seq  int
	Asks int
}

type Summary struct {
	ID  string
	Seq int
}

type Snapshot struct {
	ID    string
	Seq   int
	Asks  int
	Owner string
	Kind  string
	note  string
}

func twin(r upstream.Record) Record {
	return Record{ID: r.ID, Seq: r.Seq} // want `Record built from r \(Record\) omits shared field Asks`
}

func copyOf(s *Snapshot) Snapshot {
	return Snapshot{ID: s.ID, Seq: s.Seq, Owner: s.Owner} // want `Snapshot built from s \(Snapshot\) omits shared fields Asks, Kind`
}

// Seeding a new value from two of five fields is deliberate.
func seed(s Snapshot) Snapshot { return Snapshot{ID: s.ID, Kind: s.Kind} }

func complete(r upstream.Record) Record { return Record{ID: r.ID, Seq: r.Seq, Asks: r.Asks} }

// The destination has no fields for the rest of the source data.
func summarize(s Snapshot) Summary { return Summary{ID: s.ID, Seq: s.Seq} }

func single(r upstream.Record) Record { return Record{ID: r.ID} }

// The production adapter used a different type name and cloned its slice.
type GateRepeatSnapshot struct {
	ReasonKey         string
	Count             int
	Asks              int
	Subjects          []string
	SubjectsTruncated bool
	Suppressed        bool
}

func droppedAsk(snap upstream.RepeatSnapshot) GateRepeatSnapshot {
	return GateRepeatSnapshot{ // want `GateRepeatSnapshot built from snap \(RepeatSnapshot\) omits shared field Asks`
		ReasonKey:         snap.ReasonKey,
		Count:             snap.Count,
		Subjects:          append([]string(nil), snap.Subjects...),
		SubjectsTruncated: snap.SubjectsTruncated,
		Suppressed:        snap.Suppressed,
	}
}

func completeAsk(snap upstream.RepeatSnapshot) GateRepeatSnapshot {
	return GateRepeatSnapshot{
		ReasonKey:         snap.ReasonKey,
		Count:             snap.Count,
		Asks:              snap.Asks,
		Subjects:          append([]string(nil), snap.Subjects...),
		SubjectsTruncated: snap.SubjectsTruncated,
		Suppressed:        snap.Suppressed,
	}
}
