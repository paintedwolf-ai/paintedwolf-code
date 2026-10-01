// Package toolcontract defines catalog-compiled execution facts.
package toolcontract

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/nativemanifest"
)

// BatchPolicy controls sibling calls in one assistant tool-call slice.
type BatchPolicy uint8

const (
	BatchSerial BatchPolicy = iota
	BatchShared
	BatchSameTool
)

// String returns the catalog batch value.
func (p BatchPolicy) String() string {
	switch p {
	case BatchShared:
		return nativemanifest.BatchShared
	case BatchSameTool:
		return nativemanifest.BatchSameTool
	default:
		return nativemanifest.BatchSerial
	}
}

// ParseBatchPolicy parses a catalog batch value.
func ParseBatchPolicy(value string) (BatchPolicy, bool) {
	switch value {
	case nativemanifest.BatchSerial:
		return BatchSerial, true
	case nativemanifest.BatchShared:
		return BatchShared, true
	case nativemanifest.BatchSameTool:
		return BatchSameTool, true
	default:
		return BatchSerial, false
	}
}

// TurnOrder positions control tools in an assistant batch.
type TurnOrder uint8

const (
	TurnOrderNormal TurnOrder = iota
	TurnOrderLate
	TurnOrderTerminal
)

// String returns the catalog order value.
func (o TurnOrder) String() string {
	switch o {
	case TurnOrderLate:
		return nativemanifest.TurnOrderLate
	case TurnOrderTerminal:
		return nativemanifest.TurnOrderTerminal
	default:
		return nativemanifest.TurnOrderNormal
	}
}

// ParseTurnOrder parses a catalog order value.
func ParseTurnOrder(value string) (TurnOrder, bool) {
	switch value {
	case nativemanifest.TurnOrderNormal, "":
		return TurnOrderNormal, true
	case nativemanifest.TurnOrderLate:
		return TurnOrderLate, true
	case nativemanifest.TurnOrderTerminal:
		return TurnOrderTerminal, true
	default:
		return TurnOrderNormal, false
	}
}

// Lifecycle describes the invoked subsystem's durability boundary.
type Lifecycle uint8

const (
	LifecycleReadOnly Lifecycle = iota
	LifecycleDBTransaction
	LifecycleJournaledMutation
	LifecycleDurableJob
	LifecycleEffectAttempt
	LifecycleEphemeralControl
)

// String returns the catalog lifecycle value.
func (l Lifecycle) String() string {
	switch l {
	case LifecycleDBTransaction:
		return nativemanifest.LifecycleDBTransaction
	case LifecycleJournaledMutation:
		return nativemanifest.LifecycleJournaledMutation
	case LifecycleDurableJob:
		return nativemanifest.LifecycleDurableJob
	case LifecycleEffectAttempt:
		return nativemanifest.LifecycleEffectAttempt
	case LifecycleEphemeralControl:
		return nativemanifest.LifecycleEphemeralControl
	default:
		return nativemanifest.LifecycleReadOnly
	}
}

// ParseLifecycle parses a catalog lifecycle value.
func ParseLifecycle(value string) (Lifecycle, bool) {
	switch value {
	case nativemanifest.LifecycleReadOnly:
		return LifecycleReadOnly, true
	case nativemanifest.LifecycleDBTransaction:
		return LifecycleDBTransaction, true
	case nativemanifest.LifecycleJournaledMutation:
		return LifecycleJournaledMutation, true
	case nativemanifest.LifecycleDurableJob:
		return LifecycleDurableJob, true
	case nativemanifest.LifecycleEffectAttempt:
		return LifecycleEffectAttempt, true
	case nativemanifest.LifecycleEphemeralControl:
		return LifecycleEphemeralControl, true
	default:
		return LifecycleReadOnly, false
	}
}

// Reversibility describes the recovery posture shown for an action.
type Reversibility uint8

const (
	ReversibilityReversible Reversibility = iota
	ReversibilityRecoverable
	ReversibilityIrreversible
)

