package command

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hostprocess"
)

func TestProcessFormattingDoesNotFabricateStatisticsOrOwnership(t *testing.T) {
	p := hostprocess.VisibleProcess{Process: hostprocess.Process{PID: 41, ParentPID: 9, UID: 12, Name: "worker", Instance: "current"}, Command: "worker input"}
	var out strings.Builder
	cols, err := parsePsColumns("pid=,ppid=,uid=,pgid=,pcpu=,rss=,tty=,stat=,time=,command=")
	if err != nil {
		t.Fatalf("parse columns: %v", err)
	}
	writePsCustomFormat(&out, cols, []hostprocess.VisibleProcess{p}, nil,
		map[int]jobStatus{41: {Handle: "stale", Process: hostprocess.Process{PID: 41, Instance: "reused"}}}, &SessionJobs{})
	if got, want := out.String(), "41 9 12 - - - - - - worker input\n"; got != want {
		t.Fatalf("snapshot row = %q; want %q", got, want)
	}
}

func TestProcessAgeSelectionUsesKernelStartOrder(t *testing.T) {
	candidates := []procCandidate{{pid: 800, started: 10}, {pid: 20, started: 30}, {pid: 99}}
	if got := selectProcessAge(candidates, true); len(got) != 1 || got[0].pid != 20 {
		t.Fatalf("newest = %+v", got)
	}
	if got := selectProcessAge(candidates, false); len(got) != 1 || got[0].pid != 800 {
		t.Fatalf("oldest = %+v", got)
	}
}

func TestNumericJobReferenceRefusesReusedKernelInstance(t *testing.T) {
	process, err := hostprocess.Observe(os.Getpid())
	if err != nil {
		t.Fatalf("observe current process: %v", err)
	}
	process.Instance += "-stale"
	jobs := &SessionJobs{processes: map[string]jobProcess{"owned": {Identity: process}}, order: []string{"owned"}, numbers: map[string]int{"owned": 1}}
	if handle, err := jobs.Resolve(strconv.Itoa(process.PID)); err == nil {
		t.Fatalf("stale PID resolved to %q", handle)
	}
	if handle, err := jobs.Resolve("%1"); err != nil || handle != "owned" {
		t.Fatalf("stable job reference = %q, %v", handle, err)
	}
}
