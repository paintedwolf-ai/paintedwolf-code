// Package detectionpack matches declarative approval-overlay rules.
package detectionpack

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/hitl"
)

// Level is a rule severity.
type Level string

const (
	LevelInformational Level = "informational"
	LevelLow           Level = "low"
	LevelMedium        Level = "medium"
	LevelHigh          Level = "high"
	LevelCritical      Level = "critical"
)

// Escalates reports whether a severity requires review under posture. An unknown
// token is inert rather than coerced into a band.
func Escalates(level Level, posture string) bool {
	p := strings.ToLower(strings.TrimSpace(posture))
	switch p {
	case "light":
		return level == LevelCritical
	case "balanced":
		return level == LevelHigh || level == LevelCritical
	case "strict":
		return level == LevelMedium || level == LevelHigh || level == LevelCritical
	default:
		return false
	}
}

// LogSource selects which event shape a rule matches against.
type LogSource string

const (
	SourceToolExec       LogSource = "tool_exec"
	SourceEgressObserved LogSource = "egress_observed"
)

// Event is the synthetic tool-execution record rules match against.
type Event struct {
	Tool                  string
	Image                 []string // one entry per stage, wrapper-resolved; any-element match
	CommandLine           string   // this process only — each line and pipe stage is its own event
	PipelineCommandLine   string   // the whole connected pipeline this process belongs to
	Argv                  []string
	ApiAction             []string // host-derived "<service>:<op>" identities
	ActionEffect          []string
	TargetScope           []string
	PrincipalScope        []string
	CredentialPersistence []string
	BulkAction            string // "true" | "false" | "unknown" | ""
	AmountPresent         string
	TargetPresent         string
	ProjectDir            string
	Contained             string // "true" | "false"
	EgressMode            string // "deny" | "proxy" | "direct_ip"
	SessionID             string
	ActionID              string
	FSJailed              string
	RootsDigest           string
	LoopbackAccess        string // "true" when direct localhost authority applies
	SocketCount           string
	SocketCapability      string
	SocketApprovedPath    []string
	SocketResolvedPath    []string
	SocketScope           []string
	SocketGrantState      []string
	EffectiveAuthority    []string
	DirectIP              string
	Visibility            string
	DeclaredDestination   []string
	ToolArg               []string // "name=value" per short scalar argument
	TargetFile            []string // paths the action names
	// EffectReach is host-derived: local | remote | unproven. Empty observation
	// projects as unproven so an unset producer fails toward asking.
	EffectReach     string
	executionGroups []Event
}

// Bounds for the tool-argument projection that gives argv-less tools something to
// match on.
const (
	maxToolArgs     = 32
	maxToolArgBytes = 256
	maxTargetFiles  = 32
	maxToolArgDepth = 3
)

// ActionObservation is the host-derived input to a tool execution event.
type ActionObservation struct {
	Tool, CommandLine, ProjectDir, SessionID, ActionID string
	Image, Argv, APIActions                            []string
	ActionEffects, TargetScopes                        []string
	PrincipalScopes, CredentialPersistence             []string
	BulkAction, AmountPresent, TargetPresent           string
	Boundary                                           hitl.Contained
	Sockets                                            []SocketObservation
	Visibility                                         string
	DeclaredDestinations                               []string
	ToolArgs                                           []string
	TargetFiles                                        []string
	EffectReach                                        string
	// ProcessEffectReach is indexed like the process events, so one stage's
	// local proof never covers a neighboring process.
	ProcessEffectReach []string
	// Plan is the executor's resolved argv, one entry of stage lines per
	// execution group; it splits processes instead of CommandLine. Empty for pty keystrokes.
	Plan [][]string
}

// SocketObservation is one parallel, host-resolved exact socket record.
type SocketObservation struct {
	ApprovedPath, ResolvedPath, Scope, GrantState, EffectiveAuthority string
}

// EgressEvent is one proxied outbound connection.
type EgressEvent struct {
	DestinationHostname string
	DestinationPort     string
	DestinationIP       string
	Transport           string
	Image               []string // semantic tool and wrapper-resolved command images
	Initiated           string   // always "true"
	SessionID           string
	ActionID            string
	Origin              string
	DecisionStage       string
	RequestMethod       string
	RequestPath         string
}

