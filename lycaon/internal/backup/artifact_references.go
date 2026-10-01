package backup

import (
	"context"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/visual"
)

func validateArtifactBodies(ctx context.Context, snapshot db.DBTX, files map[string]archiveSource) error {
	rows, err := snapshot.QueryContext(ctx, `SELECT DISTINCT project_id,content_hash,byte_size,stored_size FROM artifacts WHERE deleted_at IS NULL`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var projectID, hash string
		var byteSize, storedSize int64
		if err := rows.Scan(&projectID, &hash, &byteSize, &storedSize); err != nil {
			return err
		}
		rel, err := retainedFilePath("artifact", projectID, hash)
		if err != nil {
			return err
		}
		source, ok := files[rel]
		if !ok || source.kind != fileKindRegular {
			return &InvalidError{Detail: "retained artifact is missing: " + rel}
		}
		if err := validateArtifactBody(ctx, source.path, hash, byteSize, storedSize); err != nil {
			return &InvalidError{Detail: fmt.Sprintf("retained artifact is unreadable: %s: %v", rel, err)}
		}
	}
	return rows.Err()
}

func validateArtifactBody(ctx context.Context, path, hash string, byteSize, storedSize int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || storedSize < 0 || info.Size() != storedSize {
		return fmt.Errorf("encoded artifact size differs")
	}
	return visual.VerifyArtifactBody(contextReader{ctx: ctx, in: file}, hash, byteSize)
}
