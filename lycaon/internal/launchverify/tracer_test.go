package launchverify

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestParseTracerPID(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		want         int
		invalid      bool
	}{
		{"untraced", "TracerPid:\t0\n", 0, false},
		{"traced", "TracerPid:\t123\n", 123, false},
		{"missing", "Uid:\t1000\n", 0, true},
		{"negative", "TracerPid:\t-1\n", 0, true},
		{"multiple values", "TracerPid:\t0 1\n", 0, true},
		{"duplicate", "TracerPid:\t0\nTracerPid:\t0\n", 0, true},
		{"malformed", "TracerPid:\tx\n", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTracerPID([]byte(tc.status))
			if (err != nil) != tc.invalid || got != tc.want {
				t.Fatalf("got %d, %v; want %d, invalid=%v", got, err, tc.want, tc.invalid)
			}
		})
	}
}

func TestVerifyChainRefusesTracingBeforeAndAfterVerification(t *testing.T) {
	for _, pid := range []int{300, 301} {
		for _, after := range []bool{false, true} {
			t.Run(fmt.Sprintf("pid%d-after%v", pid, after), func(t *testing.T) {
				proc, v := chainFixture(t)
				var chain Chain
				var err error
				if after {
					chain, err = v.VerifyChain(301, 1000)
					testutil.FailErr(t, "verify original chain", err)
				}
				path := filepath.Join(proc.root, fmt.Sprint(pid), "status")
				raw, err := os.ReadFile(path)
				testutil.FailErr(t, "read status", err)
				raw = []byte(strings.Replace(string(raw), "TracerPid:\t0", "TracerPid:\t42", 1))
				testutil.FailErr(t, "write traced status", os.WriteFile(path, raw, 0600))
				if after {
					err = v.Recheck(chain)
				} else {
					_, err = v.VerifyChain(301, 1000)
				}
				if CodeOf(err) != CodeProcessTraced {
					t.Fatalf("got %v, want process_traced", err)
				}
			})
		}
	}
}
