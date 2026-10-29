package native

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestProcessListRequiresOwnerReview(t *testing.T) {
	service, err := hostprocess.New()
	testutil.FailErr(t, "create process service", err)
	native := ProcessTools{Service: service}
	denied := errors.New("review denied")
	calls := 0
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "task"},
		Execution: tools.InvocationExecution{ProcessReview: func(_ context.Context, operation string, processes []hostprocess.Process) error {
			calls++
			if operation != "list" || len(processes) != 0 {
				t.Fatal("unexpected review subject")
			}
			return denied
		}},
	}
	result, err := native.List(t.Context(), map[string]any{"pid": os.Getpid()}, tc)
	if !errors.Is(err, denied) || result != "" || calls != 1 {
		t.Fatalf("list bypassed review: %q %v %d", result, err, calls)
	}
	_, err = native.List(t.Context(), nil, tools.ToolContext{})
	if toolrejection.AsToolReject(err) == nil {
		t.Fatal("list without reviewer was not rejected")
	}
}

// References are bound to the session whose launch started the process; the
// review sees the host-resolved instance, and nothing else reaches review.
func TestProcessSignalReferencesStayWithTheOwningSessionAndStopAtReview(t *testing.T) {
	service, err := hostprocess.New()
	testutil.FailErr(t, "create process service", err)
	line, err := lineage.Open("command:call_1", "worker")
	testutil.FailErr(t, "open lineage", err)
	t.Cleanup(func() { _ = line.Close() })
	child := exec.Command("/bin/sleep", "60")
	child.ExtraFiles = []*os.File{line.ChildFile()}
	testutil.FailErr(t, "start owned child", child.Start())
	line.Started()
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	unowned := exec.Command("/bin/sleep", "60")
	testutil.FailErr(t, "start unowned child", unowned.Start())
	t.Cleanup(func() { _ = unowned.Process.Kill(); _ = unowned.Wait() })

	disownLine, err := lineage.Open("command:call_2", "worker")
	testutil.FailErr(t, "open disown lineage", err)
	t.Cleanup(func() { _ = disownLine.Close() })
	disowned := exec.Command("/bin/sh", "-c", "read _; exec 3>&-; echo dropped; read _")
	disowned.ExtraFiles = []*os.File{disownLine.ChildFile()}
	disownIn, err := disowned.StdinPipe()
	testutil.FailErr(t, "disowned stdin", err)
	disownOut, err := disowned.StdoutPipe()
	testutil.FailErr(t, "disowned stdout", err)
	testutil.FailErr(t, "start disowned child", disowned.Start())
	disownLine.Started()
	t.Cleanup(func() { _ = disownIn.Close(); _ = disowned.Process.Kill(); _ = disowned.Wait() })

	denied := errors.New("review denied")
	allow := false
	reviewed := 0
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "worker", ParentSessionID: "task"},
		Execution: tools.InvocationExecution{ProcessReview: func(_ context.Context, operation string, targets []hostprocess.Process) error {
			if operation == "list" {
				return nil
			}
			reviewed++
			if operation != "signal" || len(targets) != 1 || targets[0].PID != child.Process.Pid || !targets[0].Owned {
				t.Fatal("incorrect resolved review target")
			}
			if allow {
				return nil
			}
			return denied
		}},
	}
	native := ProcessTools{Service: service}
	reference := ""
	disownedRef := ""
	for _, pid := range []int{child.Process.Pid, unowned.Process.Pid, disowned.Process.Pid} {
		raw, err := native.List(t.Context(), map[string]any{"pid": pid}, tc)
		testutil.FailErr(t, "list process", err)
		var listed struct{ Processes []hostprocess.Process }
		testutil.FailErr(t, "decode listing", json.Unmarshal([]byte(raw), &listed))
		if len(listed.Processes) != 1 {
			t.Fatalf("listing %s", raw)
		}
		row := listed.Processes[0]
		if pid == unowned.Process.Pid {
			if row.Owned || row.Reference != "" {
				t.Fatalf("a process the session did not start carries a reference: %s", raw)
			}
			continue
		}
		if !row.Owned || row.Reference == "" {
			t.Fatalf("the session's own process carries no reference: %s", raw)
		}
		if pid == disowned.Process.Pid {
			disownedRef = row.Reference
		} else {
			reference = row.Reference
		}
	}
	args := map[string]any{"references": []any{reference}, "signal": "TERM"}
	if _, err := native.Signal(t.Context(), args, tc); !errors.Is(err, denied) || reviewed != 1 {
		t.Fatalf("owned reference did not reach review: %v", err)
	}
	tc.Identity.SessionID = "task"
	_, err = native.Signal(t.Context(), args, tc)
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "PROCESS_REFERENCE_STALE" || reviewed != 1 {
		t.Fatalf("reference crossed the session boundary: %v", err)
	}
	tc.Identity.SessionID = "worker"
	_, err = disownIn.Write([]byte("\n"))
	testutil.FailErr(t, "release lineage fd in child", err)
	lineText, err := bufio.NewReader(disownOut).ReadString('\n')
	testutil.FailErr(t, "await fd release", err)
	if lineText != "dropped\n" {
		t.Fatalf("unexpected child output %q", lineText)
	}
	_, err = native.Signal(t.Context(), map[string]any{"references": []any{disownedRef}, "signal": "TERM"}, tc)
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "PROCESS_NOT_OWNED" || reviewed != 1 {
		t.Fatalf("unowned process reached review or returned wrong code: err=%v reviewed=%d", err, reviewed)
	}
	allow = true
	rawSignal, err := native.Signal(t.Context(), args, tc)
	testutil.FailErr(t, "signal owned process after approval", err)
	var settled struct {
		Results []struct {
			PID       int  `json:"pid"`
			Delivered bool `json:"delivered"`
		} `json:"results"`
	}
	testutil.FailErr(t, "decode signal result", json.Unmarshal([]byte(rawSignal), &settled))
	if reviewed != 2 || len(settled.Results) != 1 || settled.Results[0].PID != child.Process.Pid || !settled.Results[0].Delivered {
		t.Fatalf("expected delivered signal to owned child, got %s (reviewed=%d)", rawSignal, reviewed)
	}
}

