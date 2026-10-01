package db

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/lycaon/lycaon/internal/timelayout"
)

func FormatTime(t time.Time) string {
	return timelayout.Format(t)
}

func ParseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

func NullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func StringFromNull(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

func MarshalJSON(v any) (sql.NullString, error) {
	if v == nil {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return sql.NullString{}, err
	}
	if string(b) == "null" {
		return sql.NullString{}, nil
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

func UnmarshalJSON(ns sql.NullString, dest any) error {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	return json.Unmarshal([]byte(ns.String), dest)
}

func TimePtrFromNull(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := ParseTime(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func NullTimePtr(t *time.Time) sql.NullString {
	if t == nil || t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: FormatTime(*t), Valid: true}
}

// INPlaceholders returns n comma-separated ? markers for parameterized IN clauses.
func INPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, 2*n-1)
	for i := 0; i < n; i++ {
		if i > 0 {
			b[2*i-1] = ','
		}
		b[2*i] = '?'
	}
	return string(b)
}
