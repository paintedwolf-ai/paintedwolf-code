package webresearch

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tools"
)

type secretScreenKey struct{}

// Grants bind transport identity; cards display the label.
type screenDestination struct {
	id    string
	label string
}

// HTTP grant identity includes the scheme and port.
func httpDestination(u *url.URL) screenDestination {
	id, label := secretmatch.HTTPDestination(u)
	return screenDestination{id: id, label: label}
}

// webSearchProviderLabels bounds provider names shown on one card.
const webSearchProviderLabels = 3

// webSearchDestination binds a release grant to the resolved provider set and endpoints.
func webSearchDestination(settings Settings, reg *Registry) screenDestination {
	resolved := resolvedSearchProviders(settings, reg)
	seen := make(map[string]string, len(resolved))
	ids := make([]string, 0, len(resolved))
	for _, provider := range resolved {
		if _, dup := seen[provider.id]; dup {
			continue
		}
		seen[provider.id] = provider.endpoint
		ids = append(ids, provider.id)
	}
	sort.Strings(ids)
	// Provider count separates variable-length destination sets.
	facts := make([]string, 0, 2*len(ids)+1)
	facts = append(facts, strconv.Itoa(len(ids)))
	for _, id := range ids {
		facts = append(facts, id, seen[id])
	}
	return screenDestination{
		id:    secretmatch.DestinationKey(string(secretmatch.SurfaceWebSearch), facts...),
		label: webSearchDestinationLabel(ids),
	}
}

// webSearchDestinationLabel names the providers a card's decision covers.
func webSearchDestinationLabel(ids []string) string {
	switch {
	case len(ids) == 0:
		return "the configured web search providers"
	case len(ids) <= webSearchProviderLabels:
		return "the configured providers " + strings.Join(ids, ", ")
	default:
		rest := len(ids) - webSearchProviderLabels
		return "the configured providers " + strings.Join(ids[:webSearchProviderLabels], ", ") +
			" and " + strconv.Itoa(rest) + " more"
	}
}

// secretScreenState resolves one decision across concurrent sends.
type secretScreenState struct {
	matcher *secretmatch.Matcher
	ask     secretmatch.AskFunc
	// contestToken is the redaction receipt the caller cited via `unredact`.
	contestToken string
	// fanout binds one decision to the complete resolved provider set.
	fanout         screenDestination
	classification map[secretmatch.SecretFingerprint]bool
	once           sync.Once
	result         atomic.Value // *secretScreenResult
}

type secretScreenResult struct {
	Decision    secretmatch.Decision
	Guidance    string
	Surface     secretmatch.ScreenSurface
	Destination string
	Match       secretmatch.Match
	// ReceiptToken names this redacted send for a later contest.
	ReceiptToken string
	Occurrences  int
	// A screening fault carries no human decision.
	Fault error
}

func (r *secretScreenResult) faulted() bool { return r != nil && r.Fault != nil }

// withSecretScreen attaches screening with an optional unredact receipt and shared destination.
func withSecretScreen(ctx context.Context, matcher *secretmatch.Matcher, ask secretmatch.AskFunc, contestToken string, fanout screenDestination) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if matcher.Inert() {
		return ctx
	}
	return context.WithValue(ctx, secretScreenKey{}, &secretScreenState{
		matcher: matcher, ask: ask, contestToken: strings.TrimSpace(contestToken), fanout: fanout,
	})
}

// SecretScreenReceipt reports a redacted send's contest token and match count.
func SecretScreenReceipt(ctx context.Context) (token string, occurrences int, ok bool) {
	st := secretScreenFrom(ctx)
	if st == nil {
		return "", 0, false
	}
	result, _ := st.result.Load().(*secretScreenResult)
	if result == nil || result.Decision != secretmatch.SendRedacted || result.ReceiptToken == "" {
		return "", 0, false
	}
	return result.ReceiptToken, result.Occurrences, true
}

func secretScreenFrom(ctx context.Context) *secretScreenState {
	if ctx == nil {
		return nil
	}
	st, _ := ctx.Value(secretScreenKey{}).(*secretScreenState)
	return st
}

// SecretDeniedError records a blocked outbound secret screen.
type SecretDeniedError struct {
	Surface  secretmatch.ScreenSurface
	Host     string
	Match    secretmatch.Match
	Guidance string
}

func (e *SecretDeniedError) Error() string {
	if e == nil {
		return "outbound secret denied"
	}
	return fmt.Sprintf("outbound secret denied: %s", e.Match.RuleID)
}

// SecretScreenFaultError records a screen that could not reach a human.
type SecretScreenFaultError struct {
	Surface secretmatch.ScreenSurface
	Host    string
	Match   secretmatch.Match
	Stage   string
	Err     error
}

func (e *SecretScreenFaultError) Error() string {
	if e == nil {
		return "outbound secret screen could not ask"
	}
	return fmt.Sprintf("outbound secret screen could not ask (%s)", e.Stage)
}

