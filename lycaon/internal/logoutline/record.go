package logoutline

import "time"

// RecordParser turns one raw line into typed fields for a single format.
type RecordParser interface {
	Format() LogFormat
	Parse(line []byte) (Record, bool)
}

// Record is one parsed log line.
type Record struct {
	Line     int
	Time     time.Time
	Severity string
	Message  string
	Fields   map[string]string
}
