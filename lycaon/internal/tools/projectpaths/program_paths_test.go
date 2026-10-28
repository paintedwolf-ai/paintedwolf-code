//go:build darwin || linux

package projectpaths_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestProgramPathHelpersUseActualEntries(t *testing.T) {
	tc, root := readSessionContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind program cwd", err)
	testutil.FailErr(t, "seed exact file", os.WriteFile(filepath.Join(root, " trailing \n"), []byte("exact"), 0600))
	testutil.FailErr(t, "seed contained link", os.Symlink(" trailing \n", filepath.Join(root, "link")))
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"basename", []string{"--", "dir/file.txt", ".txt"}, "file\n"},
		{"basename", []string{""}, "\n"},
		{"dirname", []string{"--", "./pkg/file.go"}, "pkg\n"},
		{"readlink", []string{"--", "link"}, " trailing \n\n"},
		{"readlink", []string{"-f", "--", "link"}, filepath.Join(fspath.CanonicalPath(root), " trailing \n") + "\n"},
		{"realpath", []string{"--", "link"}, filepath.Join(fspath.CanonicalPath(root), " trailing \n") + "\n"},
		{"native_path", []string{"-0", "--", "@ws/ trailing \n"}, filepath.Join(root, " trailing \n") + "\x00"},
	}
	for _, c := range cases {
		t.Run(c.name+"/"+c.want, func(t *testing.T) {
			var output bytes.Buffer
			result, err := p.Invoke(t.Context(), commandprogram.OperationRequest{OperationID: c.name, Arguments: c.args}, commandprogram.Streams{Output: &output})
			testutil.FailErr(t, "invoke path helper", err)
			testutil.FailErr(t, "validate producer terminal", result.Validate())
			if result.Status != 0 || !result.Complete || !result.PublicationEligible || output.String() != c.want {
				t.Fatalf("result=%+v output=%q want=%q", result, output.String(), c.want)
			}
		})
	}
}

func TestProgramPathsKeepCwdInvocationLocal(t *testing.T) {
	tc, root := readSessionContext(t)
	before, err := os.Getwd()
	testutil.FailErr(t, "get host cwd", err)
	first, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "first invocation", err)
	second, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "second invocation", err)
	testutil.FailErr(t, "change logical cwd", first.Chdir(t.Context(), "pkg"))
	bound, err := first.Bind(t.Context(), "b.go", false)
	testutil.FailErr(t, "bind relative path", err)
	if bound.Resolved.Abs != filepath.Join(root, "pkg", "b.go") || second.Cwd().Abs != root {
		t.Fatalf("cwd leaked: first=%+v second=%+v", bound, second.Cwd())
	}
	after, err := os.Getwd()
	testutil.FailErr(t, "get unchanged host cwd", err)
	if after != before {
		t.Fatalf("host cwd changed from %q to %q", before, after)
	}
}

func TestProgramPathBindingRefusesLinkSwapAtEffect(t *testing.T) {
	tc, root := readSessionContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	testutil.FailErr(t, "create link", os.Symlink("a.go", filepath.Join(root, "selected")))
	bound, err := p.Bind(t.Context(), "selected", false)
	testutil.FailErr(t, "bind selected path", err)
	outside := filepath.Join(t.TempDir(), "secret")
	testutil.FailErr(t, "seed outside", os.WriteFile(outside, []byte("must not read"), 0600))
	testutil.FailErr(t, "replace old link", os.Remove(filepath.Join(root, "selected")))
	testutil.FailErr(t, "swap escaping link", os.Symlink(outside, filepath.Join(root, "selected")))
	if file, err := bound.Open(); err == nil {
		file.Close()
		t.Fatal("escaped read admitted after link swap")
	}
}

