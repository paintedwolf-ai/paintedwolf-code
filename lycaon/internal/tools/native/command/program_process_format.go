package command

import (
	"fmt"
	"strconv"
	"strings"
	"time"

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
	memoryTotal, now := hostprocess.PhysicalMemoryBytes(), time.Now()
	headers := make([]string, len(cols))
	showHeader := false
	for i, col := range cols {
		headers[i] = col.header
		showHeader = showHeader || col.header != ""
	}
	if showHeader {
		out.WriteString(strings.Join(headers, " ") + "\n")
	}
	represented := make(map[string]bool, len(statuses))
	for _, proc := range visible {
		job, owned := ownedByPID[proc.PID]
		owned = owned && job.Process.Instance == proc.Instance
		if owned {
			represented[job.Handle] = true
		}
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
			case "pgid":
				value = "-"
			case "pcpu", "%cpu", "c":
				if proc.Stats.Available && !proc.Stats.Started.IsZero() {
					elapsed := now.Sub(proc.Stats.Started).Seconds()
					if elapsed > 0 {
						value = fmt.Sprintf("%.1f", 100*proc.Stats.CPUTime.Seconds()/elapsed)
					}
				}
			case "pmem", "%mem":
				if proc.Stats.Available && memoryTotal > 0 {
					value = fmt.Sprintf("%.1f", 100*float64(proc.Stats.ResidentBytes)/float64(memoryTotal))
				}
			case "vsz":
				if proc.Stats.Available {
					value = strconv.FormatUint(proc.Stats.VirtualBytes/1024, 10)
				}
			case "rss":
				if proc.Stats.Available {
					value = strconv.FormatUint(proc.Stats.ResidentBytes/1024, 10)
				}
			case "tty", "tt":
				if proc.Stats.TTY != "" {
					value = proc.Stats.TTY
				}
			case "stat", "state", "s":
				if proc.Stats.State != "" {
					value = proc.Stats.State
				}
			case "start", "stime":
				if !proc.Stats.Started.IsZero() {
					value = formatProcessStart(proc.Stats.Started, now)
				}
			case "time":
				if proc.Stats.Available {
					value = formatProcessDuration(proc.Stats.CPUTime)
				}
			case "etime", "etimes":
				if !proc.Stats.Started.IsZero() {
					elapsed := max(0, int(now.Sub(proc.Stats.Started).Seconds()))
					if col.name == "etimes" {
						value = strconv.Itoa(elapsed)
					} else {
						value = formatProcessDuration(time.Duration(elapsed) * time.Second)
					}
				}
			case "command", "args", "cmd":
				value = proc.Command
				if owned {
					value += fmt.Sprintf(" [session job #%d %d running]", jobs.Number(job.Handle), jobs.publicID(job.Handle))
				}
			}
			values[i] = value
		}
		out.WriteString(strings.Join(values, " ") + "\n")
	}
	for _, job := range statuses {
		if represented[job.Handle] {
			continue
		}
		values := make([]string, len(cols))
		for i, col := range cols {
			value := "-"
			switch col.name {
			case "pid":
				value = strconv.Itoa(jobs.publicID(job.Handle))
			case "comm":
				value = job.Name
			case "command", "args", "cmd":
				value = fmt.Sprintf("[%d] %d running %s [session job]", jobs.Number(job.Handle), jobs.publicID(job.Handle), job.CommandLine)
			}
			values[i] = value
		}
		out.WriteString(strings.Join(values, " ") + "\n")
	}
}

func formatProcessStart(start, now time.Time) string {
	if start.Year() == now.Year() && start.YearDay() == now.YearDay() {
		return start.Format("15:04")
	}
	return start.Format("Jan02")
}

func formatProcessDuration(d time.Duration) string {
	seconds := max(0, int64(d/time.Second))
	if seconds >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, (seconds/60)%60, seconds%60)
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}