// EgressObservation is one canonical broker-recorded destination observation.
// Host, port, and transport are normalized before this package receives them.
type EgressObservation struct {
	DestinationHostname string
	DestinationPort     uint16
	DestinationIP       string
	Transport           string
	Image               []string
	SessionID           string
	ActionID            string
	Origin              string
	DecisionStage       string
	RequestMethod       string
	RequestPath         string
}

// Match cites the rule that fired.
type Match struct {
	PackID    string
	RuleID    string
	RuleTitle string
	Level     Level
	// Effect flags determine gating; Level controls presentation severity.
	External      bool
	Local         bool
	Unrecoverable bool
	// MintsCredential marks a call whose output carries a freshly issued
	// credential. It raises no card; it tells the host to treat the result as
	// provenance evidence, the same class as a credential container's contents.
	MintsCredential bool
	// Untagged matches require review.
	Tagged bool
}

// Effect tags classify where a matched action lands.
const (
	TagEffectExternal = "lycaon.effect.external"
	// TagEffectLocal marks an effect on the local machine.
	TagEffectLocal         = "lycaon.effect.local"
	TagEffectUnrecoverable = "lycaon.effect.unrecoverable"
	// TagEffectMintsCredential marks a call that issues a credential in its
	// output. Shape rules need their keyword within a few dozen characters of a
	// value, which a minted token is printed far from.
	TagEffectMintsCredential = "lycaon.effect.mints_credential"
)

// Effect is a rule's declared effect class.
type Effect struct {
	External        bool
	Local           bool
	Unrecoverable   bool
	MintsCredential bool
	// Untagged effects require review.
	Tagged bool
}

// EffectFromTags reads the declared effect class off a rule's tags.
func EffectFromTags(tags []string) Effect {
	var effect Effect
	for _, tag := range tags {
		switch strings.ToLower(strings.TrimSpace(tag)) {
		case TagEffectExternal:
			effect.External, effect.Tagged = true, true
		case TagEffectLocal:
			effect.Local, effect.Tagged = true, true
		case TagEffectUnrecoverable:
			effect.Unrecoverable, effect.Tagged = true, true
		case TagEffectMintsCredential:
			effect.MintsCredential, effect.Tagged = true, true
		}
	}
	return effect
}

// NormalizeCommandLine collapses whitespace, trims, and pads with one leading and
// one trailing space so rules can use space-delimited tokens like " rm ".
func NormalizeCommandLine(raw string) string {
	joined := strings.Join(strings.Fields(raw), " ")
	if joined == "" {
		return ""
	}
	return " " + joined + " "
}

// SplitArgv returns shell-ish tokens across every stage of the command.
func SplitArgv(raw string) []string {
	normalized := strings.Join(strings.Fields(raw), " ")
	if normalized == "" {
		return nil
	}
	tokens := commandsurface.TokenizeShell(normalized)
	out := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		if commandsurface.IsShellOperator(tok) {
			continue
		}
		out = append(out, stripQuotes(tok))
	}
	return out
}

// ApiActionFromArgv returns a recognized service operation.
func ApiActionFromArgv(image string, argv []string) string {
	if image != "aws" {
		return ""
	}
	positionals := awsCommandPositionals(argv[1:])
	if len(positionals) < 2 {
		return ""
	}
	return strings.ToLower(positionals[0] + ":" + positionals[1])
}

var awsGlobalOptionsWithValue = map[string]struct{}{
	"--ca-bundle": {}, "--cli-binary-format": {}, "--cli-connect-timeout": {},
	"--cli-read-timeout": {}, "--color": {}, "--endpoint-url": {}, "--output": {},
	"--profile": {}, "--query": {}, "--region": {},
}

var awsGlobalFlags = map[string]struct{}{
	"--cli-auto-prompt": {}, "--debug": {}, "--no-cli-auto-prompt": {},
	"--no-cli-pager": {}, "--no-paginate": {}, "--no-sign-request": {},
	"--no-verify-ssl": {}, "--version": {},
}