func TestProgramPathBindingRefusesReplacedRoot(t *testing.T) {
	tc, root := readSessionContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	bound, err := p.Bind(t.Context(), "a.go", false)
	testutil.FailErr(t, "bind file", err)
	displaced := root + "-displaced"
	testutil.FailErr(t, "displace root", os.Rename(root, displaced))
	t.Cleanup(func() { _ = os.RemoveAll(root); _ = os.Rename(displaced, root) })
	testutil.FailErr(t, "replace root", os.Mkdir(root, 0700))
	testutil.FailErr(t, "plant replacement", os.WriteFile(filepath.Join(root, "a.go"), []byte("replacement"), 0600))
	if err := bound.Validate(); err == nil {
		t.Fatal("replacement root gained old binding")
	}
	if file, err := bound.Open(); err == nil {
		file.Close()
		t.Fatal("replacement root read through old binding")
	}
}

func TestProgramPathConversionDoesNotRecordOrGrantMutation(t *testing.T) {
	tc, root, recorder := scratchToolContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	converted, err := captureNativePaths(t.Context(), p, []string{"./new file"}, true)
	testutil.FailErr(t, "convert path", err)
	if !bytes.Equal(converted, append([]byte(filepath.Join(tc.ActiveRootPath(), "new file")), 0)) {
		t.Fatalf("converted=%q", converted)
	}
	if len(recorder.mutations) != 0 || len(tc.Files.GrantedWriteRoots) != 0 {
		t.Fatal("conversion modified authority or mutation history")
	}
	tmp, err := p.TemporaryDirectory(t.Context())
	testutil.FailErr(t, "session temporary directory", err)
	if tmp != root {
		t.Fatalf("temporary directory=%q want %q", tmp, root)
	}
}

func TestProgramNullOperandIsTypedAndEmpty(t *testing.T) {
	tc, _ := readSessionContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	bound, err := p.Bind(t.Context(), "/dev/null", true)
	testutil.FailErr(t, "bind typed null", err)
	if !bound.Null || bound.Resolved.Abs != "" {
		t.Fatal("null became an ordinary path")
	}
	reader, err := bound.OpenReader()
	testutil.FailErr(t, "open null input", err)
	defer reader.Close()
	bytes, err := io.ReadAll(reader)
	testutil.FailErr(t, "read null input", err)
	if len(bytes) != 0 {
		t.Fatalf("null produced %q", bytes)
	}
	for _, literal := range []string{"./NUL", "./dev/null", "https://example.test/dev/null"} {
		if projectpaths.IsNullOperand(literal) {
			t.Fatalf("literal %q treated as device", literal)
		}
	}
}

func TestProgramPathPreservesNonUnicodeNulLists(t *testing.T) {
	tc, root := readSessionContext(t)
	name := string([]byte{'n', 0xff})
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	if runtime.GOOS == "darwin" {
		_, err := captureNativePaths(t.Context(), p, []string{name}, true)
		var pathErr *projectpaths.PathError
		if !errors.As(err, &pathErr) || pathErr.Code != "COMMAND_OPERAND_INVALID" {
			t.Fatalf("unrepresentable Darwin name did not refuse explicitly: %v", err)
		}
		return
	}
	testutil.FailErr(t, "seed native byte name", os.WriteFile(filepath.Join(root, name), []byte("native bytes"), 0600))
	got, err := captureNativePaths(t.Context(), p, []string{name}, true)
	testutil.FailErr(t, "convert native byte list", err)
	if !bytes.Equal(got, append([]byte(filepath.Join(root, name)), 0)) {
		t.Fatalf("lost native byte name: %q", got)
	}
	if _, err := captureNativePaths(t.Context(), p, []string{name}, false); err == nil {
		t.Fatal("unrepresentable scalar accepted")
	}
}

func TestProgramCanonicalRefusesMissingAndDanglingEntries(t *testing.T) {
	tc, root := readSessionContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	testutil.FailErr(t, "seed dangling link", os.Symlink("pkg/new.go", filepath.Join(root, "dangling")))
	for _, path := range []string{"pkg/new.go", "missing/new.go", "dangling"} {
		if _, err := p.Canonical(t.Context(), path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing path %q=%v", path, err)
		}
	}
}

