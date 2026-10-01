package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/lycaon/lycaon/internal/db"
)

type storeBaselineResult struct {
	Compatible         bool              `json:"compatible"`
	SchemaVersion      int               `json:"schema_version"`
	StoreSchemaVersion int               `json:"store_schema_version"`
	RecoveryReason     db.RecoveryReason `json:"recovery_reason,omitempty"`
}

func writeStoreBaseline(ctx context.Context, path string, out io.Writer) error {
	store, err := db.OpenReadOnly(ctx, path)
	if err != nil {
		return fmt.Errorf("inspect store baseline: %w", err)
	}
	defer func() { _ = store.Close() }()
	version, err := db.ReadUserVersion(ctx, store)
	if err != nil {
		return err
	}
	result := storeBaselineResult{Compatible: true, SchemaVersion: db.SchemaVersion, StoreSchemaVersion: version}
	if err := db.CheckBaseline(ctx, store); err != nil {
		var incompatible *db.StoreIncompatibleError
		if !errors.As(err, &incompatible) {
			return err
		}
		result.Compatible = false
		result.RecoveryReason = incompatible.Reason
	}
	return json.NewEncoder(out).Encode(result)
}