// String returns the catalog reversibility value.
func (r Reversibility) String() string {
	switch r {
	case ReversibilityRecoverable:
		return nativemanifest.TierRecoverable
	case ReversibilityIrreversible:
		return nativemanifest.TierIrreversible
	default:
		return nativemanifest.TierReversible
	}
}

// ParseReversibility parses a catalog reversibility value.
func ParseReversibility(value string) (Reversibility, bool) {
	switch value {
	case nativemanifest.TierReversible:
		return ReversibilityReversible, true
	case nativemanifest.TierRecoverable:
		return ReversibilityRecoverable, true
	case nativemanifest.TierIrreversible:
		return ReversibilityIrreversible, true
	default:
		return ReversibilityReversible, false
	}
}

// EvidencePolicy names the proof retained by a settled invocation.
type EvidencePolicy string

const (
	EvidenceResult  EvidencePolicy = "result"
	EvidenceCommit  EvidencePolicy = "commit"
	EvidenceJournal EvidencePolicy = "journal"
	EvidenceJob     EvidencePolicy = "job"
	EvidenceAttempt EvidencePolicy = "attempt"
	EvidenceControl EvidencePolicy = "control"
)

// RecoveryPolicy names how unfinished work is reconciled after restart.
type RecoveryPolicy string

const (
	RecoveryNone    RecoveryPolicy = "none"
	RecoveryOwner   RecoveryPolicy = "owner"
	RecoveryJournal RecoveryPolicy = "journal"
	RecoveryResume  RecoveryPolicy = "resume"
	RecoveryAbandon RecoveryPolicy = "abandon"
)

// Capability is a generated execution surface.
type Capability uint32

const (
	CapabilityHostResource Capability = 1 << iota
	CapabilitySocket
	CapabilityDirectIP
	CapabilityLocalListen
	CapabilityLoopbackConnect
	CapabilityTerminalCapture
	CapabilityWriteRoot
	CapabilityReadPath
	CapabilityPackageExecution
	CapabilityHeldTerminalInput
	CapabilityFileChange
	CapabilityProcessControl
	CapabilityHostExecution
)

// NamedCapability maps a catalog execution_capabilities token to its bit.
func NamedCapability(name string) (Capability, bool) {
	switch name {
	case "host_resource":
		return CapabilityHostResource, true
	case "socket":
		return CapabilitySocket, true
	case "process_control":
		return CapabilityProcessControl, true
	case "host_execution":
		return CapabilityHostExecution, true
	case "direct_ip":
		return CapabilityDirectIP, true
	case "local_listen":
		return CapabilityLocalListen, true
	case "loopback_connect":
		return CapabilityLoopbackConnect, true
	case "terminal_capture":
		return CapabilityTerminalCapture, true
	case "write_root":
		return CapabilityWriteRoot, true
	case "read_path":
		return CapabilityReadPath, true
	case "package_execution":
		return CapabilityPackageExecution, true
	case "file_change":
		return CapabilityFileChange, true
	case "held_terminal_input":
		return CapabilityHeldTerminalInput, true
	default:
		return 0, false
	}
}

// capabilityRequestFields is the closed capability_request object key set.
var capabilityRequestFields = []string{
	"host_resources", "socket_paths", "process_control", "host_execution", "direct_ip", "local_listen", "loopback_connect", "write_root", "read_path",
}

// CapabilityRequestFields returns the structured capability_request keys.
func CapabilityRequestFields() []string {
	return append([]string(nil), capabilityRequestFields...)
}

// IsCapabilityRequestField reports whether name is a capability_request key.
func IsCapabilityRequestField(name string) bool {
	for _, field := range capabilityRequestFields {
		if field == name {
			return true
		}
	}
	return false
}

