package hitl

import "strconv"

// Frozen approval-option titles. Duration that bounds a grant is in the title.
const (
	TitleAllowOnce           = "Allow once"
	TitleAllowFor1Day        = "Allow for 1 day"
	TitleAllowForThisChat    = "Allow for this chat"
	TitleAllowForThisProject = "Allow for this project for 7 days"
	TitleAllowOnThisDevice   = "Allow on this device for 30 days"
	TitleSendRedacted        = "Send redacted"
	// TitleSend releases a value the host already protects.
	TitleSend          = "Send"
	TitleSendUnchanged = "Send unchanged"
	// Protect vaults the value; the send still proceeds.
	TitleProtect = "Protect"
	// Release titles state that the original value is sent.
	TitleSendUnchangedFor1Day        = "Send unchanged for 1 day"
	TitleSendUnchangedForThisChat    = "Send unchanged for this chat"
	TitleSendUnchangedForThisProject = "Send unchanged for this project for 7 days"
	// A managed value is already protected, so its rungs drop "unchanged".
	TitleSendFor1Day        = "Send for 1 day"
	TitleSendForThisChat    = "Send for this chat"
	TitleSendForThisProject = "Send for this project for 7 days"
	// Standing redaction can span the device because it reduces disclosure.
	TitleKeepRedactingThisDevice = "Keep redacting on this device for 30 days"
	// Duration; group is the verb, coverage the subject.
	TitleQuietForThisChat = "For this chat"
	// Posture controls whether this broader subject is primary or a menu choice.
	TitleAllowCommandNetworkForThisChat = "Allow this command's network for this chat"
)

// Provider trust uses the same authority as Settings.
func TitleTrustProvider(label string) string {
	return "Trust " + label + " with credentials on this device"
}

// Disabled options retain their positions and display a reason.
const (
	NoteNoProjectOpen = "Not available: no project is open to attach a longer approval to"
	NoteEndsWithChat  = "Not available: this authority lasts only for this chat"
)

// Frozen expires_when copy.
const (
	ExpiresAfterThisAction          = "after this action"
	ExpiresIn1DayOrChatDeleted      = "in 1 day or when this chat is deleted"
	ExpiresIn1DayOrRevoked          = "in 1 day or when revoked"
	ExpiresWhenChatDeleted          = "when this chat is deleted"
	ExpiresIn7DaysOrRevoked         = "in 7 days or when revoked"
	ExpiresIn30DaysOrRevoked        = "in 30 days or when revoked"
	ExpiresWhenChatDeletedOrRevoked = "when this chat is deleted or you revoke it"
	ExpiresWhenUntrusted            = "when you untrust the provider in Settings"
	ReaskWhenActionRunsAgain        = "the action runs again"
	ReaskWhenQuietRevoked           = "Revoke it in Saved approvals, or start a new chat"
	CoverageOnlyThisExactAction     = "only this exact action"
	CoverageOnlyExactActionArgs     = "only this exact action and arguments, apart from time limits and output framing"
	CoverageOnlyEnumeratedActions   = "only the enumerated actions shown here"
	ReaskWhenDifferentPath          = "a different path, direction, or project"
	ReaskWhenOutsideFolder          = "a path outside this folder, a write, or a different project"
	ReaskWhenDifferentSiteOrPort    = "a different site, port, project, or confinement"
	ReaskWhenDifferentCommand       = "a different command, chat, or confinement"
	ReaskWhenDifferentProvider      = "a different provider or destination"
	// Secret-card send coverage.
	CoverageEveryCredentialReplaced = "this request with every detected credential replaced"
	CoverageProtectedAndSent        = "this request sent with each detected credential held in protected storage and replaced by a reference"
	CoverageRedactionUnavailable    = "not available — this send cannot be rewritten"
	// CoverageRedactionBreaks is the cost where the value authenticates the call.
	CoverageRedactionBreaks = "strips the value — this request will likely fail without it"
)

// NoteStandingRedactionHeld explains an unusable redaction grant.
const NoteStandingRedactionHeld = "Your keep-redacting approval covers this credential, but it cannot be applied to this send."

// NoteContestedRedaction explains why the redacted option is inert on a
// contested card.
const NoteContestedRedaction = "A redacted send of this request already completed and did not serve."

// QuietLabelThisExactAction is the quiet subject shown for the three gates whose
// quiet is keyed to the byte-identical action rather than to a class.
const QuietLabelThisExactAction = "this exact action"

// CoverageReadsOf is exact-path read coverage.
func CoverageReadsOf(path string) string { return "reads of `" + path + "`" }

// CoverageReadsOfTree is folder-and-descendants read coverage.
func CoverageReadsOfTree(path string) string {
	return "reads of `" + path + "` and files under it"
}