func awsCommandPositionals(args []string) []string {
	for len(args) > 0 {
		tok := strings.ToLower(stripQuotes(args[0]))
		if tok == "--" {
			args = args[1:]
			break
		}
		if !strings.HasPrefix(tok, "-") {
			break
		}
		name := tok
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = name[:i]
			if _, ok := awsGlobalOptionsWithValue[name]; ok {
				args = args[1:]
				continue
			}
		}
		if _, ok := awsGlobalFlags[name]; ok {
			args = args[1:]
			continue
		}
		if _, ok := awsGlobalOptionsWithValue[name]; ok && len(args) > 1 {
			args = args[2:]
			continue
		}
		return nil
	}
	if len(args) < 2 || strings.HasPrefix(stripQuotes(args[0]), "-") || strings.HasPrefix(stripQuotes(args[1]), "-") {
		return nil
	}
	return []string{stripQuotes(args[0]), stripQuotes(args[1])}
}

func newCommandEvent(
	tool, rawCommand, projectDir string, contained bool, egressMode, sessionID string, plan [][]string,
) Event {
	ev := newCommandEventFields(tool, rawCommand, projectDir, contained, egressMode, sessionID)
	ev.PipelineCommandLine = ev.CommandLine
	groups := commandEventGroups(tool, rawCommand, projectDir, contained, egressMode, sessionID, plan)
	if len(groups) > 1 {
		ev.executionGroups = groups
	}
	return ev
}

// commandEventGroups builds one event per process from the resolved plan, or
// from the tokenized text when no argv exists (pty keystrokes).
func commandEventGroups(
	tool, rawCommand, projectDir string, contained bool, egressMode, sessionID string, plan [][]string,
) []Event {
	groups := make([]Event, 0, 4)
	stageEvent := func(stageLine, pipelineLine string) Event {
		ev := newCommandEventFields(tool, stageLine, projectDir, contained, egressMode, sessionID)
		ev.PipelineCommandLine = pipelineLine
		return ev
	}
	for _, process := range commandProcesses(rawCommand, plan) {
		groups = append(groups, stageEvent(process.commandLine, process.pipelineLine))
	}
	return groups
}

type commandProcess struct {
	commandLine  string
	pipelineLine string
}

// commandProcesses is the one process projection for matching and effect
// reach, so reach index i describes event i.
func commandProcesses(rawCommand string, plan [][]string) []commandProcess {
	return expandInlineShellScripts(hostCommandProcesses(rawCommand, plan), 0)
}

// maxInlineShellDepth bounds recursive inline scripts.
const maxInlineShellDepth = 3

// expandInlineShellScripts appends processes started by inline scripts. The
// wrapper stays in the list, so the projection only ever adds events and the
// per-stage reach at index i keeps describing the event at index i.
func expandInlineShellScripts(processes []commandProcess, depth int) []commandProcess {
	if depth >= maxInlineShellDepth {
		return processes
	}
	out := make([]commandProcess, 0, len(processes))
	for _, process := range processes {
		out = append(out, process)
		script, ok := inlineShellScript(process.commandLine)
		if !ok {
			continue
		}
		inner := expandInlineShellScripts(hostCommandProcesses(script, nil), depth+1)
		out = append(out, inner...)
	}
	return out
}

// shellInterpreters are the supported inline-script interpreters.
var shellInterpreters = map[string]struct{}{
	"sh": {}, "bash": {}, "zsh": {}, "dash": {}, "ksh": {}, "ash": {}, "fish": {},
}

// inlineShellScript returns the script selected by a short `-c` option.
func inlineShellScript(commandLine string) (string, bool) {
	stages := resolveStages(commandLine)
	if len(stages) != 1 {
		return "", false
	}
	if _, ok := shellInterpreters[stages[0].image]; !ok {
		return "", false
	}
	argv := stages[0].argv
	for i := 1; i < len(argv); i++ {
		token := argv[i]
		switch {
		case token == "--":
			continue
		case !strings.HasPrefix(token, "-"):
			// A script path does not expose the commands it runs.
			return "", false
		case !shellInlineFlag(token):
			continue
		case i+1 < len(argv):
			return argv[i+1], true
		default:
			return "", false
		}
	}
	return "", false
}

// shellInlineFlag reports whether a short-option bundle includes `c`.
func shellInlineFlag(token string) bool {
	if !strings.HasPrefix(token, "-") || strings.HasPrefix(token, "--") {
		return false
	}
	return strings.ContainsRune(token[1:], 'c')
}

