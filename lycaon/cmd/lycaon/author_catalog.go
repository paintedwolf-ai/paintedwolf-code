package main

import (
	"context"

	"github.com/lycaon/lycaon/internal/extpacks"
)

// installAuthorCatalog resolves the device catalog for author commands.
func installAuthorCatalog(ctx context.Context) {
	if _, err := extpacks.ApplyCatalog(ctx, nil, nil); err != nil {
		return
	}
}
