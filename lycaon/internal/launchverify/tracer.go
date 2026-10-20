package launchverify

import (
 "errors"
 "fmt"
 "strconv"
 "strings"
)

// parseTracerPID requires exactly one kernel status field. An absent or
// malformed observation cannot establish that the process is untraced.
func parseTracerPID(status []byte) (int, error) {
 found := false
 tracer := 0
 for _, line := range strings.Split(string(status), "\n") {
  if !strings.HasPrefix(line, "TracerPid:") { continue }
  if found { return 0, errors.New("duplicate TracerPid field") }
  found = true
  fields := strings.Fields(strings.TrimPrefix(line, "TracerPid:"))
  if len(fields) != 1 { return 0, errors.New("malformed TracerPid field") }
  n, err := strconv.Atoi(fields[0])
  if err != nil || n < 0 { return 0, fmt.Errorf("invalid TracerPid %q", fields[0]) }
  tracer = n
 }
 if !found { return 0, errors.New("status has no TracerPid field") }
 return tracer, nil
}
