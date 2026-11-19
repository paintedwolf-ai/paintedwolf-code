package httpaction

import (
	"context"
	"net/http"

	"github.com/lycaon/lycaon/internal/httpcookies"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tools"
)

type cookieStoreProvider interface {
	CookieStore() *httpcookies.Store
}

func cookieHopIssuer(spec requestSpec) string {
	if spec.socket != "" {
		return spec.socketBinding.Key()
	}
	if len(spec.recipientOverrides()) > 0 {
		id, _ := requestDestination(spec)
		return id
	}
	return ""
}

func admittedHTTP(selected []httpcookies.Cookie, allowed map[httpcookies.Cookie]bool) []*http.Cookie {
	out := make([]*http.Cookie, 0, len(selected))
	for _, c := range selected {
		if !allowed[c] {
			continue
		}
		out = append(out, &http.Cookie{ //nolint:gosec // G124 grades response attributes; a request Cookie header carries none
			Name: c.Name, Value: c.Value, Secure: c.Secure, HttpOnly: c.HTTPOnly,
		})
	}
	return out
}

// frontendJarCookieDelivery screens a caller-supplied cookie jar's store before
// delivery and asks for authority over matched secret-shaped cookies.
func frontendJarCookieDelivery(
	ctx context.Context, deps Deps, spec requestSpec, tctx tools.ToolContext, frontendJar http.CookieJar,
) (func(context.Context, outboundhttp.HopRequest) error, error) {
	if frontendJar == nil {
		return nil, nil
	}
	provider, ok := frontendJar.(cookieStoreProvider)
	if !ok || provider == nil {
		return nil, screenFaultReject(secretmatch.FaultStageScreenUnwired)
	}
	store := provider.CookieStore()
	if store == nil {
		return nil, screenFaultReject(secretmatch.FaultStageScreenUnwired)
	}
	reviewed := make(map[httpcookies.Cookie]map[string]bool)
	current := make(map[httpcookies.Cookie]bool)
	store.SetDelivery("", func(cookie httpcookies.Cookie) bool { return current[cookie] })
	beforeHop := func(hopCtx context.Context, hop outboundhttp.HopRequest) error {
		target := hop.URL
		hopSpec := spec.forHop(hop)
		hopIssuer := cookieHopIssuer(hopSpec)
		destination, label := requestDestination(hopSpec)
		selected := store.Selected(target)
		current = make(map[httpcookies.Cookie]bool, len(selected))
		store.SetDelivery(hopIssuer, func(cookie httpcookies.Cookie) bool { return current[cookie] })
		if len(selected) == 0 || deps.SecretMatcher == nil {
			for _, cookie := range selected {
				current[cookie] = true
			}
			admitted := admittedHTTP(selected, current)
			if hop.FreezeCookies(admitted) {
				_ = store.Cookies(target)
			}
			return nil
		}
		classification := deps.SecretMatcher.ClassificationSnapshot(hopCtx)
		screenCtx := deps.SecretMatcher.WithClassifications(hopCtx, classification)
		var matchedCookies []httpcookies.Cookie
		var matches []secretmatch.Match
		reviewValue := ""
		for _, cookie := range selected {
			if cookie.Value == "" {
				current[cookie] = true
				continue
			}
			wireCookie := cookie.Name + "=" + cookie.Value
			hits := deps.SecretMatcher.ScreenLabeledContext(screenCtx, "Cookie", wireCookie)
			if len(hits) == 0 {
				current[cookie] = true
				continue
			}
			if decisions := reviewed[cookie]; decisions != nil {
				if allowed, ok := decisions[destination]; ok {
					current[cookie] = allowed
					continue
				}
			}
			matchedCookies = append(matchedCookies, cookie)
			matches = secretmatch.MergeEvidence(matches, hits)
			if reviewValue == "" {
				reviewValue = secretmatch.ReviewValue(wireCookie, hits[0])
			}
		}
		if len(matchedCookies) == 0 {
			admitted := admittedHTTP(selected, current)
			if hop.FreezeCookies(admitted) {
				_ = store.Cookies(target)
			}
			return nil
		}
		if deps.SecretAsk == nil || len(matches) == 0 {
			return screenFaultReject(secretmatch.FaultStageScreenUnwired)
		}
		match := primaryMatch(matches)
		resolution, err := deps.SecretAsk(hopCtx, secretmatch.Alert{
			ReviewValue:      reviewValue,
			Surface:          secretmatch.SurfaceHTTPRequest,
			DestinationID:    destination,
			DestinationLabel: label,
			SecretNames:      secretmatch.ManagedNames(matches),
			RuleID:           match.RuleID,
			RuleTitle:        match.Title,
			GenericShape:     match.GenericShape,
			Occurrences:      len(matches),
			SourceKind:       secretmatch.SourceToolArgument,
			SourceTool:       "http_request",
			SourcePath:       "cookies",
			OriginKind:       secretmatch.OriginFile,
			Fingerprints:     secretmatch.Fingerprints(matches),
			VarName:          match.VarName,
			Container:        match.Container,
			RecipientsLocal:  hopSpec.dialsLoopback(),
		})
		if err != nil {
			stage := secretmatch.FaultStageRaise
			if fault, ok := secretmatch.Faulted(err); ok {
				stage = fault.Stage
			}
			return screenFaultReject(stage)
		}
		if err := deps.SecretMatcher.CheckClassification(hopCtx, classification); err != nil {
			return screenFaultReject(secretmatch.FaultStageRaise)
		}
		// Withhold/redaction on a cookie jar withholds only the denied cookie bytes
		// so the HTTP hop can proceed credential-free.
		allowed := resolution.Decision == secretmatch.SendUnchanged
		for _, cookie := range matchedCookies {
			if reviewed[cookie] == nil {
				reviewed[cookie] = make(map[string]bool)
			}
			reviewed[cookie][destination], current[cookie] = allowed, allowed
		}
		admitted := admittedHTTP(selected, current)
		if hop.FreezeCookies(admitted) {
			_ = store.Cookies(target)
		}
		return nil
	}
	return beforeHop, nil
}