func TestProgramPathsAuthorizeActualLinkIdentity(t *testing.T) {
	tc, root := readSessionContext(t)
	testutil.FailErr(t, "seed denied target", os.WriteFile(filepath.Join(root, "secret"), []byte("private"), 0600))
	testutil.FailErr(t, "seed allowed spelling", os.Symlink("../secret", filepath.Join(root, "pkg", "alias")))
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: toolprofiles.DefaultToolProfileID, Tools: map[string]bool{"read": true}, ReadGlobs: []string{"pkg", "pkg/**"}}})
	p, err := projectpaths.NewProgramPaths(t.Context(), boundary, tc, "pkg")
	testutil.FailErr(t, "bind allowed invocation directory", err)
	bound, err := p.Bind(t.Context(), "alias", false)
	testutil.FailErr(t, "bind coordinates without authority", err)
	if file, err := bound.Open(); err == nil {
		file.Close()
		t.Fatal("allowed spelling widened actual target read authority")
	}
	if _, err := p.Canonical(t.Context(), "alias"); err == nil {
		t.Fatal("canonical metadata bypassed actual target authority")
	}
	result, err := p.Invoke(t.Context(), commandprogram.OperationRequest{OperationID: "realpath", Arguments: []string{"--", "alias"}}, commandprogram.Streams{})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SURVEY_PATH_ESCAPE" || result.Terminal != toolcontract.ProducerRefused || result.StopCode != reject.Code {
		t.Fatalf("scope refusal became ordinary failure: result=%+v err=%v", result, err)
	}

	if _, err := p.LinkTarget(t.Context(), "../secret"); err == nil {
		t.Fatal("link metadata bypassed entry authority")
	}
}

func TestCdNoArgumentRestoresInitialLogicalDirectory(t *testing.T) {
	tc, _ := readSessionContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "pkg")
	testutil.FailErr(t, "initial subdirectory", err)
	testutil.FailErr(t, "leave initial directory", p.Chdir(t.Context(), ".."))
	result, err := p.Invoke(t.Context(), commandprogram.OperationRequest{OperationID: "cd"}, commandprogram.Streams{})
	testutil.FailErr(t, "restore initial directory", err)
	testutil.FailErr(t, "validate cd terminal", result.Validate())
	var out bytes.Buffer
	_, err = p.Invoke(t.Context(), commandprogram.OperationRequest{OperationID: "pwd"}, commandprogram.Streams{Output: &out})
	testutil.FailErr(t, "logical pwd", err)
	if out.String() != "pkg\n" {
		t.Fatalf("logical cwd=%q", out.String())
	}
}

type shortPathWriter struct{}

func (shortPathWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
func TestPathProducerSettlesShortWriteAndTypedRefusals(t *testing.T) {
	tc, _ := readSessionContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	result, err := p.Invoke(t.Context(), commandprogram.OperationRequest{OperationID: "pwd"}, commandprogram.Streams{Output: shortPathWriter{}})
	if !errors.Is(err, io.ErrShortWrite) || result.Terminal != toolcontract.ProducerFailed || result.PublicationEligible || result.Complete {
		t.Fatalf("short write result=%+v err=%v", result, err)
	}
	testutil.FailErr(t, "validate short-write terminal", result.Validate())
	result, err = p.Invoke(t.Context(), commandprogram.OperationRequest{OperationID: "basename", Arguments: []string{"--bad"}}, commandprogram.Streams{})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "COMMAND_OPTION_UNSUPPORTED" || result.Terminal != toolcontract.ProducerRefused || result.StopCode != reject.Code || reject.Data["expected_form"] != "basename [--] PATH [SUFFIX]" {
		t.Fatalf("unsupported result=%+v reject=%+v", result, reject)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err = p.Invoke(ctx, commandprogram.OperationRequest{OperationID: "pwd"}, commandprogram.Streams{})
	if !errors.Is(err, context.Canceled) || result.Terminal != toolcontract.ProducerInterrupted || result.PublicationEligible {
		t.Fatalf("cancelled result=%+v err=%v", result, err)
	}
}

func TestPhysicalRootAliasKeepsAttachedReadScope(t *testing.T) {
	tc, root := readSessionContext(t)
	alias := filepath.Join(t.TempDir(), "attached-alias")
	testutil.FailErr(t, "seed attached root alias", os.Symlink(root, alias))
	tc.Source.Roots[0].Path = alias
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: toolprofiles.DefaultToolProfileID, Tools: map[string]bool{"read": true}, ReadGlobs: []string{"pkg", "pkg/**"}}})
	p, err := projectpaths.NewProgramPaths(t.Context(), boundary, tc, "pkg")
	testutil.FailErr(t, "bind alias invocation", err)
	allowed, err := p.Bind(t.Context(), filepath.Join(root, "pkg", "b.go"), false)
	testutil.FailErr(t, "bind physical spelling of attached alias", err)
	file, err := allowed.Open()
	testutil.FailErr(t, "open actual admitted identity", err)
	file.Close()
	denied, err := p.Bind(t.Context(), filepath.Join(root, "a.go"), false)
	testutil.FailErr(t, "bind denied coordinates", err)
	if file, err := denied.Open(); err == nil {
		file.Close()
		t.Fatal("physical root spelling bypassed attached read profile")
	}
}

