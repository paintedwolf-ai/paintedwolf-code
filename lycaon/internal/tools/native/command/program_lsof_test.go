//go:build (darwin && cgo) || linux

package command_test

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hostprocess"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// lsofReview answers the host process inspection review and counts it.
type lsofReview struct {
	calls int
	err   error
}

func (r *lsofReview) install(f *programFixture) {
	f.Configure = func(tc *tools.ToolContext) {
		tc.Execution.ProcessReview = func(_ context.Context, operation string, processes []hostprocess.Process) error {
			r.calls++
			if operation != "list" || len(processes) != 0 {
				return fmt.Errorf("unexpected review %s %v", operation, processes)
			}
			return r.err
		}
	}
}

// A listener this test process holds is outside the program's session. After
// approval lsof names it with its PID and command; without approval it lists
// no host row and says the holders were not inspected; and kill still refuses
// the PID lsof printed.
func TestProgramEntryLsofNamesHostListenersOnlyAfterApproval(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen", err)
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	self, err := hostprocess.Observe(os.Getpid())
	testutil.FailErr(t, "observe test process", err)
	f := newProgramFixture(t)
	review := &lsofReview{}
	review.install(f)

	out, err := f.call(t, t.Context(), fmt.Sprintf("lsof -nP -iTCP:%d -sTCP:LISTEN", port))
	testutil.FailErr(t, "approved lsof", err)
	outcome := decodeOutcome(t, out)
	lines := strings.Split(strings.TrimSuffix(outcome.Stdout, "\n"), "\n")
	want := []string{self.Name, strconv.Itoa(os.Getpid()), strconv.Itoa(os.Getuid()), "IPv4", "TCP", fmt.Sprintf("127.0.0.1:%d", port), "(LISTEN)"}
	if outcome.Status != 0 || outcome.Stderr != "" || len(lines) != 2 || strings.Join(strings.Fields(lines[0]), " ") != "COMMAND PID USER TYPE NAME" || strings.Join(strings.Fields(lines[1]), " ") != strings.Join(want, " ") {
		t.Fatalf("approved lsof %s", out)
	}
	out, err = f.call(t, t.Context(), fmt.Sprintf("lsof -t -i :%d", port))
	testutil.FailErr(t, "terse lsof", err)
	if outcome := decodeOutcome(t, out); outcome.Stdout != strconv.Itoa(os.Getpid())+"\n" || outcome.Status != 0 {
		t.Fatalf("terse lsof %s", out)
	}
	if review.calls != 2 {
		t.Fatalf("approved listings took %d reviews, want 2", review.calls)
	}

	out, err = f.call(t, t.Context(), fmt.Sprintf("kill %d", os.Getpid()))
	testutil.FailErr(t, "kill host listener", err)
	if outcome := decodeOutcome(t, out); outcome.Refusal == nil || outcome.Refusal.Code != "PROCESS_NOT_OWNED" {
		t.Fatalf("kill of a host listener %s", out)
	}

	review.err = &toolrejection.ToolReject{Code: isolation.CodeExecutionCapabilityDenied}
	out, err = f.call(t, t.Context(), fmt.Sprintf("lsof -i :%d", port))
	testutil.FailErr(t, "denied lsof", err)
	outcome = decodeOutcome(t, out)
	if outcome.Status != 1 || outcome.Stdout != "" || outcome.Refusal != nil ||
		outcome.Stderr != fmt.Sprintf("lsof: port %d is held outside this session; host process inspection was not approved, so those holders were not inspected\n", port) {
		t.Fatalf("denied lsof %s", out)
	}
	f.Configure = nil
	out, err = f.call(t, t.Context(), fmt.Sprintf("lsof -i :%d", port))
	testutil.FailErr(t, "lsof without a reviewer", err)
	if outcome := decodeOutcome(t, out); outcome.Status != 1 || outcome.Stdout != "" || !strings.Contains(outcome.Stderr, "host process inspection is unavailable here, so those holders were not inspected") {
		t.Fatalf("lsof without a reviewer %s", out)
	}
	if review.calls != 3 {
		t.Fatalf("listings took %d reviews, want 3", review.calls)
	}
}

// fixedPorts answers lsof from a fixed holder table.
type fixedPorts struct {
	holders []hostprocess.PortHolder
	queries []lineage.ListenerQuery
}

func (p *fixedPorts) PortHolders(_ context.Context, _ string, query lineage.ListenerQuery) ([]hostprocess.PortHolder, error) {
	p.queries = append(p.queries, query)
	var out []hostprocess.PortHolder
	for _, holder := range p.holders {
		if query.Port == 0 || holder.Socket.Port == int(query.Port) {
			out = append(out, holder)
		}
	}
	return out, nil
}