// hostCommandProcesses projects the top-level process plan.
func hostCommandProcesses(rawCommand string, plan [][]string) []commandProcess {
	var out []commandProcess
	if len(plan) > 0 {
		for _, group := range plan {
			pipelineLine := NormalizeCommandLine(strings.Join(group, " | "))
			for _, stage := range group {
				out = append(out, commandProcess{commandLine: stage, pipelineLine: pipelineLine})
			}
		}
		return out
	}
	for _, line := range commandsurface.SplitCommandLines(rawCommand) {
		for _, pipeline := range commandsurface.SplitExecutionGroups(line) {
			pipelineLine := NormalizeCommandLine(strings.Join(pipeline, " "))
			for _, stage := range commandsurface.SplitPipelineStages(pipeline) {
				out = append(out, commandProcess{
					commandLine: strings.Join(stage, " "), pipelineLine: pipelineLine,
				})
			}
		}
	}
	return out
}

func newCommandEventFields(tool, rawCommand, projectDir string, contained bool, egressMode, sessionID string) Event {
	containedStr := "false"
	if contained {
		containedStr = "true"
	}
	stages := resolveStages(rawCommand)
	images := make([]string, 0, len(stages))
	actions := make([]string, 0, len(stages))
	for _, st := range stages {
		if st.image != "" {
			images = append(images, st.image)
		}
		if action := ApiActionFromArgv(st.image, st.argv); action != "" {
			actions = append(actions, action)
		}
	}
	return Event{
		Tool:        tool,
		Image:       images,
		CommandLine: NormalizeCommandLine(rawCommand),
		Argv:        SplitArgv(rawCommand),
		ApiAction:   actions,
		ProjectDir:  projectDir,
		Contained:   containedStr,
		EgressMode:  egressMode,
		SessionID:   sessionID,
	}
}

// NewEvent builds the complete capability-aware execution event from one
// canonical host observation.
func NewEvent(in ActionObservation) Event {
	directIP := in.Boundary.DirectIP
	ev := newCommandEvent(
		in.Tool,
		in.CommandLine,
		in.ProjectDir,
		in.Boundary.FSJailed && !directIP && len(in.Sockets) == 0,
		in.Boundary.Egress,
		in.SessionID,
		in.Plan,
	)
	if in.CommandLine == "" {
		if imgs := canonicalBounded(in.Image, 32); len(imgs) > 0 {
			ev.Image = imgs
		}
		if argv := canonicalBounded(in.Argv, 256); len(argv) > 0 {
			ev.Argv = argv
		}
	}
	applyObservationFields(&ev, in)
	for i := range ev.executionGroups {
		applyObservationFields(&ev.executionGroups[i], in)
		if len(in.ProcessEffectReach) > 0 {
			reach := ""
			if i < len(in.ProcessEffectReach) {
				reach = in.ProcessEffectReach[i]
			}
			ev.executionGroups[i].EffectReach = ProjectEffectReach(reach)
		}
	}
	return ev
}

func applyObservationFields(ev *Event, in ActionObservation) {
	if ev == nil {
		return
	}
	if mapped := canonicalBounded(in.APIActions, 64); len(mapped) > 0 {
		ev.ApiAction = mapped
	}
	ev.ActionEffect = ProjectConsequenceEnums("ActionEffect", in.ActionEffects, 32)
	ev.TargetScope = ProjectConsequenceEnums("TargetScope", in.TargetScopes, 16)
	ev.PrincipalScope = ProjectConsequenceEnums("PrincipalScope", in.PrincipalScopes, 16)
	ev.CredentialPersistence = ProjectConsequenceEnums("CredentialPersistence", in.CredentialPersistence, 8)
	ev.BulkAction = ProjectPresence(in.BulkAction)
	ev.AmountPresent = ProjectPresence(in.AmountPresent)
	ev.TargetPresent = ProjectPresence(in.TargetPresent)
	ev.ActionID = strings.TrimSpace(in.ActionID)
	ev.FSJailed = boolString(in.Boundary.FSJailed)
	ev.RootsDigest = rootsDigest(in.Boundary.Roots)
	ev.LoopbackAccess = boolString(in.Boundary.LoopbackAccess)
	for _, socket := range canonicalSockets(in.Sockets, 8) {
		ev.SocketApprovedPath = append(ev.SocketApprovedPath, socket.ApprovedPath)
		ev.SocketResolvedPath = append(ev.SocketResolvedPath, socket.ResolvedPath)
		ev.SocketScope = append(ev.SocketScope, socket.Scope)
		ev.SocketGrantState = append(ev.SocketGrantState, socket.GrantState)
		ev.EffectiveAuthority = append(ev.EffectiveAuthority, socket.EffectiveAuthority)
	}
	ev.SocketCount = intString(len(ev.SocketApprovedPath))
	ev.SocketCapability = socketCapability(ev.SocketScope)
	ev.DirectIP = boolString(in.Boundary.DirectIP)
	ev.Visibility = strings.TrimSpace(in.Visibility)
	ev.DeclaredDestination = canonicalBounded(in.DeclaredDestinations, 32)
	ev.ToolArg = canonicalBounded(in.ToolArgs, maxToolArgs)
	ev.TargetFile = canonicalBounded(homeRelativePaths(in.TargetFiles), maxTargetFiles)
	ev.EffectReach = ProjectEffectReach(in.EffectReach)
}

