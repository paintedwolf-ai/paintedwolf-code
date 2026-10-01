package hitl

import (
	"crypto/sha256"
	"encoding/base64"
	"sort"
	"strings"
)

// RootsDigest identifies the attached jail roots a grant witness pins.
func RootsDigest(roots []string) string {
	cp := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root != "" {
			cp = append(cp, root)
		}
	}
	sort.Strings(cp)
	sum := sha256.Sum256([]byte(strings.Join(cp, "\x00")))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// BoundaryWitness is the jail a lease was reviewed under.
func BoundaryWitness(c Contained) ApprovalGrantWitness {
	return ApprovalGrantWitness{
		FSJailed:    c.FSJailed,
		Egress:      strings.TrimSpace(c.Egress),
		RootsDigest: RootsDigest(c.Roots),
	}
}

// WitnessEqual reports whether two boundary witnesses describe the same jail.
func WitnessEqual(a, b ApprovalGrantWitness) bool {
	return a.FSJailed == b.FSJailed &&
		a.Egress == b.Egress &&
		a.RootsDigest == b.RootsDigest
}
