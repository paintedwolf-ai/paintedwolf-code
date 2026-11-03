package datetext

import (
	"errors"
	"testing"
	"time"
)

// now is Wednesday 2026-10-28 05:15:31.25 UTC; expectations were recorded
// from GNU date 9 and the macOS date at that instant.
var now = time.Date(2026, 10, 28, 5, 15, 31, 250_000_000, time.UTC)

func TestParseReadsTheDateGrammar(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"", "2026-10-28 00:00:00.000000000 +0000"},
		{"now", "2026-10-28 05:15:31.250000000 +0000"},
		{"today", "2026-10-28 05:15:31.250000000 +0000"},
		{"yesterday", "2026-10-27 05:15:31.250000000 +0000"},
		{"tomorrow", "2026-10-29 05:15:31.250000000 +0000"},
		{"2 days 3 hours ago", "2026-10-30 02:15:31.250000000 +0000"},
		{"1 day ago", "2026-10-27 05:15:31.250000000 +0000"},
		{"-1 day", "2026-10-27 05:15:31.250000000 +0000"},
		{"+2 weeks", "2026-11-11 05:15:31.250000000 +0000"},
		{"week", "2026-11-04 05:15:31.250000000 +0000"},
		{"1 fortnight", "2026-11-11 05:15:31.250000000 +0000"},
		{"fortnight ago", "2026-10-14 05:15:31.250000000 +0000"},
		{"next year", "2027-10-28 05:15:31.250000000 +0000"},
		{"last day", "2026-10-27 05:15:31.250000000 +0000"},
		{"1 month ago", "2026-09-28 05:15:31.250000000 +0000"},
		{"yesterday 10:00", "2026-10-27 10:00:00.000000000 +0000"},
		{"10:00 yesterday", "2026-10-27 10:00:00.000000000 +0000"},
		{"monday", "2026-11-02 00:00:00.000000000 +0000"},
		{"monday 10:00", "2026-11-02 10:00:00.000000000 +0000"},
		{"next monday", "2026-11-02 00:00:00.000000000 +0000"},
		{"last monday", "2026-10-26 00:00:00.000000000 +0000"},
		{"wednesday", "2026-10-28 00:00:00.000000000 +0000"},
		{"next wednesday", "2026-11-04 00:00:00.000000000 +0000"},
		{"last wednesday", "2026-10-21 00:00:00.000000000 +0000"},
		{"this wednesday", "2026-10-28 00:00:00.000000000 +0000"},
		{"last friday", "2026-10-23 00:00:00.000000000 +0000"},
		{"tues", "2026-11-03 00:00:00.000000000 +0000"},
		{"2026-10-26", "2026-10-26 00:00:00.000000000 +0000"},
		{"2026-10-26 +1 day", "2026-10-27 00:00:00.000000000 +0000"},
		{"2026-10-26 +1 day -2 hours", "2026-10-26 22:00:00.000000000 +0000"},
		{"2026-10-26 12:00 +1 day", "2026-10-27 11:00:00.000000000 +0000"},
		{"2026-01-31 +1 month", "2026-03-03 00:00:00.000000000 +0000"},
		{"2026-10-26 1230", "2026-10-26 12:30:00.000000000 +0000"},
		{"2026-10-26 10:00:00.5", "2026-10-26 10:00:00.500000000 +0000"},
		{"2026-10-26T10:30:00Z", "2026-10-26 10:30:00.000000000 +0000"},
		{"2026-10-26T10:30:00.123Z", "2026-10-26 10:30:00.123000000 +0000"},
		{"2026-10-26T10:00:00+02:00", "2026-10-26 08:00:00.000000000 +0000"},
		{"2026-10-26 10:00 -05:00", "2026-10-26 15:00:00.000000000 +0000"},
		{"2026-10-26 10:00 +0530", "2026-10-26 04:30:00.000000000 +0000"},
		{"2026-10-26 12:00 UTC", "2026-10-26 12:00:00.000000000 +0000"},
		{"2026-10-26 EST", "2026-10-26 05:00:00.000000000 +0000"},
		{"UTC", "2026-10-28 00:00:00.000000000 +0000"},
		{"UTC+2", "2026-10-27 22:00:00.000000000 +0000"},
		{"now UTC", "2026-10-28 05:15:31.250000000 +0000"},
		{"Mon, 26 Oct 2026 10:00:00 +0200", "2026-10-26 08:00:00.000000000 +0000"},
		{"Oct 26", "2026-10-26 00:00:00.000000000 +0000"},
		{"26 Oct", "2026-10-26 00:00:00.000000000 +0000"},
		{"Oct 26 2026", "2026-10-26 00:00:00.000000000 +0000"},
		{"Oct 26, 2026", "2026-10-26 00:00:00.000000000 +0000"},
		{"26 Oct 2026 3pm", "2026-10-26 15:00:00.000000000 +0000"},
		{"26-Oct-2026", "2026-10-26 00:00:00.000000000 +0000"},
		{"Oct-26-2026", "2026-10-26 00:00:00.000000000 +0000"},
		{"2026/10/26", "2026-10-26 00:00:00.000000000 +0000"},
		{"10/26/2026", "2026-10-26 00:00:00.000000000 +0000"},
		{"10/26/26", "2026-10-26 00:00:00.000000000 +0000"},
		{"20261026", "2026-10-26 00:00:00.000000000 +0000"},
		{"12:30", "2026-10-28 12:30:00.000000000 +0000"},
		{"12am", "2026-10-28 00:00:00.000000000 +0000"},
		{"12pm", "2026-10-28 12:00:00.000000000 +0000"},
		{"12:30am", "2026-10-28 00:30:00.000000000 +0000"},
		{"3 pm", "2026-10-28 15:00:00.000000000 +0000"},
		{"3p.m.", "2026-10-28 15:00:00.000000000 +0000"},
		{"3pm yesterday", "2026-10-27 15:00:00.000000000 +0000"},
		{"@1700000000", "2023-11-14 22:13:20.000000000 +0000"},
		{"@0.5", "1970-01-01 00:00:00.500000000 +0000"},
	} {
		got, err := Parse(c.text, now, time.UTC)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.text, err)
			continue
		}
		if s := got.Format("2006-01-02 15:04:05.000000000 -0700"); s != c.want {
			t.Errorf("Parse(%q) = %s, want %s", c.text, s, c.want)
		}
	}
}