// projectToolArgs flattens structured tool arguments into bounded key-value strings for rule matching.
func projectToolArgs(args map[string]any) []string {
	out := make([]string, 0, maxToolArgs)
	var walk func(prefix string, value any, depth int)
	walk = func(prefix string, value any, depth int) {
		if len(out) >= maxToolArgs || depth > maxToolArgDepth {
			return
		}
		switch t := value.(type) {
		case string:
			if len(t) <= maxToolArgBytes {
				out = append(out, prefix+"="+t)
			}
		case bool:
			out = append(out, prefix+"="+boolString(t))
		case float64:
			out = append(out, prefix+"="+strconv.FormatFloat(t, 'f', -1, 64))
		case int:
			out = append(out, prefix+"="+intString(t))
		case []any:
			for _, item := range t {
				walk(prefix, item, depth+1)
			}
		case map[string]any:
			for _, key := range sortedKeys(t) {
				walk(prefix+"."+key, t[key], depth+1)
			}
		}
	}
	for _, key := range sortedKeys(args) {
		walk(key, args[key], 1)
	}
	return canonicalBounded(out, maxToolArgs)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func socketCapability(scopes []string) string {
	best, rank := "none", 0
	ranks := map[string]int{"requested": 1, "current_action": 2, "chat": 3, "durable": 4}
	for _, scope := range scopes {
		if ranks[scope] > rank {
			best, rank = scope, ranks[scope]
		}
	}
	return best
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func intString(v int) string {
	return fmt.Sprintf("%d", v)
}

func rootsDigest(roots []string) string {
	canonical := canonicalBounded(roots, 64)
	if len(canonical) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(canonical, "\x00")))
	return hex.EncodeToString(sum[:])
}