func TestNativeCaseSpellingRetainsFilesystemIdentity(t *testing.T) {
	tc, root := readSessionContext(t)
	testutil.FailErr(t, "seed upper case name", os.WriteFile(filepath.Join(root, "Case"), []byte("upper"), 0600))
	testutil.FailErr(t, "seed lower case name", os.WriteFile(filepath.Join(root, "case"), []byte("lower"), 0600))
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind invocation", err)
	for _, name := range []string{"Case", "case"} {
		bound, err := p.Bind(t.Context(), name, false)
		testutil.FailErr(t, "bind preserved case spelling", err)
		if bound.Resolved.Abs != filepath.Join(root, name) {
			t.Fatalf("case spelling changed: %q", bound.Resolved.Abs)
		}
		file, err := bound.Open()
		testutil.FailErr(t, "open native case identity", err)
		native, err := io.ReadAll(file)
		file.Close()
		testutil.FailErr(t, "read native case identity", err)
		actual, err := os.ReadFile(filepath.Join(root, name))
		testutil.FailErr(t, "read OS case identity", err)
		if !bytes.Equal(native, actual) {
			t.Fatalf("native and OS disagree for %q", name)
		}
	}
	upper, err := os.Stat(filepath.Join(root, "Case"))
	testutil.FailErr(t, "stat upper", err)
	lower, err := os.Stat(filepath.Join(root, "case"))
	testutil.FailErr(t, "stat lower", err)
	if !os.SameFile(upper, lower) {
		a, err := os.ReadFile(filepath.Join(root, "Case"))
		testutil.FailErr(t, "read distinct upper", err)
		b, err := os.ReadFile(filepath.Join(root, "case"))
		testutil.FailErr(t, "read distinct lower", err)
		if string(a) != "upper" || string(b) != "lower" {
			t.Fatal("distinct case-sensitive entries were collapsed")
		}
	}
}

func TestOpenCwdRetainsCheckedDirectoryAcrossReplacement(t *testing.T) {
	tc, root := readSessionContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "pkg")
	testutil.FailErr(t, "bind current directory", err)
	held, err := p.OpenCwd(t.Context())
	testutil.FailErr(t, "hold checked directory", err)
	defer held.Close()
	before, err := held.Stat()
	testutil.FailErr(t, "capture held identity", err)
	testutil.FailErr(t, "move cwd fixture", os.Rename(filepath.Join(root, "pkg"), filepath.Join(root, "moved")))
	testutil.FailErr(t, "replace cwd spelling", os.Mkdir(filepath.Join(root, "pkg"), 0700))
	if replacement, err := p.OpenCwd(t.Context()); err == nil {
		replacement.Close()
		t.Fatal("cwd replacement inherited captured identity")
	}
	after, err := held.Stat()
	testutil.FailErr(t, "inspect retained handle", err)
	if !os.SameFile(before, after) {
		t.Fatal("held cwd identity changed")
	}
	entries, err := held.ReadDir(-1)
	testutil.FailErr(t, "read retained directory", err)
	if len(entries) != 1 || entries[0].Name() != "b.go" {
		t.Fatal("held directory observed replacement")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.OpenCwd(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled cwd open=%v", err)
	}
}