// Contract is the compiled runtime projection for one tool.
type Contract struct {
	Owner                 string
	Reversibility         Reversibility
	Batch                 BatchPolicy
	BatchOptInArg         string
	BatchConcurrencyLimit int
	BatchSerialWhenArg    string
	Order                 TurnOrder
	Lifecycle             Lifecycle
	Capabilities          Capability
	// ChunkablePayload allows argument payloads to be split across calls.
	ChunkablePayload bool
	// SurveyNeutral preserves the read-only streak for bookkeeping calls.
	SurveyNeutral bool
	// DetachAfterBudget lets a call outlive its foreground wait as a held call.
	DetachAfterBudget bool
	// SessionScope is the session shape this tool belongs to. Empty admits both.
	SessionScope SessionScope
	// SocketArg names the argument whose AF_UNIX socket the tool dials itself;
	// the executor reviews it as exact socket authority.
	SocketArg string
	// BoundedInProcess marks tools whose execution runs synchronously in-process.
	BoundedInProcess bool
	// SecretReferenceSurface is the outbound screen for resolved managed-secret
	// references. Empty keeps references as literal text.
	SecretReferenceSurface SecretSurface
	// SecretReferenceArgs names the value slots resolved when SecretReferenceSurface
	// is SecretSurfaceFile. Empty resolves across the entire argument tree.
	SecretReferenceArgs []string
}

// SessionScope restricts tool availability within profiles shared across session types.
type SessionScope string

const (
	// SessionScopeAny admits either shape.
	SessionScopeAny SessionScope = ""
	// SessionScopeWorkerChild requires a spawned worker session.
	SessionScopeWorkerChild SessionScope = "worker_child"
	// SessionScopeAddressed requires a user-addressed session.
	SessionScopeAddressed SessionScope = "addressed_session"
)

// AdmitsSession reports whether this contract runs in a session of the given shape.
func (c Contract) AdmitsSession(workerChild bool) bool {
	switch c.SessionScope {
	case SessionScopeWorkerChild:
		return workerChild
	case SessionScopeAddressed:
		return !workerChild
	default:
		return true
	}
}

// AdmitsSession reports whether the named tool runs in a session of the given
// shape. An undeclared tool admits both.
func AdmitsSession(name string, workerChild bool) bool {
	contract, ok := Lookup(name)
	return !ok || contract.AdmitsSession(workerChild)
}

// Supports reports whether this tool contract declares capability.
func (c Contract) Supports(capability Capability) bool {
	return c.Capabilities&capability != 0
}

// CapabilityRequestFields returns only the request fields this execution contract supports.
func (c Contract) CapabilityRequestFields() []string {
	var fields []string
	for _, entry := range []struct {
		field      string
		capability Capability
	}{
		{"host_resources", CapabilityHostResource},
		{"socket_paths", CapabilitySocket},
		{"direct_ip", CapabilityDirectIP},
		{"process_control", CapabilityProcessControl},
		{"host_execution", CapabilityHostExecution},
		{"local_listen", CapabilityLocalListen},
		{"loopback_connect", CapabilityLoopbackConnect},
		{"write_root", CapabilityWriteRoot},
		{"read_path", CapabilityReadPath},
	} {
		if c.Supports(entry.capability) {
			fields = append(fields, entry.field)
		}
	}
	return fields
}

// MutatesWorld reports whether a call leaves state for later calls.
func (c Contract) MutatesWorld() bool {
	switch c.Lifecycle {
	case LifecycleDBTransaction, LifecycleJournaledMutation, LifecycleDurableJob, LifecycleEffectAttempt:
		return true
	default:
		return false
	}
}

// Lookup returns the compiled contract when name is declared.
func Lookup(name string) (Contract, bool) {
	contract, ok := compiledContracts[name]
	return contract, ok
}

// Concurrent reports whether the contract admits sibling execution.
func (c Contract) Concurrent() bool {
	return c.Batch != BatchSerial
}

