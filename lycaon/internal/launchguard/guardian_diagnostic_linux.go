//go:build linux

package launchguard

import (
	"encoding/json"
	"io"

	"github.com/lycaon/lycaon/internal/osprocess"
	"golang.org/x/sys/unix"
)

type guardianRetirementCause string

const (
	guardianAdmissionFailed   guardianRetirementCause = "admission_failed"
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
	case guardianAdmissionFailed, guardianWaitFailed, guardianUnsettledChildren,
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
