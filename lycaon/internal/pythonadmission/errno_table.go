package pythonadmission

import (
	"time"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

// AdmissionCondition identifies a structured host admission or filesystem
// condition mapped by ErrnoOutcomeTable.
type AdmissionCondition string

const (
	// Recoverable ordinary filesystem conditions -> DecisionDeny + ordinary Errno
	// so Python raises a catchable OSError subclass.
	ConditionMissingFile         AdmissionCondition = "missing_file"
	ConditionMissingRuntimeProbe AdmissionCondition = "missing_runtime_probe"
	ConditionNameCollision       AdmissionCondition = "name_collision"
	ConditionIsDirectory         AdmissionCondition = "is_directory"
	ConditionNotDirectory        AdmissionCondition = "not_directory"
	ConditionCrossDeviceRename   AdmissionCondition = "cross_device_rename"
	ConditionInvalidArgument     AdmissionCondition = "invalid_argument"

	// Terminal host policy/resource conditions -> DecisionStop + TerminalStop
	// which latches StopLatch, seals stdout/stderr, and aborts the guest.
	ConditionReadOnlyWrite         AdmissionCondition = "read_only_write"
	ConditionOutOfScopePath        AdmissionCondition = "out_of_scope_path"
	ConditionProtectedSink         AdmissionCondition = "protected_sink"
	ConditionEffectDenied          AdmissionCondition = "effect_denied"
	ConditionApprovalDeclined      AdmissionCondition = "approval_declined"
	ConditionApprovalCancelled     AdmissionCondition = "approval_cancelled"
	ConditionHostRestart           AdmissionCondition = "host_restart"
	ConditionRevisionConflict      AdmissionCondition = "revision_conflict"
	ConditionWorkLimitExceeded     AdmissionCondition = "work_limit_exceeded"
	ConditionParkedBudgetExceeded  AdmissionCondition = "parked_budget_exceeded"
	ConditionUnsupportedSharedView AdmissionCondition = "unsupported_shared_view"
)

// OutcomeDisposition distinguishes ordinary recoverable Python OSErrors from
// terminal host stops.
type OutcomeDisposition string

const (
	DispositionRecoverableErrno OutcomeDisposition = "recoverable_errno"
	DispositionTerminalStop     OutcomeDisposition = "terminal_stop"
)

// ErrnoOutcomeEntry maps one structured AdmissionCondition to its decision
// kind, Python-visible errno (when recoverable), or typed terminal stop code.
type ErrnoOutcomeEntry struct {
	Condition       AdmissionCondition         `json:"condition"`
	Disposition     OutcomeDisposition         `json:"disposition"`
	Decision        pythonruntime.DecisionKind `json:"decision"`
	Errno           pythonruntime.Errno        `json:"errno,omitempty"`
	StopCode        pythonruntime.StopCode     `json:"stop_code,omitempty"`
	PythonException string                     `json:"python_exception,omitempty"`
	Summary         string                     `json:"summary"`
}

var defaultErrnoOutcomeEntries = []ErrnoOutcomeEntry{
	{
		Condition:       ConditionMissingFile,
		Disposition:     DispositionRecoverableErrno,
		Decision:        pythonruntime.DecisionDeny,
		Errno:           pythonruntime.ErrnoENOENT,
		PythonException: "FileNotFoundError",
		Summary:         "Ordinary missing workspace file returns catchable ENOENT",
	},
	{
		Condition:       ConditionMissingRuntimeProbe,
		Disposition:     DispositionRecoverableErrno,
		Decision:        pythonruntime.DecisionDeny,
		Errno:           pythonruntime.ErrnoENOENT,
		PythonException: "FileNotFoundError",
		Summary:         "Missing optional file in closed runtime image returns catchable ENOENT",
	},
	{
		Condition:       ConditionNameCollision,
		Disposition:     DispositionRecoverableErrno,
		Decision:        pythonruntime.DecisionDeny,
		Errno:           pythonruntime.ErrnoEEXIST,
		PythonException: "FileExistsError",
		Summary:         "Existing target name collision returns catchable EEXIST",
	},
	{
		Condition:       ConditionIsDirectory,
		Disposition:     DispositionRecoverableErrno,
		Decision:        pythonruntime.DecisionDeny,
		Errno:           pythonruntime.ErrnoEISDIR,
		PythonException: "IsADirectoryError",
		Summary:         "File operation on a directory returns catchable EISDIR",
	},
	{
		Condition:       ConditionNotDirectory,
		Disposition:     DispositionRecoverableErrno,
		Decision:        pythonruntime.DecisionDeny,
		Errno:           pythonruntime.ErrnoENOTDIR,
		PythonException: "NotADirectoryError",
		Summary:         "Directory operation on a non-directory returns catchable ENOTDIR",
	},
	{
		Condition:       ConditionCrossDeviceRename,
		Disposition:     DispositionRecoverableErrno,
		Decision:        pythonruntime.DecisionDeny,
		Errno:           pythonruntime.ErrnoEXDEV,
		PythonException: "OSError(EXDEV)",
		Summary:         "Unsupported cross-mount rename returns catchable EXDEV",
	},
	{
		Condition:       ConditionInvalidArgument,
		Disposition:     DispositionRecoverableErrno,
		Decision:        pythonruntime.DecisionDeny,
		Errno:           pythonruntime.ErrnoEINVAL,
		PythonException: "OSError(EINVAL)",
		Summary:         "Malformed ordinary argument returns catchable EINVAL",
	},
	{
		Condition:   ConditionReadOnlyWrite,
		Disposition: DispositionTerminalStop,
		Decision:    pythonruntime.DecisionStop,
		StopCode:    pythonruntime.StopEffectNotAllowed,
		Summary:     "Write attempt in read-only mode latches COMMAND_EFFECT_NOT_ALLOWED",
	},
	{
		Condition:   ConditionOutOfScopePath,
		Disposition: DispositionTerminalStop,
		Decision:    pythonruntime.DecisionStop,
		StopCode:    pythonruntime.StopEffectNotAllowed,
		Summary:     "Access outside allowed workspace scope latches COMMAND_EFFECT_NOT_ALLOWED",
	},
	{
		Condition:   ConditionProtectedSink,
		Disposition: DispositionTerminalStop,
		Decision:    pythonruntime.DecisionStop,
		StopCode:    pythonruntime.StopEffectNotAllowed,
		Summary:     "Access to protected sink or immutable runtime write latches COMMAND_EFFECT_NOT_ALLOWED",
	},
	{
		Condition:   ConditionEffectDenied,
		Disposition: DispositionTerminalStop,
		Decision:    pythonruntime.DecisionStop,
		StopCode:    pythonruntime.StopEffectNotAllowed,
		Summary:     "Policy-refused effect latches COMMAND_EFFECT_NOT_ALLOWED",
	},
	{
		Condition:   ConditionApprovalDeclined,
		Disposition: DispositionTerminalStop,
		Decision:    pythonruntime.DecisionStop,
		StopCode:    pythonruntime.StopEffectNotAllowed,
		Summary:     "Human-declined held operation latches COMMAND_EFFECT_NOT_ALLOWED",
	},
	{
		Condition:   ConditionApprovalCancelled,
		Disposition: DispositionTerminalStop,
		Decision:    pythonruntime.DecisionStop,
		StopCode:    pythonruntime.StopCancelled,
		Summary:     "Cancelled held operation latches PYTHON_CANCELLED",
	},
	{
		Condition:   ConditionHostRestart,
		Disposition: DispositionTerminalStop,
		Decision:    pythonruntime.DecisionStop,
		StopCode:    pythonruntime.StopCancelled,
		Summary:     "Host restart settles interrupted guest with PYTHON_CANCELLED",
	},
	{
		Condition:   ConditionRevisionConflict,
		Disposition: DispositionTerminalStop,
		Decision:    pythonruntime.DecisionStop,
		StopCode:    pythonruntime.StopRevisionConflict,
		Summary:     "Workspace revision conflict latches COMMAND_REVISION_CONFLICT",
	},
	{
		Condition:   ConditionWorkLimitExceeded,
		Disposition: DispositionTerminalStop,
		Decision:    pythonruntime.DecisionStop,
		StopCode:    pythonruntime.StopWorkLimitExceeded,
		Summary:     "Host work limit exhaustion latches PYTHON_WORK_LIMIT_EXCEEDED",
	},
	{
		Condition:   ConditionParkedBudgetExceeded,
		Disposition: DispositionTerminalStop,
		Decision:    pythonruntime.DecisionStop,
		StopCode:    pythonruntime.StopWorkLimitExceeded,
		Summary:     "Aggregate parked-guest budget exhaustion latches PYTHON_WORK_LIMIT_EXCEEDED",
	},
	{
		Condition:   ConditionUnsupportedSharedView,
		Disposition: DispositionTerminalStop,
		Decision:    pythonruntime.DecisionStop,
		StopCode:    pythonruntime.StopEffectNotAllowed,
		Summary:     "Subprocess requiring unpublished overlay bytes without mediated view latches COMMAND_EFFECT_NOT_ALLOWED",
	},
}

var errnoOutcomeIndex = func() map[AdmissionCondition]ErrnoOutcomeEntry {
	idx := make(map[AdmissionCondition]ErrnoOutcomeEntry, len(defaultErrnoOutcomeEntries))
	for _, entry := range defaultErrnoOutcomeEntries {
		idx[entry.Condition] = entry
	}
	return idx
}()

// DefaultErrnoOutcomeTable returns a copy of the complete errno/host-outcome
// mapping table.
func DefaultErrnoOutcomeTable() []ErrnoOutcomeEntry {
	out := make([]ErrnoOutcomeEntry, len(defaultErrnoOutcomeEntries))
	copy(out, defaultErrnoOutcomeEntries)
	return out
}

// LookupOutcome looks up the disposition for cond in the canonical table.
func LookupOutcome(cond AdmissionCondition) (ErrnoOutcomeEntry, bool) {
	entry, ok := errnoOutcomeIndex[cond]
	return entry, ok
}

// DecisionForCondition builds an OperationDecision from a structured
// AdmissionCondition. Unknown conditions fail closed to COMMAND_EFFECT_NOT_ALLOWED.
func DecisionForCondition(cond AdmissionCondition, opID, reason string, now time.Time) pythonruntime.OperationDecision {
	entry, ok := LookupOutcome(cond)
	if !ok {
		if reason == "" {
			reason = "unknown admission refusal condition: " + string(cond)
		}
		return pythonruntime.OperationDecision{
			Kind:   pythonruntime.DecisionStop,
			Reason: reason,
			Stop: &pythonruntime.TerminalStop{
				Code:        pythonruntime.StopEffectNotAllowed,
				Reason:      reason,
				OperationID: opID,
				LatchedAt:   now.UTC(),
			},
		}
	}
	if reason == "" {
		reason = entry.Summary
	}
	if entry.Disposition == DispositionRecoverableErrno {
		return pythonruntime.OperationDecision{
			Kind:   pythonruntime.DecisionDeny,
			Errno:  entry.Errno,
			Reason: reason,
		}
	}
	return pythonruntime.OperationDecision{
		Kind:   pythonruntime.DecisionStop,
		Reason: reason,
		Stop: &pythonruntime.TerminalStop{
			Code:        entry.StopCode,
			Reason:      reason,
			OperationID: opID,
			LatchedAt:   now.UTC(),
		},
	}
}
