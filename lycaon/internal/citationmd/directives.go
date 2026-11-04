package citationmd

import (
	"strings"

	"github.com/lycaon/lycaon/internal/verification"
)

// verifySeparators lists the method/reason separators, longest first.
var verifySeparators = []string{"--", "-", "—", "–", ":"}

var reasonUnescaper = strings.NewReplacer(`\[`, `[`, `\]`, `]`)

func parseVerify(body string) parsedNode {
	bad := parsedNode{issue: IssueInvalidVerification, kind: KindVerify}
	if !strings.HasPrefix(body, "verify:") {
		return bad
	}
	rest := strings.TrimLeft(body[len("verify:"):], " \t")
	methodEnd := 0
	for methodEnd < len(rest) && rest[methodEnd] >= 'a' && rest[methodEnd] <= 'z' {
		methodEnd++
	}
	method := rest[:methodEnd]
	rest = strings.TrimLeft(rest[methodEnd:], " \t")
	found := false
	for _, sep := range verifySeparators {
		if strings.HasPrefix(rest, sep) {
			rest = rest[len(sep):]
			found = true
			break
		}
	}
	assessment := verification.Assessment{Method: method, Reason: reasonUnescaper.Replace(strings.TrimSpace(rest))}
	if !found || !assessment.Valid() {
		return bad
	}
	return parsedNode{kind: KindVerify, members: []parsedMember{{kind: KindVerify, verify: &assessment}}}
}

func parseArtifact(body string) parsedNode {
	node := parsedNode{kind: KindArtifact}
	if !strings.HasPrefix(body, "artifact:") {
		return parsedNode{issue: IssueMalformedArtifact, kind: KindArtifact}
	}
	offset := 1 + len("artifact:")
	for _, raw := range strings.Split(body[len("artifact:"):], ",") {
		id := strings.TrimSpace(raw)
		if !artifactID.MatchString(id) {
			return parsedNode{issue: IssueMalformedArtifact, kind: KindArtifact}
		}
		start := offset + strings.Index(raw, id)
		node.members = append(node.members, parsedMember{kind: KindArtifact, start: start, end: start + len(id), artifact: id})
		offset += len(raw) + 1
	}
	return node
}

func parseRepair(body string) parsedNode {
	bad := parsedNode{issue: IssueMalformedRepair, kind: KindRepair}
	target, value, ok := strings.Cut(strings.TrimPrefix(body, "cite:"), "=>")
	target = strings.TrimSpace(target)
	value = strings.TrimSpace(value)
	if !ok || !strings.HasPrefix(body, "cite:") || !repairTarget.MatchString(target) || value == "" {
		return bad
	}
	repair := &Repair{Target: target}
	if strings.HasPrefix(value, "<") && strings.HasSuffix(value, ">") {
		repair.URL = value[1 : len(value)-1]
		if !isWebDestination(repair.URL) {
			return bad
		}
	} else {
		r := memberReader{body: value}
		citation, code := r.member()
		if code != "" || !r.done() {
			return bad
		}
		repair.Citation = citation
	}
	return parsedNode{kind: KindRepair, members: []parsedMember{{kind: KindRepair, repair: repair}}}
}
