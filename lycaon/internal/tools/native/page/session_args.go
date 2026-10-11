package page

import (
	"encoding/json"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

func parsePageOpenArgs(args map[string]any) (pageOpenArgs, error) {
	raw, err := surveyjson.Marshal(args)
	if err != nil {
		return pageOpenArgs{}, err
	}
	var in pageOpenArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return pageOpenArgs{}, err
	}
	in.URL = strings.TrimSpace(in.URL)
	in.ProjectDir = strings.TrimSpace(in.ProjectDir)
	in.Path = strings.TrimSpace(in.Path)
	in.ProcessHandle = strings.TrimSpace(in.ProcessHandle)
	in.Wait = strings.TrimSpace(in.Wait)
	if in.URL == "" && in.ProjectDir == "" {
		return pageOpenArgs{}, &toolrejection.ToolReject{Code: "CAPTURE_TARGET_INVALID", Data: map[string]any{"reason": "missing_target"}}
	}
	if in.URL != "" && in.ProjectDir != "" {
		return pageOpenArgs{}, &toolrejection.ToolReject{Code: "CAPTURE_TARGET_INVALID", Data: map[string]any{"reason": "url_and_project_dir"}}
	}
	if in.Path != "" && in.ProjectDir == "" {
		return pageOpenArgs{}, &toolrejection.ToolReject{Code: "CAPTURE_TARGET_INVALID", Data: map[string]any{"reason": "path_without_project_dir"}}
	}
	if in.ProcessHandle != "" && in.ProjectDir != "" {
		return pageOpenArgs{}, &toolrejection.ToolReject{Code: "CAPTURE_TARGET_INVALID", Data: map[string]any{"reason": "process_handle_with_project_dir"}}
	}
	if in.Path != "" {
		if _, err := browser.JoinStaticEntry(in.Path); err != nil {
			return pageOpenArgs{}, mapBrowserReject(err)
		}
	}
	return in, nil
}

func parsePageActArgs(args map[string]any) (pageActArgs, error) {
	raw, err := surveyjson.Marshal(args)
	if err != nil {
		return pageActArgs{}, err
	}
	var in pageActArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return pageActArgs{}, err
	}
	in.ID = strings.TrimSpace(in.ID)
	if in.ID == "" {
		return pageActArgs{}, &toolrejection.ToolReject{Code: "PAGE_ID_REQUIRED", Data: map[string]any{"reason": "missing_id"}}
	}
	if len(in.Actions) == 0 {
		return pageActArgs{}, &toolrejection.ToolReject{Code: "CAPTURE_TARGET_INVALID", Data: map[string]any{"reason": "missing_actions", "capture_actions_required": true}}
	}
	if in.Record != nil {
		if err := browser.ValidateRecordOpts(*in.Record); err != nil {
			return pageActArgs{}, mapBrowserReject(err)
		}
	}
	return in, nil
}

func parsePageSnapshotArgs(args map[string]any) (pageSnapshotArgs, error) {
	raw, err := surveyjson.Marshal(args)
	if err != nil {
		return pageSnapshotArgs{}, err
	}
	var in pageSnapshotArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return pageSnapshotArgs{}, err
	}
	in.ID = strings.TrimSpace(in.ID)
	in.Selector = strings.TrimSpace(in.Selector)
	in.Caption = strings.TrimSpace(in.Caption)
	if in.ID == "" {
		return pageSnapshotArgs{}, &toolrejection.ToolReject{Code: "PAGE_ID_REQUIRED", Data: map[string]any{"reason": "missing_id"}}
	}
	return in, nil
}

func parsePageCloseArgs(args map[string]any) (pageCloseArgs, error) {
	raw, err := surveyjson.Marshal(args)
	if err != nil {
		return pageCloseArgs{}, err
	}
	var in pageCloseArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return pageCloseArgs{}, err
	}
	in.ID = strings.TrimSpace(in.ID)
	in.Selector = strings.TrimSpace(in.Selector)
	in.Caption = strings.TrimSpace(in.Caption)
	if in.ID == "" {
		return pageCloseArgs{}, &toolrejection.ToolReject{Code: "PAGE_ID_REQUIRED", Data: map[string]any{"reason": "missing_id"}}
	}
	return in, nil
}
