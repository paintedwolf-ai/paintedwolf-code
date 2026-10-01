package db

import (
	"database/sql"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFormatParseTimeRoundTrip(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 123456789, time.UTC)
	formatted := FormatTime(now)
	parsed, err := ParseTime(formatted)
	testutil.FailErr(t, "ParseTime failed", err)
	if !parsed.Equal(now) {
		t.Fatalf("parsed = %v want %v", parsed, now)
	}
	zero, err := ParseTime("")
	if err != nil || !zero.IsZero() {
		t.Fatalf("empty parse = %v err=%v", zero, err)
	}
}

func TestNullStringHelpers(t *testing.T) {
	if ns := NullString(""); ns.Valid {
		t.Fatal("empty should be invalid")
	}
	if ns := NullString("x"); !ns.Valid || ns.String != "x" {
		t.Fatalf("ns = %+v", ns)
	}
	if got := StringFromNull(sql.NullString{String: "y", Valid: true}); got != "y" {
		t.Fatalf("got = %q", got)
	}
	if got := StringFromNull(sql.NullString{}); got != "" {
		t.Fatalf("got = %q", got)
	}
}

func TestMarshalUnmarshalJSON(t *testing.T) {
	ns, err := MarshalJSON(nil)
	if err != nil || ns.Valid {
		t.Fatalf("nil = %+v err=%v", ns, err)
	}
	ns, err = MarshalJSON(map[string]any{"a": 1})
	if err != nil || !ns.Valid || ns.String != `{"a":1}` {
		t.Fatalf("object = %+v err=%v", ns, err)
	}
	var out map[string]any
	if err := UnmarshalJSON(sql.NullString{}, &out); err != nil {
		testutil.FailErr(t, "UnmarshalJSON failed", err)
	}
	if err := UnmarshalJSON(ns, &out); err != nil || out["a"].(float64) != 1 {
		t.Fatalf("out = %+v err=%v", out, err)
	}
}

func TestTimePtrHelpers(t *testing.T) {
	if ns := NullTimePtr(nil); ns.Valid {
		t.Fatal("nil time should be invalid")
	}
	now := time.Now().UTC()
	ns := NullTimePtr(&now)
	ptr, err := TimePtrFromNull(ns)
	if err != nil || ptr == nil || !ptr.Equal(now) {
		t.Fatalf("ptr = %v err=%v", ptr, err)
	}
	ptr, err = TimePtrFromNull(sql.NullString{})
	if err != nil || ptr != nil {
		t.Fatalf("empty = %v err=%v", ptr, err)
	}
}

func TestINPlaceholders(t *testing.T) {
	if got := INPlaceholders(0); got != "" {
		t.Fatalf("0 = %q", got)
	}
	if got := INPlaceholders(3); got != "?,?,?" {
		t.Fatalf("3 = %q", got)
	}
}
