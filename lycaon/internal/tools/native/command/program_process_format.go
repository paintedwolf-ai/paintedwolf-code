package command

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/hostprocess"
)

type psColumn struct{ name, header string }

// Known columns can be unavailable in a snapshot; unknown identifiers are errors.
func parsePsColumns(spec string) ([]psColumn, error) {
	var cols []psColumn
	items := strings.Split(spec, ",")
	var fields []string
	for _, item := range items {
		if strings.Contains(item, "=") {
			fields = append(fields, strings.TrimSpace(item))
		} else {
			fields = append(fields, strings.Fields(item)...)
		}
	}
	for _, raw := range fields {
		name, header, custom := strings.Cut(raw, "=")
		name = strings.ToLower(name)
		switch name {
		case "pid", "ppid", "pgid", "user", "uid", "stat", "state", "s", "comm", "command", "args", "cmd", "pcpu", "%cpu", "pmem", "%mem", "vsz", "rss", "tty", "tt", "start", "stime", "time", "c", "etime", "etimes":
		default:
			return nil, fmt.Errorf("ps: unknown output field: %s", name)
		}
		if !custom {
			header = strings.ToUpper(name)
		}
		cols = append(cols, psColumn{name: name, header: header})
	}
	return cols, nil
}

// A dash denotes a fact the snapshot did not observe. Native jobs have no OS
// process statistics, and a PID alone never establishes session ownership.
func writePsCustomFormat(out *strings.Builder, cols []psColumn, visible []hostprocess.VisibleProcess, statuses []jobStatus, ownedByPID map[int]jobStatus, jobs *SessionJobs) {
	headers := make([]string, len(cols))
	showHeader := false
	for i, col := range cols {
		headers[i] = col.header
		showHeader = showHeader || col.header != ""
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
			switch col.name {
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
			switch col.name {
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