// CoverageWritesTo is exact-path write coverage.
func CoverageWritesTo(path string) string { return "writes to `" + path + "`" }

// CoverageCommandNetwork is the mediated-network coverage of one command for
// the chat: every host it reaches is still observed and recorded.
func CoverageCommandNetwork(command string) string {
	return "every host `" + command + "` reaches through network mediation, for this chat"
}

// CoverageTrustProvider is the device-wide provider trust coverage.
func CoverageTrustProvider(label string) string {
	return "any detected credential sent to " + label + " from this device"
}

func QuietSubjectSummary(labels []string) string {
	switch len(labels) {
	case 0:
		return QuietLabelThisExactAction
	case 1:
		return labels[0]
	case 2:
		return labels[0] + " and " + labels[1]
	default:
		return "these " + strconv.Itoa(len(labels)) + " reasons"
	}
}

// DeviceCoverageSuffix marks a duration-only device rung as project-bound.
const DeviceCoverageSuffix = ", in this project"

// Secret-specific groups follow the fixed primary and quiet slots.
const (
	GroupAlsoAllow     = "Also allow"
	GroupHostResources = "Host resources"
	GroupQuiet         = "Allow and stop asking"
	// Redaction includes current-send and standing choices.
	GroupRedaction = "Redaction"
	// Provider trust applies across the device.
	GroupTrust = "Trusted provider"
)

// Capability copy states authority retained after approval.
const (
	// SocketWhatFormat takes the number of local service targets.
	SocketWhatFormat = "Connect to %d local service target(s)."
	// SocketWhatOne takes one already-formatted socket path.
	SocketWhatOne   = "Connect to the local service at %s."
	SocketIfWrong   = "The command still runs inside the filesystem sandbox, and direct outbound network stays blocked unless separately approved. The service behind each socket acts with its own authority outside the sandbox; a container engine can reach any file it mounts."
	SocketAllowLine = "connections to the exact local services listed"
	// SocketDialIfWrong is for a socket the app dials itself for one request.
	SocketDialIfWrong = "The app sends the request to the service over the approved socket, and the service acts with its own authority outside the filesystem sandbox. No other socket or network destination opens."

	DirectIPWhat      = "Run this command with direct network access, including hosts on your local network and local servers."
	DirectIPIfWrong   = "The command still runs inside the filesystem sandbox, but network services may cause effects outside it. Local service sockets remain blocked unless separately approved."
	DirectIPAllowLine = "direct network access for the selected duration (destinations unobserved)"

	LocalListenWhat = "Let commands in this chat run a local server."
	// The bind address is not bounded by the sandbox profile, so the copy names
	// local listener authority without claiming loopback-only.
	LocalListenIfWrong   = "A server this chat starts can accept connections from other programs on this machine. The command still runs inside the filesystem sandbox, and its own outbound traffic stays mediated."
	LocalListenAllowLine = "binding local server ports"

	LoopbackConnectWhat      = "Let this chat connect to the requested local service."
	LoopbackConnectIfWrong   = "A local service may expose privileged data or perform actions for its client. Filesystem restrictions and mediated external traffic remain in force."
	LoopbackConnectAllowLine = "connections to local TCP or UDP service ports"

	// WhoAgentCommand is the actor line both capability cards use.
	WhoAgentCommand = "a command the agent is running"
	WhoAgentAction  = "an action the agent is running"
)

const (
	ProcessControlWhat    = "This command can signal processes outside its sandbox. Filesystem and network restrictions remain applied; individual signal targets are not observed."
	ProcessControlIfWrong = "It can interrupt processes your account may signal, including Painted Wolf Code and other tasks."
	HostExecutionWhat     = "This command runs outside the command sandbox. Filesystem access, network destinations, and process effects are not restricted by that sandbox."
	HostExecutionIfWrong  = "It can change files and services outside this project, including Painted Wolf Code itself, and read the app's settings and credentials. Programs such as sudo may obtain additional operating-system privileges."
)

const (
	ProcessListTitle     = "Inspect host processes"
	ProcessListWhat      = "Reads process names, executable paths, owners, and process identities from this machine. Arguments and environment are not read."
	ProcessListIfWrong   = "Process metadata can reveal other applications and work on this machine."
	ProcessSignalTitle   = "Signal host processes"
	ProcessSignalWhat    = "Sends the requested signal to these exact process instances. The host rechecks identity before signaling; it does not signal by name or process group."
	ProcessSignalIfWrong = "Signals can interrupt work or discard unsaved data. A delivered signal does not mean the process has exited."
)

const (
	HostExecutionTitle  = "Run outside the command sandbox"
	ProcessControlTitle = "Run with process control"
	ExecutionAllowLine  = "the described process or execution access for the selected duration"
)
