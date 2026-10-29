//go:build linux

package launchguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/lycaon/lycaon/internal/osprocess"
	"golang.org/x/sys/unix"
)

type guardianRetirementCause string

const (
	guardianWaitFailed        guardianRetirementCause = "wait_failed"
	guardianUnsettledChildren guardianRetirementCause = "no_children_unsettled"
	guardianSignalReceived    guardianRetirementCause = "signal_received"
	guardianRootExited        guardianRetirementCause = "registered_role_exited"
	guardianDesktopReady      guardianRetirementCause = "desktop_pidfd_ready"
	guardianRoleReady         guardianRetirementCause = "helper_pidfd_ready"
	guardianHealthEnded       guardianRetirementCause = "health_channel_ended"
	guardianHealthRejected    guardianRetirementCause = "health_frame_rejected"
	guardianHealthExpired     guardianRetirementCause = "health_deadline_expired"
	guardianPollFailed        guardianRetirementCause = "poll_failed"
)

type guardianObservation struct {
	Cause     guardianRetirementCause `json:"cause"`
	RoleIndex int                     `json:"role_index"`
	Poll      [4]int16                `json:"poll"`
	Frame     *guardianHealthFact     `json:"frame,omitempty"`
	Exit      *guardianExitFact       `json:"exit,omitempty"`
}

type guardianHealthFact struct {
	Kind       byte   `json:"kind"`
	Sequence   uint64 `json:"sequence"`
	Descriptor bool   `json:"descriptor"`
}

type guardianDiagnosticProcess struct {
	Role  string `json:"role"`
	PID   int    `json:"pid"`
	Start int64  `json:"start"`
}

type guardianRetirement struct {
	Version     int                          `json:"version"`
	Cause       guardianRetirementCause      `json:"cause"`
	MonotonicNS int64                        `json:"monotonic_ns"`
	Guardian    guardianDiagnosticProcess    `json:"guardian"`
	Roles       [3]guardianDiagnosticProcess `json:"roles"`
	RoleIndex   int                          `json:"role_index"`
	WaitStatus  *uint32                      `json:"wait_status,omitempty"`
	Signal      int                          `json:"signal,omitempty"`
	Observation *guardianObservation         `json:"observation,omitempty"`
}

// Birth facts are diagnostic only. Missing metadata never changes admission,
// retirement, signaling, waiting or generation custody.
func (state *protectedGuardian) captureDiagnosticBirths() [4]int64 {
	var births [4]int64
	if !state.registeredAlive() {
		return births
	}
	pids := [4]int{state.roots.stage, state.roots.pids[0], state.roots.pids[1], state.roots.pids[2]}
	for index, pid := range pids {
		start, ok := osprocess.StartTime(pid)
		if !ok || start <= 0 {
			return [4]int64{}
		}
		births[index] = start
	}
	if !state.registeredAlive() {
		return [4]int64{}
	}
	return births
}

// Call immediately before the existing first retirement assignment. The
// diagnostic cannot select a role, authorize cleanup or alter its outcome.
func (state *protectedGuardian) writeRetirement(writer io.Writer, event guardianRetirement) {
	if writer == nil || state.retired || event.RoleIndex < -1 || event.RoleIndex > 2 {
		return
	}
	switch event.Cause {
	case guardianWaitFailed, guardianUnsettledChildren,
		guardianSignalReceived, guardianRootExited, guardianDesktopReady,
		guardianRoleReady, guardianHealthEnded, guardianHealthRejected,
		guardianHealthExpired, guardianPollFailed:
	default:
		return
	}
	for _, birth := range state.diagnosticBirths {
		if birth <= 0 {
			return
		}
	}
	var stamp unix.Timespec
	if unix.ClockGettime(unix.CLOCK_MONOTONIC, &stamp) != nil || stamp.Nano() <= 0 {
		return
	}
	event.Version, event.MonotonicNS = 1, stamp.Nano()
	event.Guardian = guardianDiagnosticProcess{"guardian", state.roots.stage, state.diagnosticBirths[0]}
	for index, role := range [3]string{"desktop", "mount-watcher", "mount-intake"} {
		event.Roles[index] = guardianDiagnosticProcess{role, state.roots.pids[index], state.diagnosticBirths[index+1]}
	}
	encoded, err := json.Marshal(event)
	if err != nil || len(encoded) > 1023 {
		return
	}
	_, _ = writer.Write(append(encoded, '\n'))
}

// Reopening proc-fd creates a separate file description: nonblocking and append
// flags cannot change another process's inherited stderr. Raw write avoids the
// os package's special SIGPIPE exit policy for standard streams.
type guardianDiagnosticStderr struct{}