func TestNativeUnicodeNormalizationSpellingRemainsExact(t *testing.T) {
	tc, root := readSessionContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind Unicode fixture", err)
	for _, name := range []string{"é", "e\u0301"} {
		testutil.FailErr(t, "seed exact Unicode spelling", os.WriteFile(filepath.Join(root, name), []byte(name), 0600))
		converted, err := captureNativePaths(t.Context(), p, []string{name}, true)
		testutil.FailErr(t, "convert exact Unicode spelling", err)
		if string(converted) != filepath.Join(root, name)+"\x00" {
			t.Fatalf("normalization changed native spelling: %q", converted)
		}
		bound, err := p.Bind(t.Context(), name, false)
		testutil.FailErr(t, "bind actual Unicode entry", err)
		file, err := bound.Open()
		testutil.FailErr(t, "open actual Unicode entry", err)
		native, err := io.ReadAll(file)
		file.Close()
		testutil.FailErr(t, "read native Unicode entry", err)
		actual, err := os.ReadFile(filepath.Join(root, name))
		testutil.FailErr(t, "read physical Unicode entry", err)
		if !bytes.Equal(native, actual) {
			t.Fatal("native normalization identity differs from OS")
		}
	}
}

func TestNativeScalarRefusesTrailingLfWithoutChangingNulList(t *testing.T) {
	tc, root := readSessionContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind newline fixture", err)
	name := "trailing\n"
	testutil.FailErr(t, "seed trailing LF name", os.WriteFile(filepath.Join(root, name), []byte("exact"), 0600))
	if _, err := captureNativePaths(t.Context(), p, []string{name}, false); err == nil {
		t.Fatal("scalar conversion accepted a name substitution would change")
	}
	exact, err := captureNativePaths(t.Context(), p, []string{name}, true)
	testutil.FailErr(t, "convert exact NUL operand", err)
	if string(exact) != filepath.Join(root, name)+"\x00" {
		t.Fatalf("NUL conversion changed terminal LF: %q", exact)
	}
	interior, err := captureNativePaths(t.Context(), p, []string{"line\nbreak"}, false)
	testutil.FailErr(t, "convert representable interior LF", err)
	if string(interior) != filepath.Join(root, "line\nbreak")+"\n" {
		t.Fatalf("interior LF changed: %q", interior)
	}
}

func captureNativePaths(ctx context.Context, p *projectpaths.ProgramPaths, operands []string, nul bool) ([]byte, error) {
	var output bytes.Buffer
	err := p.NativePaths(ctx, operands, nul, &output)
	return output.Bytes(), err
}

func TestPathStreamingSeparatesDeliveredPrefixFromCompleteness(t *testing.T) {
	tc, root := readSessionContext(t)
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, tc, "")
	testutil.FailErr(t, "bind streaming fixture", err)
	var output bytes.Buffer
	result, err := p.Invoke(t.Context(), commandprogram.OperationRequest{OperationID: "native_path", Arguments: []string{"--", "a.go", "/dev/null"}}, commandprogram.Streams{Output: &output})
	if err == nil || output.Len() != 0 || result.PublicationEligible {
		t.Fatalf("invalid conversion exposed unvalidated prefix: output=%q result=%+v err=%v", output.String(), result, err)
	}
	result, err = p.Invoke(t.Context(), commandprogram.OperationRequest{OperationID: "realpath", Arguments: []string{"--", "a.go", "missing"}}, commandprogram.Streams{Output: &output})
	if !errors.Is(err, os.ErrNotExist) || output.String() != filepath.Join(fspath.CanonicalPath(root), "a.go")+"\n" || result.Complete || result.PublicationEligible || result.Terminal != toolcontract.ProducerFailed {
		t.Fatalf("partial output reported complete: output=%q result=%+v err=%v", output.String(), result, err)
	}
	testutil.FailErr(t, "validate incomplete producer", result.Validate())
}
