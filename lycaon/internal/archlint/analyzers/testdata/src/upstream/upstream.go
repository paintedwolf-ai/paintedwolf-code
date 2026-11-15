package upstream

type Record struct {
	ID   string
	Seq  int
	Asks int
}

func Open(path string, mode int) error { return nil }

func Close() {}