func canonicalBounded(values []string, limit int) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func canonicalSockets(values []SocketObservation, limit int) []SocketObservation {
	byKey := make(map[string]SocketObservation, len(values))
	for _, value := range values {
		value.ApprovedPath = strings.TrimSpace(value.ApprovedPath)
		value.ResolvedPath = strings.TrimSpace(value.ResolvedPath)
		value.Scope = strings.TrimSpace(value.Scope)
		value.GrantState = strings.TrimSpace(value.GrantState)
		value.EffectiveAuthority = strings.TrimSpace(value.EffectiveAuthority)
		if value.ApprovedPath == "" || value.ResolvedPath == "" {
			continue
		}
		byKey[value.ApprovedPath+"\x00"+value.ResolvedPath] = value
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > limit {
		keys = keys[:limit]
	}
	out := make([]SocketObservation, 0, len(keys))
	for _, key := range keys {
		out = append(out, byKey[key])
	}
	return out
}

// NewEgressEvent builds an event from a broker-normalized observation.
func NewEgressEvent(in EgressObservation) EgressEvent {
	port := ""
	if in.DestinationPort > 0 {
		port = intString(int(in.DestinationPort))
	}
	return EgressEvent{
		DestinationHostname: strings.TrimSuffix(strings.ToLower(strings.TrimSpace(in.DestinationHostname)), "."),
		DestinationPort:     port,
		DestinationIP:       strings.TrimSpace(in.DestinationIP),
		Transport:           strings.TrimSpace(in.Transport),
		Image:               canonicalBounded(in.Image, 32),
		Initiated:           "true",
		SessionID:           strings.TrimSpace(in.SessionID),
		ActionID:            strings.TrimSpace(in.ActionID),
		Origin:              strings.TrimSpace(in.Origin),
		DecisionStage:       strings.TrimSpace(in.DecisionStage),
		RequestMethod:       strings.ToUpper(strings.TrimSpace(in.RequestMethod)),
		RequestPath:         strings.TrimSpace(in.RequestPath),
	}
}

func (ev Event) lookup(field string) (any, bool) {
	switch field {
	case "Tool":
		return ev.Tool, true
	case "Image":
		return ev.Image, true
	case "CommandLine":
		return ev.CommandLine, true
	case "PipelineCommandLine":
		return ev.PipelineCommandLine, true
	case "Argv":
		return ev.Argv, true
	case "ApiAction":
		return ev.ApiAction, true
	case "ActionEffect":
		return ev.ActionEffect, true
	case "TargetScope":
		return ev.TargetScope, true
	case "PrincipalScope":
		return ev.PrincipalScope, true
	case "CredentialPersistence":
		return ev.CredentialPersistence, true
	case "BulkAction":
		return ev.BulkAction, true
	case "AmountPresent":
		return ev.AmountPresent, true
	case "TargetPresent":
		return ev.TargetPresent, true
	case "ProjectDir":
		return ev.ProjectDir, true
	case "Contained":
		return ev.Contained, true
	case "EgressMode":
		return ev.EgressMode, true
	case "SessionId":
		return ev.SessionID, true
	case "ActionId":
		return ev.ActionID, true
	case "FSJailed":
		return ev.FSJailed, true
	case "RootsDigest":
		return ev.RootsDigest, true
	case "LoopbackAccess":
		return ev.LoopbackAccess, true
	case "SocketCount":
		return ev.SocketCount, true
	case "SocketCapability":
		return ev.SocketCapability, true
	case "SocketApprovedPath":
		return ev.SocketApprovedPath, true
	case "SocketResolvedPath":
		return ev.SocketResolvedPath, true
	case "SocketScope":
		return ev.SocketScope, true
	case "SocketGrantState":
		return ev.SocketGrantState, true
	case "EffectiveAuthority":
		return ev.EffectiveAuthority, true
	case "DirectIp":
		return ev.DirectIP, true
	case "Visibility":
		return ev.Visibility, true
	case "DeclaredDestination":
		return ev.DeclaredDestination, true
	case "ToolArg":
		return ev.ToolArg, true
	case "TargetFile":
		return ev.TargetFile, true
	case "EffectReach":
		return ev.EffectReach, true
	default:
		return nil, false
	}
}

func (ev EgressEvent) lookup(field string) (any, bool) {
	switch field {
	case "DestinationHostname":
		return ev.DestinationHostname, true
	case "DestinationPort":
		return ev.DestinationPort, true
	case "DestinationIp":
		return ev.DestinationIP, true
	case "Transport":
		return ev.Transport, true
	case "Image":
		return ev.Image, true
	case "Initiated":
		return ev.Initiated, true
	case "SessionId":
		return ev.SessionID, true
	case "ActionId":
		return ev.ActionID, true
	case "Origin":
		return ev.Origin, true
	case "DecisionStage":
		return ev.DecisionStage, true
	case "RequestMethod":
		return ev.RequestMethod, true
	case "RequestPath":
		return ev.RequestPath, true
	default:
		return nil, false
	}
}

var toolExecFields = map[string]struct{}{
	"Tool": {}, "Image": {}, "CommandLine": {}, "PipelineCommandLine": {}, "Argv": {}, "ApiAction": {},
	"ActionEffect": {}, "TargetScope": {}, "PrincipalScope": {}, "CredentialPersistence": {},
	"BulkAction": {}, "AmountPresent": {}, "TargetPresent": {},
	"ProjectDir": {}, "Contained": {}, "EgressMode": {}, "SessionId": {},
	"ActionId": {}, "FSJailed": {}, "RootsDigest": {}, "LoopbackAccess": {},
	"SocketCount": {}, "SocketCapability": {}, "SocketApprovedPath": {}, "SocketResolvedPath": {},
	"SocketScope": {}, "SocketGrantState": {}, "EffectiveAuthority": {},
	"DirectIp": {}, "Visibility": {}, "DeclaredDestination": {},
	"ToolArg": {}, "TargetFile": {}, "EffectReach": {},
}

var egressFields = map[string]struct{}{
	"DestinationHostname": {}, "DestinationPort": {}, "DestinationIp": {},
	"Transport": {}, "Image": {}, "Initiated": {}, "SessionId": {},
	"ActionId": {}, "Origin": {}, "DecisionStage": {},
	"RequestMethod": {}, "RequestPath": {},
}

func fieldsForSource(src LogSource) map[string]struct{} {
	switch src {
	case SourceToolExec:
		return toolExecFields
	case SourceEgressObserved:
		return egressFields
	default:
		return nil
	}
}

// transparentWrappers, credentialBrokers, and packageRunners are peeled only during image normalization.
var transparentWrappers = map[string]struct{}{
	"sudo": {}, "env": {}, "command": {}, "exec": {}, "nohup": {},
	"time": {}, "stdbuf": {}, "nice": {}, "xargs": {},
}

var credentialBrokers = map[string]struct{}{
	"aws-vault": {}, "doppler": {}, "op": {}, "assume": {}, "chamber": {},
}

var packageRunners = map[string]struct{}{
	"npx": {}, "bunx": {}, "pnpx": {}, "dlx": {}, "pipx": {}, "uv": {},
	"poetry": {}, "rye": {}, "hatch": {}, "bundle": {}, "mise": {},
	"asdf": {}, "rbenv": {}, "pyenv": {},
	"npm": {}, "pnpm": {}, "yarn": {},
}

// alwaysPeelRunners always wrap another program (npx wrangler …). The rest only
// peel when a launcher subcommand follows (poetry run …, pnpm dlx …), so
// `poetry publish` and `npm install` keep their own Image.
var alwaysPeelRunners = map[string]struct{}{
	"npx": {}, "bunx": {}, "pnpx": {}, "dlx": {}, "pipx": {},
}

// packageRunnerPeelSub reports whether sub is a launcher verb for runner.
// npm/pnpm/yarn "run" names a package.json script, not a binary — only exec/dlx peel.
func packageRunnerPeelSub(runner, sub string) bool {
	switch runner {
	case "npm", "pnpm", "yarn":
		return sub == "exec" || sub == "dlx"
	default:
		return sub == "run" || sub == "exec" || sub == "dlx"
	}
}

type resolvedStage struct {
	image string
	argv  []string // tokens after resolution, argv[0] is the program basename when present
}

func resolveStages(raw string) []resolvedStage {
	normalized := strings.Join(strings.Fields(raw), " ")
	if normalized == "" {
		return nil
	}
	stageToks := splitStages(normalized)
	out := make([]resolvedStage, 0, len(stageToks))
	for _, toks := range stageToks {
		st := resolveOneStage(toks)
		if st.image != "" || len(st.argv) > 0 {
			out = append(out, st)
		}
	}
	return out
}

func resolveOneStage(tokens []string) resolvedStage {
	toks := append([]string(nil), tokens...)
	for step := 0; step < 8; step++ {
		toks = dropLeadingAssignments(toks)
		changed := false
		for len(toks) > 0 {
			base := strings.ToLower(filepath.Base(stripQuotes(toks[0])))
			if _, ok := transparentWrappers[base]; ok {
				toks = peelTransparentWrapper(base, toks[1:])
				changed = true
				continue
			}
			break
		}
		if len(toks) == 0 {
			return resolvedStage{}
		}
		// Drop a bare -- a wrapper left behind.
		if stripQuotes(toks[0]) == "--" {
			toks = toks[1:]
			changed = true
			if len(toks) == 0 {
				return resolvedStage{}
			}
		}
		base := strings.ToLower(filepath.Base(stripQuotes(toks[0])))
		if _, ok := credentialBrokers[base]; ok {
			if idx := indexBareDoubleDash(toks[1:]); idx >= 0 {
				toks = toks[1+idx+1:]
				continue
			}
		}
		if _, ok := packageRunners[base]; ok {
			rest := toks[1:]
			for len(rest) > 0 && strings.HasPrefix(stripQuotes(rest[0]), "-") {
				rest = rest[1:]
			}
			hasLauncherSub := false
			if len(rest) > 0 {
				sub := strings.ToLower(stripQuotes(rest[0]))
				hasLauncherSub = packageRunnerPeelSub(base, sub)
			}
			_, always := alwaysPeelRunners[base]
			if !always && !hasLauncherSub {
				// e.g. poetry publish / npm install — the runner is the program.
				break
			}
			toks = rest
			if hasLauncherSub {
				toks = toks[1:]
			}
			if len(toks) == 0 {
				return resolvedStage{}
			}
			continue
		}
		if !changed {
			break
		}
	}
	toks = dropLeadingAssignments(toks)
	if len(toks) == 0 {
		return resolvedStage{}
	}
	argv := make([]string, len(toks))
	for i, t := range toks {
		argv[i] = stripQuotes(t)
	}
	image := strings.ToLower(filepath.Base(argv[0]))
	argv[0] = image
	return resolvedStage{image: image, argv: argv}
}

var wrapperOptionsWithValue = map[string]map[string]struct{}{
	"sudo": {"-C": {}, "-D": {}, "-g": {}, "-h": {}, "-p": {}, "-R": {}, "-T": {}, "-u": {},
		"--chdir": {}, "--close-from": {}, "--group": {}, "--host": {}, "--prompt": {},
		"--role": {}, "--type": {}, "--user": {}},
	"env":    {"-C": {}, "-S": {}, "-u": {}, "--chdir": {}, "--split-string": {}, "--unset": {}},
	"exec":   {"-a": {}},
	"nice":   {"-n": {}, "--adjustment": {}},
	"stdbuf": {"-i": {}, "-o": {}, "-e": {}},
	"time":   {"-f": {}, "-o": {}, "--format": {}, "--output": {}},
	"xargs": {"-a": {}, "-d": {}, "-E": {}, "-I": {}, "-L": {}, "-n": {}, "-P": {}, "-s": {},
		"--arg-file": {}, "--delimiter": {}, "--eof": {}, "--max-args": {}, "--max-chars": {},
		"--max-lines": {}, "--max-procs": {}, "--replace": {}},
}

func peelTransparentWrapper(wrapper string, args []string) []string {
	wantsValue := wrapperOptionsWithValue[wrapper]
	for len(args) > 0 {
		tok := stripQuotes(args[0])
		if tok == "--" {
			return args[1:]
		}
		if wrapper == "env" && isEnvAssignment(tok) {
			args = args[1:]
			continue
		}
		if !strings.HasPrefix(tok, "-") || tok == "-" {
			return args
		}
		if i := strings.IndexByte(tok, '='); i >= 0 {
			args = args[1:]
			continue
		}
		if _, ok := wantsValue[tok]; ok && len(args) > 1 {
			args = args[2:]
			continue
		}
		args = args[1:]
	}
	return nil
}

func dropLeadingAssignments(toks []string) []string {
	for len(toks) > 0 {
		t := stripQuotes(toks[0])
		if isEnvAssignment(t) {
			toks = toks[1:]
			continue
		}
		break
	}
	return toks
}

func isEnvAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	name := tok[:eq]
	if name == "" {
		return false
	}
	for i, r := range name {
		if i == 0 {
			if !(unicode.IsLetter(r) || r == '_') {
				return false
			}
			continue
		}
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			return false
		}
	}
	return true
}

