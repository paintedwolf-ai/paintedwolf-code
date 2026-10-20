package launchverify

import "testing"

func TestParseTracerPID(t *testing.T) {
 for _, tc := range []struct { name, status string; want int; invalid bool }{
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
   if (err != nil) != tc.invalid || got != tc.want { t.Fatalf("got %d, %v; want %d, invalid=%v", got, err, tc.want, tc.invalid) }
  })
 }
}
