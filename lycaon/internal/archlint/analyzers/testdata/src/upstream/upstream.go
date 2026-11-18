package upstream

type Record struct {
	ID   string
	Seq  int
	Asks int
}

func Open(path string, mode int) error { return nil }

func Close() {}

// RepeatSnapshot is the source shape of the repeat-approval adapter.
type RepeatSnapshot struct {
	ReasonKey         string
	Count             int
	Asks              int
	Subjects          []string
	SubjectsTruncated bool
	Suppressed        bool
}