func indexBareDoubleDash(toks []string) int {
	for i, t := range toks {
		if stripQuotes(t) == "--" {
			return i
		}
	}
	return -1
}

func splitStages(normalized string) [][]string {
	tokens := commandsurface.TokenizeShell(normalized)
	var stages [][]string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			stages = append(stages, cur)
			cur = nil
		}
	}
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if commandsurface.IsStageSeparator(tok) {
			flush()
			continue
		}
		cur = append(cur, tok)
	}
	flush()
	return stages
}

func stripQuotes(tok string) string {
	if len(tok) >= 2 {
		if (tok[0] == '\'' && tok[len(tok)-1] == '\'') || (tok[0] == '"' && tok[len(tok)-1] == '"') {
			return tok[1 : len(tok)-1]
		}
	}
	return tok
}

// homeRelativePaths rewrites paths under the user's home directory to ~/ form,
// so rules name credential stores without a machine-specific home path.
func homeRelativePaths(values []string) []string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return values
	}
	home = filepath.Clean(home)
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		clean := filepath.Clean(trimmed)
		if clean == home {
			out = append(out, "~")
			continue
		}
		if rest, ok := strings.CutPrefix(clean, home+string(filepath.Separator)); ok {
			out = append(out, "~/"+filepath.ToSlash(rest))
			continue
		}
		out = append(out, value)
	}
	return out
}
