package httpaction

import (
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"net/url"
	"strings"
)

// tokenPlacement is one {{token:name}} reference in the request and the
// token it names.
type tokenPlacement struct {
	name  string
	token secretcap.Token
	// foreign marks a token issued by a service other than the request's
	// destination, so placing it discloses the credential to a new recipient.
	foreign bool
}

// referencedTokenNames lists the names the request references, once each.
func referencedTokenNames(req outboundRequest) []string {
	var names []string
	collect := func(in string) {
		for _, match := range tokenReference.FindAllStringSubmatch(in, -1) {
			names = append(names, strings.TrimSpace(match[1]))
		}
	}
	collect(string(req.body))
	for _, header := range req.headers {
		collect(header.Value)
	}
	return sortedUnique(names)
}

// placedTokens resolves every reference against the jar and marks the ones
// whose issuer is not this request's destination. An unheld name is refused
// rather than sent literally.
func placedTokens(req outboundRequest, jar *secretcap.TokenJar, spec requestSpec) ([]tokenPlacement, *toolrejection.ToolReject) {
	destinationID, _ := requestDestination(spec)
	foreign := len(spec.recipientOverrides()) > 0
	names := referencedTokenNames(req)
	if len(names) == 0 {
		return nil, nil
	}
	if jar == nil {
		return nil, invalid(errTokenReferenceNeedsJar)
	}
	var missing []string
	placements := make([]tokenPlacement, 0, len(names))
	for _, name := range names {
		token, ok := jar.Lookup(name)
		if !ok {
			missing = append(missing, name)
			continue
		}
		placements = append(placements, tokenPlacement{name: name, token: token, foreign: foreign || token.Origin != destinationID})
	}
	if len(missing) > 0 {
		return nil, &toolrejection.ToolReject{
			Code: TokenNotHeldCode,
			Data: map[string]any{"jar": jar.Name, "missing": missing, "held": jar.Names()},
		}
	}
	return placements, nil
}

// substituteTokens writes each placed token onto the wire. A foreign token
// the screen released redacted is written as the marker instead, and the
// request then carries the redaction receipt.
func substituteTokens(req outboundRequest, placements []tokenPlacement, release secretmatch.Resolution) (outboundRequest, *toolrejection.ToolReject) {
	if len(placements) == 0 {
		return req, nil
	}
	spelling := make(map[string]string, len(placements))
	redacted := false
	for _, placement := range placements {
		spelling[placement.name] = placement.token.Value
		if placement.foreign && release.Decision == secretmatch.SendRedacted {
			spelling[placement.name] = secretmatch.RedactedMarker
			redacted = true
		}
	}
	substitute := func(in string) string {
		return tokenReference.ReplaceAllStringFunc(in, func(match string) string {
			return spelling[strings.TrimSpace(tokenReference.FindStringSubmatch(match)[1])]
		})
	}
	out := req
	out.headers = make([]outboundhttp.Header, len(req.headers))
	out.body = []byte(substitute(string(req.body)))
	out.redacted = req.redacted || redacted

	if redacted && out.receipt == "" {
		out.receipt = release.ReceiptToken
	}
	for i, header := range req.headers {
		out.headers[i] = outboundhttp.Header{Name: header.Name, Value: substitute(header.Value)}
	}
	if err := outboundhttp.ValidateHeaders(out.headers); err != nil {
		return outboundRequest{}, invalid(err)
	}
	return out, nil
}

// responseIssuer identifies the service whose response carried a captured
// token: the reviewed socket, or the origin the exchange ended at.
func responseIssuer(spec requestSpec, resp outboundhttp.Response) (id, label string) {
	if spec.socket != "" || len(spec.recipientOverrides()) > 0 {
		return requestDestination(spec)
	}
	if final, err := url.Parse(strings.TrimSpace(resp.FinalURL)); err == nil && final.Hostname() != "" {
		return secretmatch.HTTPDestination(final)
	}
	return secretmatch.HTTPDestination(spec.target)
}
