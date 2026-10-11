package promptadmin

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/api/secretview"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Submission) ingestPromptSecrets(
	ctx context.Context,
	sess *wire.Session,
	refs []wire.PromptSecretReferencePart,
) (string, []wire.MessageContentPart, error) {
	lines, err := secretview.ReferenceLines(s.Store, s.ManagedSecrets, ctx, sess, refs)
	if err != nil || len(lines) == 0 {
		return "", nil, err
	}
	parts := []wire.MessageContentPart{{
		Content: "The user selected the following managed secrets. Use only these references in supported outbound tool arguments; never request or expose their protected values.",
		Origin:  wire.MessageOriginHost, Authority: wire.ContentAuthoritySystem,
		TrustTier: wire.ContentTrustTierTrusted, Source: "managed-secret-selection",
	}}
	allLines := []string{parts[0].Content}
	for _, line := range lines {
		allLines = append(allLines, line)
		parts = append(parts, wire.MessageContentPart{
			Content: line, Origin: wire.MessageOriginUser, Authority: wire.ContentAuthorityUser,
			TrustTier: wire.ContentTrustTierTrusted, Source: "managed-secret-selection",
		})
	}
	return strings.Join(allLines, "\n"), parts, nil
}
