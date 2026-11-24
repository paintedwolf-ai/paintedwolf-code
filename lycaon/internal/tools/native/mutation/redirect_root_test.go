package mutation

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestInputRedirectBindsOnlyWithAnEffectiveReviewGrant(t *testing.T) {
	h := newHarness(t, nil)
	shared, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve shared temp", err)
	t.Setenv("TMPDIR", shared)
	path := filepath.Join(shared, "input.sh")
	testutil.FailErr(t, "seed shared temp input", os.WriteFile(path, []byte("echo shared\n"), 0o600))
	declined := errors.New("declined")

	p := h.publication()
	p.ReviewRead = func(_ context.Context, tc tools.ToolContext, operator, target string) (tools.ToolContext, error) {
		t.Fatalf("unexpected review for %q %q", operator, target)
		return tc, nil
	}
	if _, _, err := p.bindInput(t.Context(), "<", "/etc/hosts"); toolrejection.AsToolReject(err) == nil {
		t.Fatalf("non-temp input escape returned %v", err)
	}

	for _, outcome := range []string{"declined", "grant missing", "approved"} {
		t.Run(outcome, func(t *testing.T) {
			p := h.publication()
			p.ReviewRead = func(_ context.Context, tc tools.ToolContext, operator, target string) (tools.ToolContext, error) {
				if (operator != "<" && operator != "source") || target != path {
					t.Fatalf("reviewed operator %q, target %q", operator, target)
				}
				if outcome == "declined" {
					return tc, declined
				}
				if outcome == "approved" {
					tc.Files.SessionReadPaths = []string{target}
				}
				return tc, nil
			}
			inv := h.invocation()
			dest := commandprogram.InputDestination{
				Invocation: inv,
				Redirect:   commandsurface.ProgramRedirect{Operator: "<", Descriptor: 0},
				Target:     commandprogram.Field{Value: path, Span: inv.Span},
			}
			reader, err := p.OpenInput(t.Context(), dest)
			switch outcome {
			case "declined":
				if !errors.Is(err, declined) {
					t.Fatalf("declined input redirect returned %v", err)
				}
			case "grant missing":
				if err == nil {
					t.Fatal("input redirect bound without the installed read grant")
				}
			case "approved":
				testutil.FailErr(t, "open reviewed input redirect", err)
				data, readErr := io.ReadAll(reader)
				_ = reader.Close()
				testutil.FailErr(t, "read reviewed input stream", readErr)
				if string(data) != "echo shared\n" {
					t.Fatalf("input stream = %q", string(data))
				}
				captured, capErr := p.Capture(t.Context(), h.invocation(), commandprogram.SourceCaptureRequest{
					Path:           path,
					Representation: toolcontract.UTF8SourceBytes,
				})
				testutil.FailErr(t, "capture reviewed source script", capErr)
				if string(captured.Bytes) != "echo shared\n" {
					t.Fatalf("captured source = %q", string(captured.Bytes))
				}
			}
		})
	}
}

func TestRedirectBindsOnlyWithAnEffectiveReviewGrant(t *testing.T) {
	h := newHarness(t, nil)
	shared, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve shared temp", err)
	t.Setenv("TMPDIR", shared)
	path := filepath.Join(shared, "output")
	declined := errors.New("declined")
	for _, outcome := range []string{"declined", "grant missing", "approved"} {
		t.Run(outcome, func(t *testing.T) {
			p := h.publication()
			p.ReviewRoot = func(_ context.Context, tc tools.ToolContext, operator, target, root string) (tools.ToolContext, error) {
				if operator != ">" || target != path || root != shared {
					t.Fatalf("reviewed operator %q, target %q, root %q", operator, target, root)
				}
				if outcome == "declined" {
					return tc, declined
				}
				if outcome == "approved" {
					tc.Files.GrantedWriteRoots = []string{root}
				}
				return tc, nil
			}
			bound, tc, err := p.bindOutput(t.Context(), h.destination(">", path))
			switch outcome {
			case "declined":
				if !errors.Is(err, declined) {
					t.Fatalf("declined redirect returned %v", err)
				}
			case "grant missing":
				if err == nil {
					t.Fatal("redirect bound without the installed root")
				}
			case "approved":
				testutil.FailErr(t, "bind reviewed redirect", err)
				if bound.Resolved.Abs != path || len(tc.Files.GrantedWriteRoots) != 1 || tc.Files.GrantedWriteRoots[0] != shared {
					t.Fatalf("bound path %q with grants %v", bound.Resolved.Abs, tc.Files.GrantedWriteRoots)
				}
			}
		})
	}
}