// A session job's listener, held by the job's process or by a process it
// started, keeps its job identity whether or not host inspection is approved;
// a holder the host could not read shows its port with the PID unknown.
func TestProgramEntryLsofKeepsJobIdentityAndUnknownHolders(t *testing.T) {
	f := newProgramFixture(t)
	_, err := f.call(t, t.Context(), "external -- /bin/sleep 30 &")
	testutil.FailErr(t, "launch session job", err)
	jobPID, err := strconv.Atoi(f.jobHandle(t))
	testutil.FailErr(t, "job pid", err)
	job, err := hostprocess.Observe(jobPID)
	testutil.FailErr(t, "observe job", err)
	server := hostprocess.Process{PID: 1 << 30, ParentPID: jobPID, UID: job.UID, Name: "node", Owned: true}
	job.Owned = true
	loopback, any6 := netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("::")
	ports := &fixedPorts{holders: []hostprocess.PortHolder{
		{Socket: hostprocess.ListeningSocket{Protocol: lineage.ProtocolTCP, Address: loopback, Port: 3000, UID: job.UID}, Process: &job},
		{Socket: hostprocess.ListeningSocket{Protocol: lineage.ProtocolTCP, Address: any6, Port: 3000, UID: job.UID}, Process: &server},
		{Socket: hostprocess.ListeningSocket{Protocol: lineage.ProtocolTCP, Address: netip.IPv4Unspecified(), Port: 3000, UID: 0}},
		{Socket: hostprocess.ListeningSocket{Protocol: lineage.ProtocolUDP, Address: any6, Port: 5353, UID: 0}},
	}}
	f.Runtime.Ports = ports
	review := &lsofReview{err: &toolrejection.ToolReject{Code: isolation.CodeExecutionCapabilityDenied}}
	review.install(f)

	jobRows := []string{
		fmt.Sprintf("%s %d %d IPv4 TCP 127.0.0.1:3000 (LISTEN) [session job #1 %d]", job.Name, jobPID, job.UID, jobPID),
		fmt.Sprintf("node %d %d IPv6 TCP *:3000 (LISTEN) [session job #1 %d]", 1<<30, job.UID, jobPID),
	}
	out, err := f.call(t, t.Context(), "lsof -i :3000")
	testutil.FailErr(t, "unapproved lsof", err)
	outcome := decodeOutcome(t, out)
	if got := normalizedRows(outcome.Stdout); outcome.Status != 0 || got != strings.Join(append([]string{"COMMAND PID USER TYPE NAME"}, jobRows...), "\n") ||
		!strings.Contains(outcome.Stderr, "port 3000 is held outside this session") {
		t.Fatalf("unapproved lsof %s", out)
	}

	review.err = nil
	out, err = f.call(t, t.Context(), "lsof -i :3000")
	testutil.FailErr(t, "approved lsof", err)
	outcome = decodeOutcome(t, out)
	want := append([]string{"COMMAND PID USER TYPE NAME"}, jobRows...)
	want = append(want, "? ? 0 IPv4 TCP *:3000 (LISTEN)")
	if got := normalizedRows(outcome.Stdout); outcome.Status != 0 || outcome.Stderr != "" || got != strings.Join(want, "\n") {
		t.Fatalf("approved lsof %s", out)
	}
	out, err = f.call(t, t.Context(), "lsof -iudp:5353")
	testutil.FailErr(t, "udp lsof", err)
	if got := normalizedRows(decodeOutcome(t, out).Stdout); got != "COMMAND PID USER TYPE NAME\n? ? 0 IPv6 UDP *:5353" {
		t.Fatalf("udp lsof %s", out)
	}
	out, err = f.call(t, t.Context(), "lsof -ti:5353")
	testutil.FailErr(t, "terse unknown holder", err)
	if outcome := decodeOutcome(t, out); outcome.Stdout != "" || outcome.Status != 1 {
		t.Fatalf("terse lsof printed an unknown PID %s", out)
	}
	if review.calls != 4 {
		t.Fatalf("lsof took %d reviews, want 4", review.calls)
	}

	out, err = f.call(t, t.Context(), "lsof")
	testutil.FailErr(t, "bare lsof", err)
	outcome = decodeOutcome(t, out)
	if got := normalizedRows(outcome.Stdout); got != strings.Join(append([]string{"COMMAND PID USER TYPE NAME"}, jobRows...), "\n") ||
		outcome.Stderr != "lsof: only this session's listeners are listed; name a port with -i :PORT to include other processes\n" || review.calls != 4 {
		t.Fatalf("bare lsof %s", out)
	}
	wantQueries := []lineage.ListenerQuery{
		{Port: 3000}, {Port: 3000}, {Protocols: []lineage.Protocol{lineage.ProtocolUDP}, Port: 5353},
		{Port: 5353}, {},
	}
	if fmt.Sprint(ports.queries) != fmt.Sprint(wantQueries) {
		t.Fatalf("listener queries %v, want %v", ports.queries, wantQueries)
	}
}

func TestProgramEntryLsofRefusesUnsupportedEndpoints(t *testing.T) {
	f := newProgramFixture(t)
	f.Runtime.Ports = &fixedPorts{}
	for _, program := range []string{"lsof -i 8080", "lsof -iTCP", "lsof -iSCTP:80", "lsof -i :0", "lsof -i :65536", "lsof -i :80 -sTCP:ESTABLISHED", "lsof -i :80 extra"} {
		out, err := f.call(t, t.Context(), program)
		testutil.FailErr(t, program, err)
		if refusal := decodeOutcome(t, out).Refusal; refusal == nil || refusal.Code != "COMMAND_OPERAND_INVALID" {
			t.Fatalf("%s: %s", program, out)
		}
	}
	f.Runtime.Ports = nil
	out, err := f.call(t, t.Context(), "lsof -i :80")
	if err == nil && decodeOutcome(t, out).Status == 0 {
		t.Fatalf("lsof without an inventory succeeded: %s", out)
	}
}

// normalizedRows collapses lsof's column padding so rows compare by field.
func normalizedRows(stdout string) string {
	var rows []string
	for _, line := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
		rows = append(rows, strings.Join(strings.Fields(line), " "))
	}
	return strings.Join(rows, "\n")
}