// ConcurrentFor reports whether one call enables sibling execution.
func (c Contract) ConcurrentFor(args map[string]any) bool {
	if !c.Concurrent() {
		return false
	}
	if c.BatchOptInArg == "" {
		return c.batchArgsAdmitConcurrency(args)
	}
	enabled, _ := args[c.BatchOptInArg].(bool)
	return enabled && c.batchArgsAdmitConcurrency(args)
}

func (c Contract) batchArgsAdmitConcurrency(args map[string]any) bool {
	if c.BatchSerialWhenArg == "" {
		return true
	}
	_, present := args[c.BatchSerialWhenArg]
	return !present
}

// Evidence returns the proof obligation implied by the subsystem-owner boundary.
func (c Contract) Evidence() EvidencePolicy {
	switch c.Lifecycle {
	case LifecycleDBTransaction:
		return EvidenceCommit
	case LifecycleJournaledMutation:
		return EvidenceJournal
	case LifecycleDurableJob:
		return EvidenceJob
	case LifecycleEffectAttempt:
		return EvidenceAttempt
	case LifecycleEphemeralControl:
		return EvidenceControl
	default:
		return EvidenceResult
	}
}

// Recovery returns the unfinished-work policy implied by the subsystem-owner boundary.
func (c Contract) Recovery() RecoveryPolicy {
	switch c.Lifecycle {
	case LifecycleDBTransaction:
		return RecoveryOwner
	case LifecycleJournaledMutation:
		return RecoveryJournal
	case LifecycleDurableJob:
		return RecoveryResume
	case LifecycleEffectAttempt:
		return RecoveryAbandon
	case LifecycleEphemeralControl:
		return RecoveryAbandon
	default:
		return RecoveryNone
	}
}

// Digest binds invocation receipts to execution and recovery policy.
func (c Contract) Digest() string {
	fields := []string{
		"invocation-contract-v3", strings.TrimSpace(c.Owner),
		strconv.Itoa(int(c.Reversibility)), strconv.Itoa(int(c.Batch)),
		strings.TrimSpace(c.BatchOptInArg), strconv.Itoa(c.BatchConcurrencyLimit),
		strings.TrimSpace(c.BatchSerialWhenArg),
		strconv.Itoa(int(c.Order)), strconv.Itoa(int(c.Lifecycle)),
		string(c.Evidence()), string(c.Recovery()),
		strconv.FormatUint(uint64(c.Capabilities), 10),
		strconv.FormatBool(c.DetachAfterBudget),
	}
	if c.SocketArg != "" {
		fields = append(fields, "socket_arg:"+c.SocketArg)
	}
	if c.BoundedInProcess {
		fields = append(fields, "bounded_in_process:true")
	}
	if c.SecretReferenceSurface != SecretSurfaceNone {
		fields = append(fields, "secret_reference_surface:"+string(c.SecretReferenceSurface))
	}
	if len(c.SecretReferenceArgs) > 0 {
		fields = append(fields, "secret_reference_args:"+strings.Join(c.SecretReferenceArgs, ","))
	}
	sum := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	return hex.EncodeToString(sum[:])
}

// External declares a dynamic tool and its external subsystem owner.
// Durability remains a host claim.
func External(owner string) Contract {
	return Contract{
		Owner: strings.TrimSpace(owner), Batch: BatchSerial,
		Reversibility: ReversibilityRecoverable, Lifecycle: LifecycleEffectAttempt,
	}
}

// BatchGroupableCalls reports whether two calls may execute together.
func BatchGroupableCalls(aName string, aArgs map[string]any, a Contract, bName string, bArgs map[string]any, b Contract) bool {
	if aName == "" || bName == "" {
		return false
	}
	if !a.ConcurrentFor(aArgs) || !b.ConcurrentFor(bArgs) {
		return false
	}
	if a.Batch == BatchSameTool || b.Batch == BatchSameTool {
		return aName == bName && a.Batch == BatchSameTool && b.Batch == BatchSameTool
	}
	return a.Batch == BatchShared && b.Batch == BatchShared
}
