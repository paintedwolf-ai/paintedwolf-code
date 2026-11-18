package httpaction

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

var jarReference = regexp.MustCompile(`\{\{(cookie|token):([^{}:]+)\}\}`)

// JarSlots contains only inline templates admitted in the command arguments.
type JarSlots struct {
	Headers []outboundhttp.Header
	Body    *string
}

func (slots JarSlots) templates(req outboundRequest) outboundRequest {
	var selected outboundRequest
	selected.headers = make([]outboundhttp.Header, len(slots.Headers))
	for i, h := range slots.Headers {
		if i < len(req.headers) && h == req.headers[i] {
			selected.headers[i] = h
		}
	}
	if slots.Body != nil && *slots.Body == string(req.body) {
		selected.body = req.body
	}
	return selected
}

func placeJarSlots(ctx context.Context, deps Deps, tc tools.ToolContext, spec requestSpec, screened, templates outboundRequest, cookies *secretcap.CookieJar, tokens *secretcap.TokenJar) (outboundRequest, error) {
	if len(templates.headers) == 0 && len(templates.body) == 0 {
		return screened, nil
	}
	placements, reject := placedTokens(templates, tokens, spec)
	if reject != nil {
		return outboundRequest{}, reject
	}
	release, reject := releaseForeignTokens(ctx, deps, spec, tc, spec.tokenJar, placements)
	if reject != nil {
		return outboundRequest{}, reject
	}
	// Resolve both kinds from the admitted template. Never parse a released value as another reference.
	var headerCookies, bodyCookies = map[string]string{}, map[string]string{}
	replace := func(s string, selected map[string]string) (string, error) {
		var missing string
		out := jarReference.ReplaceAllStringFunc(s, func(ref string) string {
			parts := jarReference.FindStringSubmatch(ref)
			name := strings.TrimSpace(parts[2])
			if parts[1] == "cookie" {
				if cookies != nil {
					if v, ok := cookies.Store.Lookup(spec.target, name); ok {
						selected[name] = v
						return v
					}
				}
				missing = name
				return ""
			}
			for _, p := range placements {
				if name == p.name {
					if p.foreign && release.Decision != secretmatch.SendUnchanged {
						return secretmatch.RedactedMarker
					}
					return p.token.Value
				}
			}
			return ref
		})
		if missing != "" {
			return "", &toolrejection.ToolReject{Code: CookieNotHeldCode, Data: map[string]any{"missing": []string{missing}}}
		}
		return out, nil
	}
	out := screened
	out.headers = slices.Clone(screened.headers)
	for i, h := range templates.headers {
		if h.Name == "" {
			continue
		}
		value, err := replace(h.Value, headerCookies)
		if err != nil {
			return outboundRequest{}, err
		}
		out.headers[i].Value = value
	}
	if err := outboundhttp.ValidateHeaders(out.headers); err != nil {
		return outboundRequest{}, invalid(err)
	}
	if templates.body != nil {
		value, err := replace(string(templates.body), bodyCookies)
		if err != nil {
			return outboundRequest{}, err
		}
		out.body = []byte(value)
	}
	if len(headerCookies) > 0 || len(bodyCookies) > 0 {
		out.validateJarSlots = func(hop outboundhttp.HopRequest) error {
			check := func(values map[string]string) error {
				for name, expected := range values {
					got, ok := cookies.Store.Lookup(hop.URL, name)
					if !ok || got != expected {
						return &toolrejection.ToolReject{Code: CookieNotHeldCode, Data: map[string]any{"jar": cookies.Name, "missing": []string{name}}}
					}
				}
				return nil
			}
			if hop.CarriesOriginHeaders {
				if err := check(headerCookies); err != nil {
					return err
				}
			}
			if hop.CarriesBody {
				return check(bodyCookies)
			}
			return nil
		}
	}
	return out, nil
}