func (e *SecretScreenFaultError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Screening faults take precedence over the absent decision.
func SecretScreenFaulted(ctx context.Context) (*SecretScreenFaultError, bool) {
	st := secretScreenFrom(ctx)
	if st == nil {
		return nil, false
	}
	v, _ := st.result.Load().(*secretScreenResult)
	if !v.faulted() {
		return nil, false
	}
	stage := secretmatch.FaultStageRaise
	if fault, ok := secretmatch.Faulted(v.Fault); ok {
		stage = fault.Stage
	}
	return &SecretScreenFaultError{
		Surface: v.Surface, Host: v.Destination, Match: v.Match, Stage: stage, Err: v.Fault,
	}, true
}

// SecretScreenDenied reports whether this context's screen blocked the send.
func SecretScreenDenied(ctx context.Context) (*SecretDeniedError, bool) {
	st := secretScreenFrom(ctx)
	if st == nil {
		return nil, false
	}
	v, _ := st.result.Load().(*secretScreenResult)
	// Screening faults have no human decision and are reported separately.
	if v == nil || v.faulted() || !v.Decision.Blocks() {
		return nil, false
	}
	return &SecretDeniedError{Surface: v.Surface, Host: v.Destination, Match: v.Match, Guidance: v.Guidance}, true
}

// screenOutbound screens once and redacts only the returned copy.
func screenOutbound(ctx context.Context, surface secretmatch.ScreenSurface, destination screenDestination, text string) (string, error) {
	st := secretScreenFrom(ctx)
	if st == nil || st.matcher == nil {
		return text, nil
	}
	// One decision covers a whole fan-out, so its grant binds the fan-out.
	if st.fanout.id != "" {
		destination = st.fanout
	}
	st.once.Do(func() {
		st.classification = st.matcher.ClassificationSnapshot(ctx)
		ctx = st.matcher.WithClassifications(ctx, st.classification)
		ms := st.matcher.ScreenContext(ctx, text)
		if len(ms) == 0 {
			return
		}
		m := ms[0]
		safeLabel := st.matcher.RedactString(ctx, strings.TrimSpace(destination.label))
		if st.ask == nil {
			st.result.Store(&secretScreenResult{
				Surface: surface, Destination: safeLabel, Match: m, Occurrences: len(ms),
				Fault: secretmatch.NewAskFault(secretmatch.FaultStageScreenUnwired, nil),
			})
			return
		}
		finding := webSecretFinding(surface, destination.id, safeLabel, m, ms)
		finding.ReviewValue = secretmatch.ReviewValue(text, m)
		finding.ToolCallID = secretmatch.AskAttributionFrom(ctx).ToolCallID
		finding.ContestToken = st.contestToken
		resolution, err := st.ask(ctx, finding)
		if err != nil {
			st.result.Store(&secretScreenResult{
				Surface: surface, Destination: safeLabel, Match: m, Occurrences: len(ms),
				Fault: err,
			})
			return
		}
		st.result.Store(&secretScreenResult{
			Decision: resolution.Decision, Guidance: resolution.Guidance,
			Surface: surface, Destination: safeLabel, Match: m,
			ReceiptToken: resolution.ReceiptToken, Occurrences: len(ms),
		})
	})
	if err := st.matcher.CheckClassification(ctx, st.classification); err != nil {
		st.result.Store(&secretScreenResult{Surface: surface, Destination: destination.label, Fault: err})
	}
	if fault, ok := SecretScreenFaulted(ctx); ok {
		return "", fault
	}
	if denied, ok := SecretScreenDenied(ctx); ok {
		return "", denied
	}
	result, _ := st.result.Load().(*secretScreenResult)
	if result != nil && result.Decision == secretmatch.SendRedacted {
		ctx = st.matcher.WithClassifications(ctx, st.classification)
		return st.matcher.RedactString(ctx, text), nil
	}
	return text, nil
}

func webSecretFinding(surface secretmatch.ScreenSurface, destinationID, destinationLabel string, match secretmatch.Match, matches []secretmatch.Match) secretmatch.Alert {
	sourceTool := string(surface)
	sourcePath := "url"
	if surface == secretmatch.SurfaceWebSearch {
		sourcePath = "query"
	}
	return secretmatch.Alert{
		Surface:          surface,
		DestinationID:    destinationID,
		DestinationLabel: destinationLabel,
		RuleID:           match.RuleID,
		RuleTitle:        match.Title,
		GenericShape:     match.GenericShape,
		VarName:          match.VarName,
		Container:        match.Container,
		Occurrences:      len(matches),
		SourceKind:       secretmatch.SourceToolArgument,
		SourceTool:       sourceTool,
		SourcePath:       sourcePath,
		OriginKind:       secretmatch.OriginField,
		Fingerprints:     secretmatch.Fingerprints(matches),
	}
}

// secretScreenReject builds the structured tool reject for a blocked secret screen.
func secretScreenReject(surface secretmatch.ScreenSurface, host string, m secretmatch.Match, guidance string) *tools.ToolReject {
	reject := &tools.ToolReject{
		Code: tools.OutboundSecretDeniedCode,
		Data: map[string]any{
			"surface": string(surface),
			"rule_id": strings.TrimSpace(m.RuleID),
			"host":    strings.TrimSpace(host),
			"shape":   strings.TrimSpace(m.GenericShape),
		},
	}
	tools.AttachUserGuidance(reject, guidance)
	return reject
}

func secretScreenRejectFromErr(err *SecretDeniedError) *tools.ToolReject {
	if err == nil {
		return secretScreenReject("", "", secretmatch.Match{}, "")
	}
	return secretScreenReject(err.Surface, err.Host, err.Match, err.Guidance)
}

// Screening failures use a distinct rejection code from human denials.
func secretScreenFaultReject(err *SecretScreenFaultError) *tools.ToolReject {
	if err == nil {
		return &tools.ToolReject{Code: tools.OutboundSecretScreenFailedCode, Data: map[string]any{}}
	}
	return &tools.ToolReject{
		Code: tools.OutboundSecretScreenFailedCode,
		Data: map[string]any{
			"surface":     string(err.Surface),
			"rule_id":     strings.TrimSpace(err.Match.RuleID),
			"host":        strings.TrimSpace(err.Host),
			"shape":       strings.TrimSpace(err.Match.GenericShape),
			"fault_stage": err.Stage,
		},
	}
}
