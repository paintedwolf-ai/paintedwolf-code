package main

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/scalefixture"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/pkg/api"
)

// historyOperationFiles is how many files one recorded agent operation creates.
const historyOperationFiles = 1_000

var historyNamespace = uuid.MustParse("7f3c2a6e-5d1b-4f0e-9a8c-25b1e7d40c19")

// recordProjectHistory writes, through the engine's source ledger, the history
// an agent leaves by creating every file of the project: what a project owns
// after use, and what deleting it removes.
func recordProjectHistory(ctx context.Context, configDir, projectID, sessionID string, files int) (err error) {
	store, err := db.OpenWithOptions(ctx, filepath.Join(configDir, "store.db"), db.UpgradeHooks{}, db.StoreOptions{})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, store.Shutdown(context.WithoutCancel(ctx))) }()
	var rootID string
	if err := store.QueryRowContext(ctx, `SELECT id FROM project_roots WHERE project_id = ?`, projectID).Scan(&rootID); err != nil {
		return err
	}
	ledger := sourceledger.New(store, filepath.Join(configDir, enginepaths.SourceContentDirName))
	defer func() { err = errors.Join(err, ledger.Snapshots.Close()) }()
	for start := 0; start < files; start += historyOperationFiles {
		operation := uuid.NewSHA1(historyNamespace, []byte(projectID+"\x00"+strconv.Itoa(start))).String()
		inputs := make([]sourceledger.RecordInput, 0, historyOperationFiles)
		for index := start; index < min(start+historyOperationFiles, files); index++ {
			inputs = append(inputs, sourceledger.RecordInput{
				RecordLocation: sourceledger.RecordLocation{RootID: rootID, Path: scalefixture.Path(index)},
				ProjectID:      projectID, Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent,
				SessionID: sessionID, OperationID: operation, After: scalefixture.Body(index),
			})
		}
		if err := ledger.RecordBatch(ctx, inputs); err != nil {
			return err
		}
	}
	return nil
}