// startPortHolder starts /bin/sleep as the only holder of a real TCP listener;
// a non-nil lineage makes it a descendant of that session's launch.
func startPortHolder(t *testing.T, line *lineage.Lineage) (*exec.Cmd, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen", err)
	file, err := listener.(*net.TCPListener).File()
	testutil.FailErr(t, "listener file", err)
	child := exec.Command("/bin/sleep", "60")
	child.ExtraFiles = []*os.File{file}
	if line != nil {
		child.ExtraFiles = append(child.ExtraFiles, line.ChildFile())
	}
	testutil.FailErr(t, "start holder", child.Start())
	if line != nil {
		line.Started()
	}
	port := listener.Addr().(*net.TCPAddr).Port
	testutil.FailErr(t, "close parent file", file.Close())
	testutil.FailErr(t, "close parent listener", listener.Close())
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	return child, port
}

type listenerListing struct {
	Processes             []hostprocess.Process
	UnattributedListeners []hostprocess.ListeningSocket `json:"unattributed_listeners"`
	SnapshotComplete      bool                          `json:"snapshot_complete"`
}

// process_list(listening_port=N) names the process holding N after the same
// review a plain listing takes; the session's own holder keeps its reference.
func TestProcessListFindsTheHolderOfAListeningPort(t *testing.T) {
	service, err := hostprocess.New()
	testutil.FailErr(t, "create process service", err)
	line, err := lineage.Open("command:call_1", "worker")
	testutil.FailErr(t, "open lineage", err)
	t.Cleanup(func() { _ = line.Close() })
	owned, ownedPort := startPortHolder(t, line)
	foreign, foreignPort := startPortHolder(t, nil)
	reviews := 0
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "worker"},
		Execution: tools.InvocationExecution{ProcessReview: func(_ context.Context, operation string, processes []hostprocess.Process) error {
			reviews++
			if operation != "list" || len(processes) != 0 {
				t.Fatalf("unexpected review subject %s %v", operation, processes)
			}
			return nil
		}},
	}
	native := ProcessTools{Service: service}
	for _, c := range []struct {
		port  int
		child *exec.Cmd
		owned bool
	}{{ownedPort, owned, true}, {foreignPort, foreign, false}} {
		raw, err := native.List(t.Context(), map[string]any{"listening_port": float64(c.port), "protocol": "tcp"}, tc)
		testutil.FailErr(t, "list listening port", err)
		var listed listenerListing
		testutil.FailErr(t, "decode listing", json.Unmarshal([]byte(raw), &listed))
		if len(listed.Processes) != 1 || listed.UnattributedListeners == nil || len(listed.UnattributedListeners) != 0 || !listed.SnapshotComplete {
			t.Fatalf("listing for %d: %s", c.port, raw)
		}
		row := listed.Processes[0]
		want := []hostprocess.ListeningSocket{{Protocol: lineage.ProtocolTCP, Address: netip.MustParseAddr("127.0.0.1"), Port: c.port, UID: uint32(os.Getuid())}}
		if row.PID != c.child.Process.Pid || row.Name != "sleep" || !reflect.DeepEqual(row.Listeners, want) || row.Owned != c.owned || (row.Reference != "") != c.owned {
			t.Fatalf("holder of %d: %s", c.port, raw)
		}
	}
	raw, err := native.List(t.Context(), map[string]any{"listening_port": float64(foreignPort), "protocol": "udp"}, tc)
	testutil.FailErr(t, "list udp", err)
	if !strings.Contains(raw, `"processes":[]`) && !strings.Contains(raw, `"processes":null`) {
		t.Fatalf("a tcp listener answered a udp query: %s", raw)
	}
	if reviews != 3 {
		t.Fatalf("listener listings took %d reviews, want 3", reviews)
	}
}

func TestProcessListRefusesInvalidListenerFiltersBeforeReview(t *testing.T) {
	service, err := hostprocess.New()
	testutil.FailErr(t, "create process service", err)
	tc := tools.ToolContext{Execution: tools.InvocationExecution{ProcessReview: func(context.Context, string, []hostprocess.Process) error {
		t.Fatal("an invalid filter reached review")
		return nil
	}}}
	for _, args := range []map[string]any{{"listening_port": float64(0)}, {"listening_port": float64(65536)}, {"protocol": "sctp"}} {
		_, err := (&ProcessTools{Service: service}).List(t.Context(), args, tc)
		if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "PROCESS_REQUEST_INVALID" {
			t.Fatalf("filter %v: %v", args, err)
		}
	}
}
