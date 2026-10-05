package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/pkg/api"
)

// The first page and complete recursive coverage are measured separately.
func (s *runState) measureSourceTree(ctx context.Context) error {
	base := "/v1/projects/" + s.project.ID + "/source"
	var workspace api.SourceWorkspace
	if err := s.client.request(ctx, http.MethodGet, base+"/workspace", nil, &workspace); err != nil {
		return err
	}
	if len(workspace.Roots) != 1 {
		return fmt.Errorf("source tree requires one fixture root")
	}
	started := time.Now()
	var view api.SourceTreeView
	request := api.SourceTreeViewCreate{Kind: "tree", OperationID: uuid.NewString(), ClientID: "performance", WorkspaceID: workspace.WorkspaceID,
		Intent: api.SourceTreeIntent{Disclosures: []api.SourceTreeDisclosure{{Address: api.SourceTreeAddress{RootID: workspace.Roots[0].ID, Path: "."}, Open: true, Recursive: true}}}}
	if err := s.client.request(ctx, http.MethodPost, base+"/views", request, &view); err != nil {
		return err
	}
	path := base + "/views/" + view.ID
	anchor, err := json.Marshal(api.SourceTreeAddress{RootID: workspace.Roots[0].ID, Path: "."})
	if err != nil {
		return err
	}
	var presPath string
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if presPath != "" {
			_ = s.client.request(cleanup, http.MethodDelete, presPath, nil, nil)
		}
		_ = s.client.request(cleanup, http.MethodDelete, path, nil, nil)
	}()
	painted := false
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if view.State == "failed" {
			return fmt.Errorf("source tree preparation failed: %+v", view.Failure)
		}
		if view.State == "ready" && !painted {
			var pres api.SourcePresentation
			presReq := api.SourcePresentationCreate{
				OperationID:    uuid.NewString(),
				IntentRevision: view.IntentRevision,
			}
			if err := s.client.request(ctx, http.MethodPost, path+"/presentations", presReq, &pres); err != nil {
				return err
			}
			presPath = path + "/presentations/" + pres.ID
			firstRows := presPath + "/rows?limit=100&anchor=" + base64.RawURLEncoding.EncodeToString(anchor)
			var frame api.SourceTreeFrame
			if err := s.client.request(ctx, http.MethodGet, firstRows, nil, &frame); err != nil {
				return err
			}
			if len(frame.Rows) == 0 {
				return fmt.Errorf("source tree returned no visible rows")
			}
			s.metrics.add("source.tree.first_page", elapsedMS(started))
			painted = true
		}
		if painted && view.Extent.Complete {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		if err := s.client.request(ctx, http.MethodGet, path, nil, &view); err != nil {
			return err
		}
	}
	s.metrics.add("source.tree", elapsedMS(started))
	return s.verifySourceTree(ctx, presPath, view)
}

func (s *runState) verifySourceTree(ctx context.Context, presPath string, view api.SourceTreeView) error {
	directories := make(map[string]bool)
	for offset := int64(0); offset < view.Extent.Rows; {
		var frame api.SourceTreeFrame
		query := fmt.Sprintf("/rows?offset=%d&limit=200", offset)
		if err := s.client.request(ctx, http.MethodGet, presPath+query, nil, &frame); err != nil {
			return err
		}
		if frame.ViewID != view.ID || frame.ProjectionRevision != view.ProjectionRevision || frame.Span.Start != offset || frame.Span.End <= offset || frame.Span.End > view.Extent.Rows || int64(len(frame.Rows)) != frame.Span.End-offset {
			return fmt.Errorf("source tree frame did not advance coherently")
		}
		for _, row := range frame.Rows {
			if row.Kind == "loading" || row.Kind == "error" {
				return fmt.Errorf("source tree directory %q is %s: %s", row.Address.Path, row.Kind, row.Error)
			}
			if row.Kind != "directory" {
				continue
			}
			if !row.Expanded && !row.Symlink {
				return fmt.Errorf("source tree directory %q is collapsed", row.Address.Path)
			}
			if directories[row.Address.Path] {
				return fmt.Errorf("source tree repeated directory %q", row.Address.Path)
			}
			directories[row.Address.Path] = true
		}
		offset = frame.Span.End
	}
	for index := range s.fixture.Directories {
		if !directories[fmt.Sprintf("pkg%04d", index)] {
			return fmt.Errorf("source tree omitted fixture directory %d", index)
		}
	}
	return nil
}
