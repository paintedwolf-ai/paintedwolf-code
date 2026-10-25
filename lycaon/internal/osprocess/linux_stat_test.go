package osprocess

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func statRecord(command string, modify func([]string)) []byte {
	fields := strings.Fields("S 7 42 9 0 -1 4194560 1 0 0 0 1 2 0 0 20 0 1 0 12345")
	if modify != nil {
		modify(fields)
	}
	return []byte("42 (" + command + ") " + strings.Join(fields, " "))
}

func TestParseLinuxStatPreservesKernelFacts(t *testing.T) {
	for _, command := range []string{"pw", "pw (nested) command)", "", "line\nbreak", string([]byte{0xff, 'x'})} {
		t.Run(fmt.Sprintf("%q", command), func(t *testing.T) {
			got, err := ParseLinuxStat(statRecord(command, nil))
			testutil.FailErr(t, "parse process record", err)
			want := LinuxStat{PID: 42, ParentPID: 7, SessionID: 9, StartTime: 12345, Command: command}
			if got != want {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		})
	}
	raw := statRecord("kernel", func(f []string) { f[1] = "0"; f[3] = "0"; f[19] = "18446744073709551615" })
	got, err := ParseLinuxStat(append(raw, []byte(" 0 1 2\n")...))
	if err != nil || got.ParentPID != 0 || got.SessionID != 0 || got.StartTime != ^uint64(0) {
		t.Fatalf("unsigned/trailing fields: %+v, %v", got, err)
	}
}

func TestParseLinuxStatRejectsMalformedRecords(t *testing.T) {
	cases := map[string][]byte{
		"empty": nil, "no command": []byte("42 S 7"), "no pid": []byte(" (pw) S 7"),
		"missing opening space": []byte("42(pw) S 7"), "missing closing space": []byte("42 (pw)S 7"),
		"short prefix": []byte("42 (pw) S 7 42 9"), "missing close": []byte("42 (pw S 7"),
	}
	for _, pid := range []string{"0", "-1", "+42", "x", "18446744073709551616"} {
		cases["pid "+pid] = []byte(strings.Replace(string(statRecord("pw", nil)), "42 (", pid+" (", 1))
	}
	for _, field := range []int{1, 3, 19} {
		for _, value := range []string{"-1", "+1", "x", "18446744073709551616"} {
			cases[fmt.Sprintf("field %d value %s", field, value)] = statRecord("pw", func(f []string) { f[field] = value })
		}
	}
	cases["parent native overflow"] = statRecord("pw", func(f []string) { f[1] = strconv.FormatUint(uint64(1)<<(strconv.IntSize-1), 10) })
	cases["session native overflow"] = statRecord("pw", func(f []string) { f[3] = strconv.FormatUint(uint64(1)<<(strconv.IntSize-1), 10) })
	cases["state width"] = statRecord("pw", func(f []string) { f[0] = "SS" })
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseLinuxStat(raw)
			if !errors.Is(err, ErrInvalidLinuxStat) || got != (LinuxStat{}) {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}

func TestParseLinuxStatBoundsTheEntireRecord(t *testing.T) {
	raw := statRecord("", nil)
	raw = statRecord(strings.Repeat("x", maxLinuxStatBytes-len(raw)), nil)
	if len(raw) != maxLinuxStatBytes {
		t.Fatal("bad boundary fixture")
	}
	if _, err := ParseLinuxStat(raw); err != nil {
		t.Fatalf("exact limit: %v", err)
	}
	if _, err := ParseLinuxStat(append(raw, ' ')); !errors.Is(err, ErrInvalidLinuxStat) {
		t.Fatalf("oversized: %v", err)
	}
}