func (guardianDiagnosticStderr) Write(data []byte) (int, error) {
	var before, opened, after unix.Stat_t
	if err := unix.Fstat(2, &before); err != nil {
		return 0, err
	}
	fd, err := unix.Open("/proc/self/fd/2", unix.O_WRONLY|unix.O_APPEND|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Fstat(fd, &opened); err != nil {
		return 0, err
	}
	if err := unix.Fstat(2, &after); err != nil {
		return 0, err
	}
	same := func(left, right unix.Stat_t) bool {
		return left.Dev == right.Dev && left.Ino == right.Ino && left.Mode == right.Mode && left.Rdev == right.Rdev
	}
	if !same(before, opened) || !same(opened, after) {
		return 0, unix.ESTALE
	}
	count, err := unix.Write(fd, data)
	if count < 0 {
		count = 0
	}
	return count, err
}

const guardianDiagnosticMagic = "PWGDIAG1"
const guardianDiagnosticLimit = 1024

type guardianReport struct {
	notice     guardianNotice
	diagnostic []byte
}

type guardianDiagnosticControl struct{ fd int }

func (sink guardianDiagnosticControl) Write(raw []byte) (int, error) {
	if len(raw) == 0 || len(raw) > guardianDiagnosticLimit {
		return 0, unix.EMSGSIZE
	}
	packet := append([]byte(guardianDiagnosticMagic), raw...)
	n, err := unix.SendmsgN(sink.fd, packet, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
	if err != nil {
		return 0, err
	}
	if n != len(packet) {
		return 0, io.ErrShortWrite
	}
	return len(raw), nil
}

func (state *protectedGuardian) reportRetirement(event guardianRetirement) {
	if state.control != nil {
		if event.Observation != nil && event.RoleIndex >= 0 && event.RoleIndex < len(state.roots.pids) {
			observation := *event.Observation
			observation.Exit = observeGuardianExit(15+event.RoleIndex, state.roots.pids[event.RoleIndex], state.caller.UID)
			event.Observation = &observation
		}
		state.writeRetirement(guardianDiagnosticControl{int(state.control.Fd())}, event)
	}
}

// Only the post-admission supervisor accepts diagnostics. This variant never
// supplies readiness, failure, settlement or an exit status.
func receiveGuardianReport(fd, pid int, caller Identity) (guardianReport, error) {
	raw, err := receiveGuardianPacket(fd, pid, caller, len(guardianDiagnosticMagic)+guardianDiagnosticLimit)
	if err != nil {
		return guardianReport{}, err
	}
	if !bytes.HasPrefix(raw, []byte(guardianDiagnosticMagic)) {
		notice, err := decodeGuardianNotice(raw)
		return guardianReport{notice: notice}, err
	}
	raw = raw[len(guardianDiagnosticMagic):]
	var event guardianRetirement
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return guardianReport{}, err
	}
	canonical, err := json.Marshal(event)
	if err != nil || !bytes.Equal(raw, append(canonical, '\n')) || !validGuardianRetirement(event, pid) {
		return guardianReport{}, errors.New("invalid guardian retirement diagnostic")
	}
	return guardianReport{diagnostic: raw}, nil
}

func validGuardianRetirement(event guardianRetirement, pid int) bool {
	if event.Version != 1 || event.MonotonicNS <= 0 || event.Guardian.Role != "guardian" || event.Guardian.PID != pid || event.Guardian.Start <= 0 || event.RoleIndex < -1 || event.RoleIndex > 2 {
		return false
	}
	seen := map[int]bool{pid: true}
	for index, name := range [3]string{"desktop", "mount-watcher", "mount-intake"} {
		role := event.Roles[index]
		if role.Role != name || role.PID <= 1 || role.Start <= 0 || seen[role.PID] {
			return false
		}
		seen[role.PID] = true
	}
	switch event.Cause {
	case guardianRootExited:
		return event.RoleIndex >= 0 && event.WaitStatus != nil && event.Signal == 0 && event.Observation == nil
	case guardianSignalReceived:
		return event.RoleIndex == -1 && event.WaitStatus == nil && (event.Signal == int(unix.SIGTERM) || event.Signal == int(unix.SIGINT) || event.Signal == int(unix.SIGHUP)) && event.Observation == nil
	case guardianWaitFailed, guardianUnsettledChildren:
		return event.RoleIndex == -1 && event.WaitStatus == nil && event.Signal == 0 && event.Observation == nil
	case guardianDesktopReady, guardianRoleReady, guardianHealthEnded, guardianHealthRejected, guardianHealthExpired, guardianPollFailed:
		if event.WaitStatus != nil || event.Signal != 0 || event.Observation == nil || event.Observation.Cause != event.Cause || event.Observation.RoleIndex != event.RoleIndex {
			return false
		}
		if event.Observation.Exit != nil {
			fact := event.Observation.Exit
			status, ok := guardianExitStatus(fact.Code, fact.Status)
			if event.RoleIndex < 0 || !ok || fact.WaitStatus != status {
				return false
			}
		}
		if event.Cause == guardianDesktopReady {
			return event.RoleIndex == 0
		}
		if event.Cause == guardianRoleReady {
			return event.RoleIndex > 0
		}
		return event.RoleIndex == -1
	default:
		return false
	}
}

// si_code and si_status are retained directly. Linux encodes only these exited
// child variants as the corresponding Wait4 status; other codes stay unknown.
type guardianExitFact struct {
	Code       int32  `json:"code"`
	Status     int32  `json:"status"`
	WaitStatus uint32 `json:"wait_status"`
}

func guardianExitStatus(code, status int32) (uint32, bool) {
	switch code {
	case 1: // CLD_EXITED
		if status >= 0 && status <= 255 {
			return uint32(status) << 8, true
		}
	case 2, 3: // CLD_KILLED, CLD_DUMPED
		if status > 0 && status <= 64 {
			raw := uint32(status)
			if code == 3 {
				raw |= 0x80
			}
			return raw, true
		}
	}
	return 0, false
}
