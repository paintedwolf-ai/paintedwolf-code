package sessions

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/credentialstore"
)

func (r *Runtime) wireCredentialFiles(ctx context.Context, deps CheckpointDependencies) error {
	if deps.Security.Harvest == nil {
		return nil
	}
	values, err := credentialstore.Open(credentialstore.Slot{Path: credentialstore.DefaultVaultPath(deps.Directory), Namespace: credentialstore.NamespaceCredentialFiles, Context: "credential-file evidence"}, nil)
	if err != nil {
		return fmt.Errorf("credential-file evidence: %w", err)
	}
	if err := deps.Security.Harvest.RestoreFiles(values); err != nil {
		return err
	}
	files := newCredentialFiles(deps.Security.Harvest, deps.Security.Fingerprinter, deps.Database, deps.Security.Capabilities, deps.Sessions.Evidence)
	files.startCredentialIndex()
	deps.Resources.Track("credential-file-index", 23, files.closeCredentialIndex)
	r.Manager.ToolContext.CredentialFiles = files
	deps.Security.CredentialFiles = files
	return nil
}