func TestParseRefusesTextOutsideTheGrammar(t *testing.T) {
	for _, text := range []string{"foo", "noon", "2026-02-30", "2026-10-26 24:00", "@0 +1 day", "1.5 days", "next", "2026-10-26 2026-10-27", "10:00 11:00", "monday tuesday", "13/01/2026", "3:61", "13pm", "ago", "_"} {
		if _, err := Parse(text, now, time.UTC); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) = %v, want ErrInvalid", text, err)
		}
	}
}

func TestParseReadsZonelessTextInTheGivenZone(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("zone database: %v", err)
	}
	got, err := Parse("2026-10-26 12:00", now, newYork)
	if err != nil || Format(got, Default) != "Mon Oct 26 12:00:00 EDT 2026" || got.UTC().Hour() != 16 {
		t.Fatalf("Parse in New York = %v, %v", got, err)
	}
	got, err = Parse("2026-10-26 12:00 UTC", now, newYork)
	if err != nil || got.Format(time.RFC3339) != "2026-10-26T08:00:00-04:00" {
		t.Fatalf("Parse with an explicit zone = %v, %v", got, err)
	}
}

func TestFormatRendersEveryConversion(t *testing.T) {
	at := time.Date(2026, 10, 26, 15, 4, 5, 0, time.UTC)
	got := Format(at, "%a|%A|%b|%B|%c|%C|%d|%D|%e|%F|%g|%G|%H|%I|%j|%k|%l|%m|%M|%p|%P|%q|%r|%R|%s|%S|%T|%u|%U|%V|%w|%W|%x|%X|%y|%Y|%z|%:z|%::z|%:::z|%Z|%-d|%_m|%05Y|%^a|%#Z|%3N|%Q|%-e|%10A|%#p|%h|%n%t%%")
	want := "Mon|Monday|Oct|October|Mon Oct 26 15:04:05 2026|20|26|10/26/26|26|2026-10-26|26|2026|15|03|299|15| 3|10|04|PM|pm|4|03:04:05 PM|15:04|1793027045|05|15:04:05|1|43|44|1|43|10/26/26|15:04:05|26|2026|+0000|+00:00|+00:00:00|+00|UTC|26|10|02026|MON|utc|000|%Q|26|    Monday|pm|Oct|\n\t%"
	if got != want {
		t.Fatalf("Format =\n%q\nwant\n%q", got, want)
	}
	if got := Format(at, Default); got != "Mon Oct 26 15:04:05 UTC 2026" {
		t.Fatalf("default format = %q", got)
	}
	early := time.Date(2026, 3, 5, 9, 7, 0, 123456789, time.FixedZone("", 5*3600+1800))
	if got := Format(early, "%e|%k|%N|%6N|%z|%:::z|%Z|%-m/%-d"); got != " 5| 9|123456789|123456|+0530|+05:30|+0530|3/5" {
		t.Fatalf("padding and zones = %q", got)
	}
	if got := Format(at, "%"); got != "%" {
		t.Fatalf("trailing percent = %q", got)
	}
}

