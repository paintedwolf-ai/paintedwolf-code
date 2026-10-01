package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/pkg/api"
)

type WorkerTaskPreambleData struct {
	TouchPaths  []string
	ScopeMode   string
	ScopePaths  []string
	ScopeAbsent bool
}

func BuildWorkerTaskPreambleData(projectDir string, touchPaths []string, scope *api.TaskScope) WorkerTaskPreambleData {
	touchPaths = session.FilterTouchPathsForProject(projectDir, touchPaths)
	data := WorkerTaskPreambleData{TouchPaths: touchPaths}
	if scope == nil {
		return data
	}
	norm := scope.Normalized()
	if norm.Mode == api.TaskScopeModeRead && len(norm.Paths) == 0 {
		return data
	}
	data.ScopeMode = string(norm.Mode)
	data.ScopePaths = append([]string(nil), norm.Paths...)
	if norm.Mode == api.TaskScopeModeWrite {
		data.ScopeAbsent = scopeAbsentOnDisk(projectDir, norm.Paths)
	}
	return data
}

// scopeAbsentOnDisk reports whether every concrete suggestion is absent.
func scopeAbsentOnDisk(projectDir string, paths []string) bool {
	if strings.TrimSpace(projectDir) == "" {
		return false
	}
	checkedAny := false
	for _, p := range paths {
		base := globBase(p)
		if base == "" || base == "." {
			continue
		}
		checkedAny = true
		if _, err := os.Stat(filepath.Join(projectDir, filepath.FromSlash(base))); err == nil {
			return false
		}
	}
	return checkedAny
}

// globBase returns path segments before the first wildcard.
func globBase(p string) string {
	p = strings.TrimSpace(p)
	var keep []string
	for _, seg := range strings.Split(p, "/") {
		if strings.ContainsAny(seg, "*?[") {
			break
		}
		keep = append(keep, seg)
	}
	return strings.Trim(strings.Join(keep, "/"), "/")
}

func (d WorkerTaskPreambleData) empty() bool {
	return len(d.TouchPaths) == 0 && strings.TrimSpace(d.ScopeMode) == ""
}

func WorkerTaskPreambleToMap(data WorkerTaskPreambleData) map[string]any {
	return map[string]any{
		"touch_paths":  data.TouchPaths,
		"scope_mode":   data.ScopeMode,
		"scope_paths":  data.ScopePaths,
		"scope_absent": data.ScopeAbsent,
	}
}

func RenderWorkerTaskPreamble(
	ctx context.Context,
	renderer *prompts.InjectRenderer,
	sessionID string,
	projectDir string,
	touchPaths []string,
	scope *api.TaskScope,
) (string, error) {
	if renderer == nil {
		return "", fmt.Errorf("inject renderer not configured")
	}
	data := BuildWorkerTaskPreambleData(projectDir, touchPaths, scope)
	if data.empty() {
		return "", nil
	}
	block, err := anchor.RenderInform(ctx, anchor.InjectWorkerTaskPreamble, anchor.MatchContext{Surface: "worker", SessionID: sessionID}, renderer, WorkerTaskPreambleToMap(data))
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" {
		return "", nil
	}
	if !strings.Contains(block, guidance.MarkerWorkerTaskPreamble) {
		return "", fmt.Errorf("worker-task-preamble inject missing sentinel %q", guidance.MarkerWorkerTaskPreamble)
	}
	return block, nil
}

func JoinWorkerAssignment(preamble, assignment string) string {
	preamble = strings.TrimSpace(preamble)
	assignment = strings.TrimSpace(assignment)
	switch {
	case preamble == "":
		return assignment
	case assignment == "":
		return preamble
	default:
		return preamble + "\n\n" + assignment
	}
}

func BuildWorkerPrompt(
	ctx context.Context,
	renderer *prompts.InjectRenderer,
	sessionID string,
	projectDir string,
	touchPaths []string,
	scope *api.TaskScope,
	assignment string,
) (string, error) {
	preamble, err := RenderWorkerTaskPreamble(ctx, renderer, sessionID, projectDir, touchPaths, scope)
	if err != nil {
		return "", err
	}
	return JoinWorkerAssignment(preamble, assignment), nil
}
