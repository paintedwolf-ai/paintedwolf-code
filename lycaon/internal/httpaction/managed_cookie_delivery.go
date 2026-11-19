package httpaction

import (
	"context"
	"net/http"

	"github.com/lycaon/lycaon/internal/httpcookies"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tools"
)

func prepareRequestCookieDelivery(
	ctx context.Context, deps Deps, spec requestSpec, tctx tools.ToolContext,
	jar *secretcap.CookieJar, frontendJar http.CookieJar, preRunHop0 bool,
) (func(context.Context, outboundhttp.HopRequest) error, error) {
	if jar != nil {
		return managedJarCookieDelivery(ctx, deps, spec, jar, preRunHop0)
	}
	return frontendJarCookieDelivery(ctx, deps, spec, tctx, frontendJar)
}

// managedJarCookieDelivery reviews the managed jar on every hop and asks for
// authority before retained cookies cross to an unreviewed destination.
func managedJarCookieDelivery(
	ctx context.Context, deps Deps, spec requestSpec,
	jar *secretcap.CookieJar, preRunHop0 bool,
) (func(context.Context, outboundhttp.HopRequest) error, error) {
	reviewed := make(map[httpcookies.Cookie]map[string]bool)
	current := make(map[httpcookies.Cookie]bool)
	initialIssuer := cookieHopIssuer(spec)
	jar.Store.SetDelivery(initialIssuer, func(cookie httpcookies.Cookie) bool { return current[cookie] })
	beforeHop := func(hopCtx context.Context, hop outboundhttp.HopRequest) error {
		if _, err := deps.Jars.RefreshCookieJar(hopCtx, jar); err != nil {
			return cookieJarOpenReject(jar.Name, err)
		}
		target := hop.URL
		hopSpec := spec.forHop(hop)
		hopIssuer := cookieHopIssuer(hopSpec)
		destination, label := requestDestination(hopSpec)
		for {
			selected := jar.Store.Selected(target)
			var foreign []httpcookies.Cookie
			current = make(map[httpcookies.Cookie]bool, len(selected))
			jar.Store.SetDelivery(hopIssuer, func(cookie httpcookies.Cookie) bool { return current[cookie] })
			for _, cookie := range selected {
				// A matching issuer keeps the selected cookie on its reviewed route.
				if cookie.Value == "" || cookie.NativeIssuer == hopIssuer {
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
			if deps.SecretAsk == nil || deps.SecretMatcher == nil {
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
			// The request can continue without a withheld cookie.
			allowed := resolution.Decision == secretmatch.SendUnchanged
			for _, cookie := range foreign {
				if reviewed[cookie] == nil {
					reviewed[cookie] = make(map[string]bool)
				}
				reviewed[cookie][destination], current[cookie] = allowed, allowed
			}
			reloaded, err := deps.Jars.RefreshCookieJar(hopCtx, jar)
			if err != nil {
				return cookieJarOpenReject(jar.Name, err)
			}
			if !reloaded {
				return nil
			}
		}
	}
	if preRunHop0 {
		if err := beforeHop(ctx, outboundhttp.HopRequest{URL: spec.target, Method: spec.method, HostHeader: spec.hostHeader}); err != nil {
			return nil, err
		}
	}
	return beforeHop, nil
}
