package page

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// resolveDriveFixtures reads each route's body_path inside the caller's workspace and
// validates every route table a drive installs: the page's own and each route action's.
func resolveDriveFixtures(ctx context.Context, tctx tools.ToolContext, routes []browser.RouteRule, actions []browser.CaptureAction) error {
	if err := resolveRouteBodies(ctx, tctx, routes); err != nil {
		return err
	}
	if err := browser.ValidateRoutes(routes); err != nil {
		return mapBrowserReject(err)
	}
	for i := range actions {
		if !strings.EqualFold(strings.TrimSpace(actions[i].Type), "route") {
			continue
		}
		if err := resolveRouteBodies(ctx, tctx, actions[i].Routes); err != nil {
			return err
		}
		if err := browser.ValidateRoutes(actions[i].Routes); err != nil {
			return mapBrowserReject(err)
		}
	}
	return nil
}

func resolveRouteBodies(ctx context.Context, tctx tools.ToolContext, routes []browser.RouteRule) error {
	for i := range routes {
		modelPath := strings.TrimSpace(routes[i].BodyPath)
		if modelPath == "" {
			continue
		}
		invalid := func(reason string) error {
			return &toolrejection.ToolReject{Code: "CAPTURE_ROUTE_INVALID", Data: map[string]any{
				"reason": reason, "route": i, "route_url": routes[i].URL, "body_path": modelPath,
			}}
		}
		resolved, err := projectpaths.ResolveRead(ctx, nil, tctx, modelPath)
		if err != nil {
			return invalid("body_path_outside_workspace")
		}
		body, err := resolved.ReadBounded(browser.MaxRouteBodyBytes)
		var tooLarge *projectpaths.FileTooLargeError
		switch {
		case errors.As(err, &tooLarge):
			return invalid("body_too_large")
		case errors.Is(err, os.ErrNotExist), errors.Is(err, projectpaths.ErrIsDirectory):
			return invalid("body_path_unreadable")
		case err != nil:
			return err
		}
		routes[i].BodyBytes = body
		if strings.TrimSpace(routes[i].ContentType) == "" {
			routes[i].ContentType = mime.TypeByExtension(filepath.Ext(resolved.DisplayPath))
		}
	}
	return nil
}
