package commandsurface

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ValidatedProgramIdentity exposes bounded immutable identity to host admission.
// Only a live execution plan carrying retained-state admission qualifies. This
// returns no syntax/source copy, grants no authority and performs no progress
// update. The caller keeps the program lease live through admission/application.
func (p *ParsedProgram) ValidatedProgramIdentity(ctx context.Context) (sourceDigest, catalogDigest string, err error) {
	if ctx == nil || p == nil {
		return "", "", fmt.Errorf("validated program identity requires context")
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed || p.releaseState == nil || p.plan.Version != toolcontract.LanguageVersion || p.plan.CatalogDigest != toolcontract.LanguageCatalogDigest() || len(p.plan.SourceDigest) != 64 {
		return "", "", fmt.Errorf("validated execution program is unavailable")
	}
	return p.plan.SourceDigest, p.plan.CatalogDigest, nil
}
