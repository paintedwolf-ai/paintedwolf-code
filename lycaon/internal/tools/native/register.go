package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/browser/renderhandle"
	"github.com/lycaon/lycaon/internal/findings"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/page"
	reporttools "github.com/lycaon/lycaon/internal/tools/native/reporting"
	"github.com/lycaon/lycaon/internal/tools/native/terminal"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// RegisterCapturePageTool registers capture_page for driving a user's web app.
func RegisterCapturePageTool(reg *tools.DefaultRegistry, pool *browser.Pool, bg *bgprocess.Registry, live page.LivePreview) error {
	if reg == nil {
		return fmt.Errorf("registry required")
	}
	if pool == nil {
		return fmt.Errorf("browser pool required")
	}
	return reg.Register(page.CaptureToolName, page.CaptureHandler(pool, bg, live))
}

// RegisterMeasurePageTool registers measure_page for render-tree geometry probes.
func RegisterMeasurePageTool(reg *tools.DefaultRegistry, pool *browser.Pool, pages *pagesession.Registry, bg *bgprocess.Registry) error {
	if reg == nil {
		return fmt.Errorf("registry required")
	}
	if pool == nil {
		return fmt.Errorf("browser pool required")
	}
	if pages == nil {
		return fmt.Errorf("page registry required")
	}
	return reg.Register(page.MeasureToolName, page.MeasureHandler(pool, pages, bg))
}

// scopedWriter writes a tool's destination file through the host write door.
func scopedWriter(boundary *sandbox.Boundary, toolName string) func(ctx context.Context, tctx tools.ToolContext, relPath string, data []byte) error {
	return func(ctx context.Context, tctx tools.ToolContext, relPath string, data []byte) error {
		if err := assertProfileWriteScope(ctx, boundary, tctx, relPath, toolName); err != nil {
			return writeScopeReject(ctx, boundary, relPath, tctx.ProfileID(), toolName, err)
		}
		if err := beforeWorkerMutation(ctx, tctx, relPath); err != nil {
			return err
		}
		resolved, err := projectpaths.ResolveWrite(ctx, boundary, tctx, relPath)
		if err != nil {
			return err
		}
		var before []byte
		if f, err := fseffect.OpenRead(resolved.EffectLocation()); err == nil {
			before, _ = io.ReadAll(f)
			_ = f.Close()
		}
		target := resolvedMutationTarget(resolved)
		if err := applyAgentFile(ctx, tctx, target, data, before, ""); err != nil {
			return err
		}
		afterSuccessfulMutation(ctx, tctx, resolved.DisplayPath)
		return nil
	}
}

func init() {
	command.SetOutputCommitter(commitCommandOutput)
}

