package command

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/hostprocess"
)

// A dash denotes a fact the snapshot did not observe. Native jobs have no OS
// process statistics, and a PID alone never establishes session ownership.
func writePsCustomFormat(out *strings.Builder, spec string, visible []hostprocess.VisibleProcess, statuses []jobStatus, ownedByPID map[int]jobStatus, jobs *SessionJobs) {
	rawCols := strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == ' ' })
	showHeader := true
	cols, headers := make([]string, 0, len(rawCols)), make([]string, 0, len(rawCols))
	for _, raw := range rawCols {
		if strings.HasSuffix(raw, "=") {
			showHeader = false
			raw = strings.TrimSuffix(raw, "=")
		}
		col := strings.ToLower(strings.TrimSpace(raw))
		cols = append(cols, col)
		headers = append(headers, strings.ToUpper(col))
	}
	if showHeader {
		out.WriteString(strings.Join(headers, " ") + "\n")
	}
	for _, proc := range visible {
		job, owned := ownedByPID[proc.PID]
		owned = owned && job.Process.Instance == proc.Instance
		values := make([]string, len(cols))
		for i, col := range cols {
			value := "-"
			switch col {
			case "pid":
				value = strconv.Itoa(proc.PID)
			case "ppid":
				value = strconv.Itoa(proc.ParentPID)
			case "user", "uid":
				value = strconv.FormatUint(uint64(proc.UID), 10)
			case "comm":
				value = proc.Name
			case "command", "args", "cmd":
				value = proc.Command
				if owned {
					value += fmt.Sprintf(" [session job #%d %s running]", jobs.Number(job.Handle), job.Handle)
				}
			}
			values[i] = value
		}
		out.WriteString(strings.Join(values, " ") + "\n")
	}
	for _, job := range statuses {
		if job.PID > 0 {
			continue
		}
		values := make([]string, len(cols))
		for i, col := range cols {
			value := "-"
			switch col {
			case "pid":
				value = job.Handle
			case "comm":
				value = job.Name
			case "command", "args", "cmd":
				value = fmt.Sprintf("[%d] %s running %s [session job]", jobs.Number(job.Handle), job.Handle, job.CommandLine)
			}
			values[i] = value
		}
		out.WriteString(strings.Join(values, " ") + "\n")
	}
}
