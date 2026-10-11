package promptadmin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type promptAttachmentRetentionSet struct {
	projectID   string
	operationID string
	blobIDs     []string
}

func (s *Attachments) capturePromptAttachmentRetentions(ctx context.Context, operationID string) (promptAttachmentRetentionSet, error) {
	retentions := promptAttachmentRetentionSet{operationID: strings.TrimSpace(operationID)}
	if retentions.operationID == "" {
		return retentions, fmt.Errorf("prompt attachment operation id required")
	}
	claims, err := s.Store.ListOperationPromptAttachmentRetentions(ctx, retentions.operationID)
	if err != nil {
		return retentions, err
	}
	seen := make(map[string]struct{})
	for _, claim := range claims {
		projectID := strings.TrimSpace(claim.ProjectID)
		blobID := strings.TrimSpace(claim.BlobID)
		if projectID == "" || blobID == "" {
			return retentions, fmt.Errorf("invalid prompt attachment retention")
		}
		if retentions.projectID != "" && retentions.projectID != projectID {
			return retentions, fmt.Errorf("prompt attachment operation spans projects")
		}
		retentions.projectID = projectID
		if _, duplicate := seen[blobID]; duplicate {
			continue
		}
		seen[blobID] = struct{}{}
		retentions.blobIDs = append(retentions.blobIDs, blobID)
	}
	return retentions, nil
}

func (s *Attachments) reconcilePromptAttachmentRetentions(ctx context.Context, retentions promptAttachmentRetentionSet) error {
	if len(retentions.blobIDs) == 0 {
		return nil
	}
	current, err := s.capturePromptAttachmentRetentions(ctx, retentions.operationID)
	if err != nil {
		return err
	}
	if current.projectID != "" && current.projectID != retentions.projectID {
		return fmt.Errorf("prompt attachment operation changed projects")
	}
	keep := make(map[string]struct{}, len(current.blobIDs))
	for _, id := range current.blobIDs {
		keep[id] = struct{}{}
	}
	store, available := s.AttachmentStore(ctx, retentions.projectID)
	if !available {
		return fmt.Errorf("project %s has attachment retentions but no attachment store", retentions.projectID)
	}
	releaseIDs := make([]string, 0, len(retentions.blobIDs))
	for _, id := range retentions.blobIDs {
		if _, retained := keep[id]; !retained {
			releaseIDs = append(releaseIDs, id)
		}
	}
	if err := store.Release(retentions.operationID, retentions.blobIDs); err != nil {
		return err
	}
	var discardErr error
	for _, id := range releaseIDs {
		_, err := store.DiscardStagedBefore(id, time.Time{})
		discardErr = errors.Join(discardErr, err)
	}
	return discardErr
}
