package httpaction

import (
	"context"
	"net/url"

	"github.com/lycaon/lycaon/internal/httpcookies"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// prepareCookieDelivery keeps release decisions on this invocation's opened jar.
// The HTTP client's before-hop boundary reviews newly eligible cookies as well.
func prepareCookieDelivery(ctx context.Context, deps Deps, spec requestSpec, jar *secretcap.CookieJar) (func(context.Context, *url.URL) error, error) {
	if jar == nil {
		return nil, nil
	}
	nativeIssuer := ""
	if spec.socket != "" {
		nativeIssuer = spec.socketBinding.Key()
	}
	reviewed := make(map[httpcookies.Cookie]map[string]bool)
	current := make(map[httpcookies.Cookie]bool)
	jar.Store.SetDelivery(nativeIssuer, func(cookie httpcookies.Cookie) bool { return current[cookie] })
	beforeHop := func(hopCtx context.Context, target *url.URL) error {
		hopSpec := spec
		hopSpec.target = target
		destination, label := requestDestination(hopSpec)
		var foreign []httpcookies.Cookie
		current = make(map[httpcookies.Cookie]bool)
		for _, cookie := range jar.Store.Selected(target) {
			// Ordinary URL cookies retain their RFC selection. A native receipt can
			// never be promoted into ordinary URL authority by changing transports.
			if cookie.Value == "" || (nativeIssuer == "" && cookie.NativeIssuer == "") || (nativeIssuer != "" && cookie.NativeIssuer == nativeIssuer) {
				current[cookie] = true
				continue
			}
			if decisions := reviewed[cookie]; decisions != nil {
				if allowed, ok := decisions[destination]; ok {
					current[cookie] = allowed
					continue
				}
			}
			foreign = append(foreign, cookie)
		}
		if len(foreign) == 0 {
			return nil
		}
		if deps.SecretAsk == nil {
			return screenFaultReject(secretmatch.FaultStageScreenUnwired)
		}
		var names []string
		var fingerprints []secretmatch.SecretFingerprint
		for _, cookie := range foreign {
			match, err := deps.SecretMatcher.ManagedValue(cookie.Value, cookie.Name, jar.Reference)
			if err != nil {
				return screenFaultReject(secretmatch.FaultStageScreenUnwired)
			}
			names = append(names, cookie.Name)
			fingerprints = append(fingerprints, match.Fingerprint)
		}
		resolution, err := deps.SecretAsk(hopCtx, secretmatch.Alert{
			Surface: secretmatch.SurfaceHTTPRequest, DestinationID: destination, DestinationLabel: label,
			SecretNames: sortedUnique(names), Fingerprints: fingerprints,
			RuleID: secretmatch.ManagedRuleID, RuleTitle: secretmatch.ManagedRuleTitle,
			GenericShape: "Protected value", Occurrences: len(foreign),
			SourceKind: secretmatch.SourceToolArgument, SourceTool: "http_request", SourcePath: "cookies",
			OriginKind: secretmatch.OriginField, Container: "cookie jar " + jar.Name,
			IssuerUnverified: true, RecipientsLocal: hopSpec.dialsLoopback(),
		})
		if err != nil {
			stage := secretmatch.FaultStageRaise
			if fault, ok := secretmatch.Faulted(err); ok {
				stage = fault.Stage
			}
			return screenFaultReject(stage)
		}
		// A denial/redaction withholds only these bytes, not the HTTP operation.
		for _, cookie := range foreign {
			if reviewed[cookie] == nil {
				reviewed[cookie] = make(map[string]bool)
			}
			allowed := resolution.Decision == secretmatch.SendUnchanged
			reviewed[cookie][destination], current[cookie] = allowed, allowed
		}
		return nil
	}
	return beforeHop, beforeHop(ctx, spec.target)
}