func TestParseFormatKeepsUnsetFieldsFromTheBase(t *testing.T) {
	for _, c := range []struct{ format, text, want, rest string }{
		{"%Y-%m-%d", "2026-10-26", "2026-10-26 05:15:31", ""},
		{"%H:%M", "10:30", "2026-10-28 10:30:31", ""},
		{"%s", "100", "1970-01-01 00:01:40", ""},
		{"%Y", "2020", "2020-10-28 05:15:31", ""},
		{"%m/%d", "02/30", "2026-03-02 05:15:31", ""},
		{"%y%m%d", "261026", "2026-10-26 05:15:31", ""},
		{"%Y-%m-%dT%H:%M:%S%z", "2026-10-26T10:00:00+0200", "2026-10-26 08:00:00", ""},
		{"%a %b %d %T %Z %Y", "Mon Oct 26 10:00:00 UTC 2026", "2026-10-26 10:00:00", ""},
		{"%b %d %Y", "Oct 26 2026", "2026-10-26 05:15:31", ""},
		{"%F %I:%M %p", "2026-10-26 03:04 PM", "2026-10-26 15:04:31", ""},
		{"%j", "60", "2026-10-28 05:15:31", ""},
		{"%Y-%m-%d", "2026-10-26x", "2026-10-26 05:15:31", "x"},
		{"%D %R", "10/26/26 09:30", "2026-10-26 09:30:31", ""},
	} {
		got, rest, err := ParseFormat(c.format, c.text, now)
		if err != nil || got.Format("2006-01-02 15:04:05") != c.want || rest != c.rest {
			t.Errorf("ParseFormat(%q, %q) = %v rest %q, %v; want %s rest %q", c.format, c.text, got, rest, err, c.want, c.rest)
		}
	}
	for _, c := range []struct{ format, text string }{{"%Y-%m-%d", "nope"}, {"%m", "13"}, {"%H:%M", "10-30"}, {"%Q", "x"}, {"%Z", "XYZ"}, {"%p", "noon"}} {
		if _, _, err := ParseFormat(c.format, c.text, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseFormat(%q, %q) = %v, want ErrInvalid", c.format, c.text, err)
		}
	}
}

