package search

import (
	"crypto/sha256"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

var searchHitNamespace = uuid.MustParse("ab4f302f-bd2d-4a27-a29a-cc61356dfb46")

// stableHitID hashes length-framed source coordinates into a UUID.
func stableHitID(parts ...string) string {
	var framed strings.Builder
	for _, part := range parts {
		value := part
		framed.WriteString(strconv.Itoa(len(value)))
		framed.WriteByte(':')
		framed.WriteString(value)
	}
	return uuid.NewHash(sha256.New(), searchHitNamespace, []byte(framed.String()), 8).String()
}