func commitCommandOutput(ctx context.Context, tc tools.ToolContext, loc fseffect.Location, output io.Reader, appendMode bool) error {
	req := agentStreamRequest{Target: mutationTarget{Abs: filepath.Join(loc.Root, loc.Rel), Location: loc}, Source: output}
	if appendMode {
		before, err := fseffect.OpenRead(loc)
		if err == nil {
			defer func() { _ = before.Close() }()
			hash := sha256.New()
			req.Source = io.MultiReader(io.TeeReader(before, hash), output)
			req.BeforeCommit = func(fseffect.Target, fseffect.Result) error {
				return verifyMutationSnapshot(loc, hex.EncodeToString(hash.Sum(nil)), false)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	_, err := applyAgentStream(ctx, tc, req)
	return err
}

// RegisterRenderViewTool registers render_view for authored mockup rasterization.
func RegisterRenderViewTool(reg *tools.DefaultRegistry, boundary *sandbox.Boundary, raster *browser.Rasterizer, handleStore renderhandle.Store) error {
	if reg == nil {
		return fmt.Errorf("registry required")
	}
	if raster == nil {
		return fmt.Errorf("browser rasterizer required")
	}
	destWriter := scopedWriter(boundary, page.RenderViewToolName)
	return reg.Register(page.RenderViewToolName, page.RenderViewHandler(raster, destWriter, handleStore))
}

// RegisterViewImageTool registers view_image for inspecting images visually.
func RegisterViewImageTool(reg *tools.DefaultRegistry, deps page.ViewImageDeps) error {
	if reg == nil {
		return fmt.Errorf("registry required")
	}
	return reg.Register(page.ViewImageToolName, page.ViewImageHandler(deps))
}

// RegisterViewVideoTool registers view_video for drawing frames from recordings.
func RegisterViewVideoTool(reg *tools.DefaultRegistry, deps page.ViewVideoDeps) error {
	if reg == nil {
		return fmt.Errorf("registry required")
	}
	if deps.Pool == nil {
		return fmt.Errorf("browser pool required")
	}
	return reg.Register(page.ViewVideoToolName, page.ViewVideoHandler(deps))
}

// RegisterPageSessionTools registers page_open / page_act / page_snapshot / page_close.
func RegisterPageSessionTools(reg *tools.DefaultRegistry, pool *browser.Pool, pages *pagesession.Registry, bg *bgprocess.Registry, live page.LivePreview) error {
	if reg == nil {
		return fmt.Errorf("registry required")
	}
	if pool == nil {
		return fmt.Errorf("browser pool required")
	}
	if pages == nil {
		return fmt.Errorf("page registry required")
	}
	if err := reg.Register(page.OpenToolName, page.OpenHandler(pool, pages, bg, live)); err != nil {
		return err
	}
	if err := reg.Register(page.ActToolName, page.ActHandler(pages, live)); err != nil {
		return err
	}
	if err := reg.Register(page.SnapshotToolName, page.SnapshotHandler(pages, live)); err != nil {
		return err
	}
	return reg.Register(page.CloseToolName, page.CloseHandler(pages, live))
}

// RegisterTerminalSessionTools registers terminal_open/send/read/snapshot/close.
func RegisterTerminalSessionTools(reg *tools.DefaultRegistry, bg *bgprocess.Registry) error {
	if reg == nil {
		return fmt.Errorf("registry required")
	}
	if bg == nil {
		return fmt.Errorf("background registry required")
	}
	if err := reg.Register(terminal.OpenToolName, terminal.OpenHandler(bg)); err != nil {
		return err
	}
	if err := reg.Register(terminal.SendToolName, terminal.SendHandler(bg)); err != nil {
		return err
	}
	if err := reg.Register(terminal.ReadToolName, terminal.ReadHandler(bg)); err != nil {
		return err
	}
	if err := reg.Register(terminal.SnapshotToolName, terminal.SnapshotHandler(bg)); err != nil {
		return err
	}
	return reg.Register(terminal.CloseToolName, terminal.CloseHandler(bg))
}

func RegisterSurfaceNoteTool(reg *tools.DefaultRegistry, deps reporttools.SurfaceNoteDeps) error {
	if reg == nil {
		return fmt.Errorf("registry required")
	}
	if deps.Ledger == nil {
		return fmt.Errorf("evidence ledger required")
	}
	if deps.Messages == nil {
		return fmt.Errorf("session messages required")
	}
	return reg.Register(reporttools.SurfaceNoteTool, func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		return reporttools.RunSurfaceNote(ctx, args, tctx, deps)
	})
}

func RegisterRecordFindingTool(reg *tools.DefaultRegistry, gates reporttools.RecordFindingGates, store findings.Store, scopeKey reporttools.FindingsScopeKey) error {
	if reg == nil {
		return fmt.Errorf("registry required")
	}
	if scopeKey == nil {
		return fmt.Errorf("findings scope key required")
	}
	if store == nil {
		return fmt.Errorf("findings store required")
	}
	return reg.Register("record_finding", reporttools.FindingsHandler(gates, store, scopeKey))
}

func RegisterUpdateProgressTool(reg *tools.DefaultRegistry, store progress.Store, scopeKey reporttools.ProgressScopeKey) error {
	if reg == nil {
		return fmt.Errorf("registry required")
	}
	if store == nil {
		return fmt.Errorf("progress store required")
	}
	if scopeKey == nil {
		return fmt.Errorf("progress scope key required")
	}
	return reg.Register("update_progress", reporttools.ProgressHandler(store, scopeKey))
}

func RegisterCompleteLegTool(reg *tools.DefaultRegistry, decode workertools.CompleteLegDecoder) error {
	if reg == nil {
		return fmt.Errorf("registry required")
	}
	if decode == nil {
		return fmt.Errorf("complete_leg decoder required")
	}
	return reg.Register(workertools.CompleteLegTool, workertools.CompleteLegHandler(decode))
}

func RegisterRequestDecisionTool(reg *tools.DefaultRegistry, deps workertools.RequestDecisionDeps) error {
	if reg == nil {
		return fmt.Errorf("registry required")
	}
	return reg.Register(workertools.RequestDecisionTool, workertools.DecisionHandler(deps))
}
