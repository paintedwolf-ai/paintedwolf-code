package httpaction

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/httpcookies"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tools"
)

type cookieStoreProvider interface {
	CookieStore() *httpcookies.Store
}

type managedJarAdapter struct {
	jar *secretcap.CookieJar
}

func (a *managedJarAdapter) Cookies(u *url.URL) []*http.Cookie {
	if a == nil || a.jar == nil || a.jar.Store == nil {
		return nil
	}
	return a.jar.Store.Cookies(u)
}

func (a *managedJarAdapter) SetCookies(u *url.URL, cookies []*http.Cookie) {
	if a == nil || a.jar == nil || a.jar.Store == nil {
		return
	}
	a.jar.Store.SetCookies(u, cookies)
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

func refreshManagedCookieJar(
	ctx context.Context, deps Deps, req secretcap.CookieJarRequest, jar *secretcap.CookieJar, lastVersion *int64,
) (bool, error) {
	if deps.Secrets == nil || jar == nil || req.ProjectID == "" || req.ChatSessionID == "" || req.Name == "" {
		return false, nil
	}
	if jar.Reference != "" {
		meta, err := deps.Secrets.Describe(ctx, req.ProjectID, req.ChatSessionID, jar.Reference)
		if err != nil {
			return false, cookieJarOpenReject(jar.Name, err)
		}
		switch meta.State {
		case secretcap.StateRevoked:
			return false, cookieJarOpenReject(jar.Name, secretcap.ErrRevoked)
		case secretcap.StateAgentUseExpired:
			return false, cookieJarOpenReject(jar.Name, secretcap.ErrAgentUseExpired)
		case secretcap.StateUnavailable:
			return false, cookieJarOpenReject(jar.Name, secretcap.ErrValueMissing)
		}
		if lastVersion != nil && meta.Version == *lastVersion {
			return false, nil
		}
	} else {
		list, err := deps.Secrets.List(ctx, req.ProjectID, req.ChatSessionID)
		if err != nil {
			return false, cookieJarOpenReject(jar.Name, err)
		}
		found := false
		for _, item := range list {
			if item.Origin == secretcap.OriginCookieJar && item.Name == req.Name && item.State == secretcap.StateActive {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}
	fresh, err := deps.Secrets.OpenCookieJar(ctx, req)
	if err != nil {
		return false, cookieJarOpenReject(jar.Name, err)
	}
	jar.Reference = fresh.Reference
	if lastVersion != nil && jar.Reference != "" {
		if meta, err := deps.Secrets.Describe(ctx, req.ProjectID, req.ChatSessionID, jar.Reference); err == nil {
			*lastVersion = meta.Version
		}
	}
	if jar.Store == nil || jar.Store.Changes().Empty() {
		jar.Store = fresh.Store
	} else {
		jar.Store = jar.Store.Changes().Merge(fresh.Store.Snapshot())
	}
	return true, nil
}

// prepareCookieDelivery keeps release decisions on this invocation's opened jar.
// The HTTP client's before-hop boundary reviews newly eligible cookies as well.
func prepareCookieDelivery(ctx context.Context, deps Deps, spec requestSpec, jar *secretcap.CookieJar) (func(context.Context, outboundhttp.HopRequest) error, error) {
	return prepareRequestCookieDelivery(ctx, deps, spec, tools.ToolContext{}, jar, secretcap.CookieJarRequest{}, nil, true)
}

func prepareRequestCookieDelivery(
	ctx context.Context, deps Deps, spec requestSpec, tctx tools.ToolContext,
	jar *secretcap.CookieJar, jarReq secretcap.CookieJarRequest, frontendJar http.CookieJar, preRunHop0 bool,
) (func(context.Context, outboundhttp.HopRequest) error, error) {
	if jar != nil {
		var lastVersion int64
		if deps.Secrets != nil && jar.Reference != "" && jarReq.ProjectID != "" && jarReq.ChatSessionID != "" {
			if meta, err := deps.Secrets.Describe(ctx, jarReq.ProjectID, jarReq.ChatSessionID, jar.Reference); err == nil {
				lastVersion = meta.Version
			}
		}
		reviewed := make(map[httpcookies.Cookie]map[string]bool)
		current := make(map[httpcookies.Cookie]bool)
		initialIssuer := cookieHopIssuer(spec)
		jar.Store.SetDelivery(initialIssuer, func(cookie httpcookies.Cookie) bool { return current[cookie] })
		beforeHop := func(hopCtx context.Context, hop outboundhttp.HopRequest) error {
			if _, err := refreshManagedCookieJar(hopCtx, deps, jarReq, jar, &lastVersion); err != nil {
				return err
			}
			target := hop.URL
			hopSpec := spec
			hopSpec.target = target
			hopSpec.hostHeader = hop.HostHeader
			hopIssuer := cookieHopIssuer(hopSpec)
			destination, label := requestDestination(hopSpec)
			for {
				selected := jar.Store.Selected(target)
				var foreign []httpcookies.Cookie
				current = make(map[httpcookies.Cookie]bool, len(selected))
				jar.Store.SetDelivery(hopIssuer, func(cookie httpcookies.Cookie) bool { return current[cookie] })
				for _, cookie := range selected {
					// Ordinary URL cookies retain their RFC selection on plain routes.
					// Any retained service receipt or recipient routing override requires
					// matching issuer authority or explicit review.
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
					admitted := admittedHTTP(selected, current)
					if hop.FreezeCookies(admitted) {
						_ = jar.Store.Cookies(target)
					}
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
				allowed := resolution.Decision == secretmatch.SendUnchanged
				for _, cookie := range foreign {
					if reviewed[cookie] == nil {
						reviewed[cookie] = make(map[string]bool)
					}
					reviewed[cookie][destination], current[cookie] = allowed, allowed
				}
				reloaded, err := refreshManagedCookieJar(hopCtx, deps, jarReq, jar, &lastVersion)
				if err != nil {
					return err
				}
				if !reloaded {
					admitted := admittedHTTP(selected, current)
					if hop.FreezeCookies(admitted) {
						_ = jar.Store.Cookies(target)
					}
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
		hopSpec := spec
		hopSpec.target = target
		hopSpec.hostHeader = hop.HostHeader
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
			ContestToken:     strings.TrimSpace(spec.contest),
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
		if resolution.Decision.Blocks() {
			tctx.Effects.Secrets.Withhold(hopCtx)
		}
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
	if preRunHop0 {
		if err := beforeHop(ctx, outboundhttp.HopRequest{URL: spec.target, Method: spec.method, HostHeader: spec.hostHeader}); err != nil {
			return nil, err
		}
	}
	return beforeHop, nil
}
