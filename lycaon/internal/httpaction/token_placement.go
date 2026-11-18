package httpaction

import (
	"strings"

	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/toolrejection"
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