func TestAdjustMovesAndSetsFields(t *testing.T) {
	day := func(s string) time.Time {
		v, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatalf("fixture %s: %v", s, err)
		}
		return v
	}
	for _, c := range []struct {
		from  string
		specs []string
		want  string
	}{
		{"2026-10-28", []string{"+wed"}, "2026-10-28"},
		{"2026-10-28", []string{"-wed"}, "2026-10-28"},
		{"2026-10-28", []string{"+thu"}, "2026-10-29"},
		{"2026-10-28", []string{"-thu"}, "2026-10-22"},
		{"2026-10-28", []string{"mon"}, "2026-10-26"},
		{"2026-10-28", []string{"+jan"}, "2027-01-28"},
		{"2026-10-28", []string{"-oct"}, "2026-10-28"},
		{"2026-10-28", []string{"-jan"}, "2026-01-28"},
		{"2026-10-28", []string{"mar"}, "2026-03-28"},
		{"2026-10-28", []string{"+1w"}, "2026-11-04"},
		{"2026-10-28", []string{"2026y"}, "2026-10-28"},
		{"2026-10-28", []string{"99y"}, "1999-10-28"},
		{"2026-10-28", []string{"5y"}, "2005-10-28"},
		{"2026-10-28", []string{"3m"}, "2026-03-28"},
		{"2026-10-28", []string{"15d"}, "2026-10-15"},
		{"2026-10-26", []string{"-1d"}, "2026-10-25"},
		{"2026-01-31", []string{"+1m", "-3d"}, "2026-02-25"},
		{"2026-03-31", []string{"-1m"}, "2026-02-28"},
		{"2026-01-31", []string{"+13m"}, "2027-02-28"},
		{"2024-02-29", []string{"+1y"}, "2025-03-01"},
		{"2026-03-15", []string{"1d", "-1d"}, "2026-02-28"},
	} {
		got := day(c.from)
		for _, spec := range c.specs {
			var err error
			if got, err = Adjust(got, spec); err != nil {
				t.Fatalf("Adjust(%s, %s): %v", c.from, spec, err)
			}
		}
		if got.Format("2006-01-02") != c.want {
			t.Errorf("Adjust(%s, %v) = %s, want %s", c.from, c.specs, got.Format("2006-01-02"), c.want)
		}
	}
	clock := time.Date(2026, 10, 28, 5, 15, 31, 0, time.UTC)
	for spec, want := range map[string]string{"+1H": "06:15:31", "-30M": "04:45:31", "+45S": "05:16:16", "12H": "12:15:31", "0M": "05:00:31", "7S": "05:15:07"} {
		if got, err := Adjust(clock, spec); err != nil || got.Format("15:04:05") != want {
			t.Errorf("Adjust(%s) = %v, %v; want %s", spec, got, err, want)
		}
	}
	for _, spec := range []string{"", "+", "+1x", "1", "+1dd", "+xyz", "13m", "32d", "24H", "60M", "61S", "3w"} {
		if _, err := Adjust(clock, spec); !errors.Is(err, ErrInvalid) {
			t.Errorf("Adjust(%q) = %v, want ErrInvalid", spec, err)
		}
	}
}

func TestZoneReadsTheProgramTZ(t *testing.T) {
	for _, c := range []struct {
		env    []string
		name   string
		offset int
	}{
		{nil, "UTC", 0},
		{[]string{"TZ="}, "UTC", 0},
		{[]string{"TZ=UTC0"}, "UTC", 0},
		{[]string{"PATH=/bin", "TZ=America/New_York"}, "EDT", -4 * 3600},
		{[]string{"TZ=:Asia/Kolkata"}, "IST", 5*3600 + 1800},
		{[]string{"TZ=ABC3"}, "ABC", -3 * 3600},
		{[]string{"TZ=<+0530>-5:30"}, "+0530", 5*3600 + 1800},
		{[]string{"TZ=Nowhere/Special"}, "Nowhere/Special", 0},
		{[]string{"TZ=X"}, "X", 0},
	} {
		at := time.Date(2026, 10, 26, 12, 0, 0, 0, Zone(c.env))
		if name, offset := at.Zone(); name != c.name || offset != c.offset {
			t.Errorf("Zone(%v) = %s %d, want %s %d", c.env, name, offset, c.name, c.offset)
		}
	}
}
