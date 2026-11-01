package search

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native"
)

var findTree = map[string]string{
	"tree/a.go":          "package a\n",
	"tree/b.txt":         "x",
	"tree/.hid":          "h",
	"tree/sub/c.go":      "package c\n",
	"tree/sub/empty.txt": "",
	"tree/emptydir/":     "",
	"tree/big.bin":       string(make([]byte, 1500)),
	"empty-dir/":         "",
}

func TestFindFormsProduceExactBytes(t *testing.T) {
	all := "tree\ntree/.hid\ntree/a.go\ntree/b.txt\ntree/big.bin\ntree/emptydir\ntree/sub\ntree/sub/c.go\ntree/sub/empty.txt\n"
	for _, c := range []formCase{
		{name: "catalog example", argv: []string{"find", "./empty-dir", "-type", "f", "-print0"}},
		{name: "default print in byte order", argv: []string{"find", "tree"}, stdout: all},
		{name: "implicit start", argv: []string{"find", "-name", "c.go"}, stdout: "./tree/sub/c.go\n"},
		{name: "name", argv: []string{"find", "tree", "-name", "*.go"}, stdout: "tree/a.go\ntree/sub/c.go\n"},
		{name: "name matches hidden", argv: []string{"find", "tree", "-name", "*h*", "-type", "f"}, stdout: "tree/.hid\n"},
		{name: "iname", argv: []string{"find", "tree", "-iname", "*.GO"}, stdout: "tree/a.go\ntree/sub/c.go\n"},
		{name: "path", argv: []string{"find", "tree", "-path", "*sub*"}, stdout: "tree/sub\ntree/sub/c.go\ntree/sub/empty.txt\n"},
		{name: "ipath", argv: []string{"find", "tree", "-ipath", "TREE/SUB"}, stdout: "tree/sub\n"},
		{name: "regex is ERE over the whole path", argv: []string{"find", "tree", "-regex", ".*/[ab]\\.(go|txt)"}, stdout: "tree/a.go\ntree/b.txt\n"},
		{name: "type d", argv: []string{"find", "tree", "-type", "d"}, stdout: "tree\ntree/emptydir\ntree/sub\n"},
		{name: "maxdepth", argv: []string{"find", "tree", "-maxdepth", "1", "-type", "f"}, stdout: "tree/.hid\ntree/a.go\ntree/b.txt\ntree/big.bin\n"},
		{name: "mindepth", argv: []string{"find", "tree", "-mindepth", "2"}, stdout: "tree/sub/c.go\ntree/sub/empty.txt\n"},
		{name: "size in bytes", argv: []string{"find", "tree", "-type", "f", "-size", "+9c"}, stdout: "tree/a.go\ntree/big.bin\ntree/sub/c.go\n"},
		{name: "size in blocks rounds up", argv: []string{"find", "tree", "-type", "f", "-size", "3"}, stdout: "tree/big.bin\n"},
		{name: "size kilobytes", argv: []string{"find", "tree", "-type", "f", "-size", "2k"}, stdout: "tree/big.bin\n"},
		{name: "size below one kilobyte", argv: []string{"find", "tree", "-type", "f", "-size", "-2k", "-name", "*.go"}, stdout: "tree/a.go\ntree/sub/c.go\n"},
		{name: "size less than one unit is empty", argv: []string{"find", "tree", "-type", "f", "-size", "-1"}, stdout: "tree/sub/empty.txt\n"},
		{name: "empty", argv: []string{"find", "tree", "-empty"}, stdout: "tree/emptydir\ntree/sub/empty.txt\n"},
		{name: "mtime exact days", argv: []string{"find", "tree/sub", "-type", "f", "-mtime", "1"}, stdout: "tree/sub/c.go\ntree/sub/empty.txt\n"},
		{name: "mtime greater", argv: []string{"find", "tree/sub", "-type", "f", "-mtime", "+1"}},
		{name: "mtime greater than zero", argv: []string{"find", "tree/sub", "-type", "f", "-mtime", "+0"}, stdout: "tree/sub/c.go\ntree/sub/empty.txt\n"},
		{name: "mtime less", argv: []string{"find", "tree/sub", "-mtime", "-2", "-type", "f"}, stdout: "tree/sub/c.go\ntree/sub/empty.txt\n"},
		{name: "not", argv: []string{"find", "tree", "!", "-name", "*.go", "-type", "f"}, stdout: "tree/.hid\ntree/b.txt\ntree/big.bin\ntree/sub/empty.txt\n"},
		{name: "not word", argv: []string{"find", "tree", "-type", "f", "-not", "-size", "+0"}, stdout: "tree/sub/empty.txt\n"},
		{name: "or", argv: []string{"find", "tree", "-name", "a.go", "-o", "-name", "b.txt"}, stdout: "tree/a.go\ntree/b.txt\n"},
		{name: "and binds tighter than or", argv: []string{"find", "tree", "-type", "d", "-name", "sub", "-o", "-name", "a.go"}, stdout: "tree/a.go\ntree/sub\n"},
		{name: "explicit and", argv: []string{"find", "tree", "-type", "f", "-a", "-name", "c.go"}, stdout: "tree/sub/c.go\n"},
		{name: "parentheses", argv: []string{"find", "tree", "(", "-name", "a.go", "-o", "-name", "c.go", ")", "-print"}, stdout: "tree/a.go\ntree/sub/c.go\n"},
		{name: "or without action prints both sides", argv: []string{"find", "tree", "-name", "sub", "-o", "-name", "*.txt"}, stdout: "tree/b.txt\ntree/sub\ntree/sub/empty.txt\n"},
		{name: "prune", argv: []string{"find", "tree", "-name", "sub", "-prune", "-o", "-type", "f", "-print"}, stdout: "tree/.hid\ntree/a.go\ntree/b.txt\ntree/big.bin\n"},
		{name: "print0", argv: []string{"find", "tree/sub", "-type", "f", "-print0"}, stdout: "tree/sub/c.go\x00tree/sub/empty.txt\x00"},
		{name: "print and print0", argv: []string{"find", "tree/sub", "-name", "c.go", "-print", "-print0"}, stdout: "tree/sub/c.go\ntree/sub/c.go\x00"},
		{name: "newer", argv: []string{"find", "tree", "-type", "f", "-newer", "tree/a.go"}},
		{name: "missing start", argv: []string{"find", "nope", "tree/sub"}, stdout: "tree/sub\ntree/sub/c.go\ntree/sub/empty.txt\n", stderr: "find: 'nope': No such file or directory\n", status: 1},
		{name: "missing newer reference", argv: []string{"find", "tree", "-newer", "nope"}, stderr: "find: 'nope': No such file or directory\n", status: 1},
		{name: "trailing slash start", argv: []string{"find", "tree/sub/", "-name", "*.go"}, stdout: "tree/sub/c.go\n"},
		{name: "iregex", argv: []string{"find", "tree", "-iregex", ".*/A\\.GO"}, stdout: "tree/a.go\n"},
		{name: "mmin", argv: []string{"find", "tree/sub", "-type", "f", "-mmin", "+2159"}, stdout: "tree/sub/c.go\ntree/sub/empty.txt\n"},
		{name: "mmin exact", argv: []string{"find", "tree/sub", "-type", "f", "-mmin", "-2161", "-mmin", "+2158"}, stdout: "tree/sub/c.go\ntree/sub/empty.txt\n"},
		{name: "perm exact", argv: []string{"find", "tree/sub", "-type", "f", "-perm", "644"}, stdout: "tree/sub/c.go\ntree/sub/empty.txt\n"},
		{name: "perm all bits", argv: []string{"find", "tree/sub", "-type", "f", "-perm", "-600"}, stdout: "tree/sub/c.go\ntree/sub/empty.txt\n"},
		{name: "perm any bit", argv: []string{"find", "tree/sub", "-type", "f", "-perm", "/111"}},
		{name: "printf", argv: []string{"find", "tree/sub", "-printf", "%y %d %P|%f|%h|%m\\n"}, stdout: "d 0 |sub|tree|755\nf 1 c.go|c.go|tree/sub|644\nf 1 empty.txt|empty.txt|tree/sub|644\n"},
		{name: "printf size", argv: []string{"find", "tree/sub", "-type", "f", "-printf", "%s\\t%T@\\n"}, stdout: "10\t1767323045.0000000000\n0\t1767323045.0000000000\n"},
		{name: "BSD -E and -s", argv: []string{"find", "-E", "-s", "tree", "-regex", ".*/[ab]\\.(go|txt)"}, stdout: "tree/a.go\ntree/b.txt\n"},
		{name: "depth first", argv: []string{"find", "tree/sub", "-depth"}, stdout: "tree/sub/c.go\ntree/sub/empty.txt\ntree/sub\n"},
		{name: "BSD -d", argv: []string{"find", "-d", "tree/sub"}, stdout: "tree/sub/c.go\ntree/sub/empty.txt\ntree/sub\n"},
		{name: "BSD depth test", argv: []string{"find", "tree", "-depth", "2"}, stdout: "tree/sub/c.go\ntree/sub/empty.txt\n"},
		{name: "BSD depth less", argv: []string{"find", "tree", "-depth", "-1"}, stdout: "tree\n"},
		{name: "same device", argv: []string{"find", "tree/sub", "-xdev"}, stdout: "tree/sub\ntree/sub/c.go\ntree/sub/empty.txt\n"},
		{name: "BSD -x", argv: []string{"find", "-x", "tree/sub"}, stdout: "tree/sub\ntree/sub/c.go\ntree/sub/empty.txt\n"},
		{name: "long operators", argv: []string{"find", "tree", "-name", "a.go", "-or", "-name", "c.go", "-and", "-type", "f"}, stdout: "tree/a.go\ntree/sub/c.go\n"},
		{name: "true and false", argv: []string{"find", "tree/sub", "-true", "-name", "c.go", "-o", "-false"}, stdout: "tree/sub/c.go\n"},
		{name: "size words", argv: []string{"find", "tree", "-type", "f", "-size", "5w"}, stdout: "tree/a.go\ntree/sub/c.go\n"},
		{name: "size gibibytes", argv: []string{"find", "tree", "-type", "f", "-size", "-1G", "-name", "*.go"}},
		{name: "printf literal and percent", argv: []string{"find", "tree/sub/c.go", "-printf", "[%p] 100%%\\0"}, stdout: "[tree/sub/c.go] 100%\x00"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, findTree)
			h.exec(c.stdin, c.argv...).expect(t, c.name, c.stdout, c.stderr, c.status)
		})
	}
}

func TestFindNewerComparesModificationTimes(t *testing.T) {
	h := newHarness(t, findTree)
	later := fixtureTime.Add(time.Hour)
	testutil.FailErr(t, "touch", os.Chtimes(filepath.Join(h.root, "tree", "b.txt"), later, later))
	h.exec("", "find", "tree", "-type", "f", "-newer", "tree/a.go").expect(t, "newer", "tree/b.txt\n", "", 0)
}

func TestFindPreservesHostileNames(t *testing.T) {
	h := newHarness(t, map[string]string{"t/-dash\nname": "x", "t/sp ace": "y"})
	h.exec("", "find", "t", "-type", "f", "-print0").expect(t, "print0", "t/-dash\nname\x00t/sp ace\x00", "", 0)
	h.exec("", "find", "t", "-name", "-dash*").expect(t, "dash glob is data", "t/-dash\nname\n", "", 0)
	r := h.exec("", "find", "t", "-name", "-dash*", "-exec", "printf", "%s", "{}", ";")
	r.expect(t, "exec keeps one argument", "argv: printf|%s|t/-dash\nname\n", "", 0)
	h.exec("", "grep", "-l", "--", "x", "t/-dash\nname").expect(t, "grep operand after --", "t/-dash\nname\n", "", 0)
}

func TestFindRefusals(t *testing.T) {
	h := newHarness(t, findTree)
	h.exec("", "find", "tree", "-not-a-version-1-option").refused(t, "unsupported primary", "COMMAND_OPTION_UNSUPPORTED")
}

func TestFindUtilityErrors(t *testing.T) {
	h := newHarness(t, findTree)
	for _, c := range []struct {
		name   string
		argv   []string
		stderr string
	}{
		{"invalid regex", []string{"find", "tree", "-regex", "(a"}, "find: failed to compile regular expression '(a': Unmatched ( or \\(\n"},
		{"unsupported type", []string{"find", "tree", "-type", "x"}, ""},
		{"bad size", []string{"find", "tree", "-size", "1q"}, ""},
		{"bad mtime", []string{"find", "tree", "-mtime", "x"}, ""},
		{"bad depth", []string{"find", "tree", "-maxdepth", "-1"}, ""},
		{"path after expression", []string{"find", "tree", "-name", "a", "extra"}, ""},
		{"unbalanced close", []string{"find", "tree", ")"}, ""},
		{"unbalanced open", []string{"find", "tree", "(", "-name", "a"}, ""},
		{"empty parentheses", []string{"find", "tree", "(", ")"}, ""},
		{"dangling operator", []string{"find", "tree", "-name", "a", "-o"}, ""},
		{"leading operator", []string{"find", "tree", "-o", "-name", "a"}, ""},
		{"exec needs terminator", []string{"find", "tree", "-exec", "printf", "{}"}, ""},
		{"second braces in batch", []string{"find", "tree", "-exec", "printf", "{}x", "{}", "+"}, ""},
		{"printf directive", []string{"find", "tree", "-printf", "%Z"}, ""},
		{"printf escape", []string{"find", "tree", "-printf", "\\q"}, ""},
		{"bad perm", []string{"find", "tree", "-perm", "u+x"}, ""},
		{"user name", []string{"find", "tree", "-user", "root"}, ""},
		{"group name", []string{"find", "tree", "-group", "wheel"}, ""},
		{"leading option after paths", []string{"find", "tree", "-E"}, ""},
		{"bad mmin", []string{"find", "tree", "-mmin", "x"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := h.exec("", c.argv...)
			if r.err != nil || r.result.Terminal != toolcontract.ProducerCompleted || r.result.Status != 1 || r.stdout != "" || r.result.PublicationEligible {
				t.Fatalf("%s: want utility error status 1, got result %+v err %v stdout %q stderr %q", c.name, r.result, r.err, r.stdout, r.stderr)
			}
			if c.stderr != "" && r.stderr != c.stderr {
				t.Fatalf("%s: stderr %q, want %q", c.name, r.stderr, c.stderr)
			}
			if r.stderr == "" {
				t.Fatalf("%s: empty stderr", c.name)
			}
		})
	}
}

func TestFindExecDispatchesOrdinaryChildren(t *testing.T) {
	h := newHarness(t, findTree)
	r := h.exec("", "find", "tree", "-name", "*.go", "-exec", "printf", "<%s>", "{}", ";")
	r.expect(t, "single", "argv: printf|<%s>|tree/a.go\nargv: printf|<%s>|tree/sub/c.go\n", "", 0)
	if len(h.dispatch.requests) != 2 {
		t.Fatalf("children %d", len(h.dispatch.requests))
	}
	for _, child := range h.dispatch.requests {
		inv := child.Invocation
		if inv.Identity.ParentExecutionID != r.execution || inv.Identity.ExecutionID == r.execution || inv.Identity.ProgramID != "program" || child.OperationID != "printf" || child.Statement.Span != inv.Span || child.Validate() != nil {
			t.Fatalf("child identity %+v operation %s", inv, child.OperationID)
		}
	}
	first, second := h.dispatch.requests[0].Invocation.Identity.ExecutionID, h.dispatch.requests[1].Invocation.Identity.ExecutionID
	if first == second {
		t.Fatalf("children share execution %s", first)
	}

	h.dispatch.requests = nil
	h.exec("", "find", "tree", "-name", "*.go", "-exec", "printf", "%s\\n", "{}", "+").expect(t, "batch", "argv: printf|%s\\n|tree/a.go|tree/sub/c.go\n", "", 0)
	if len(h.dispatch.requests) != 1 {
		t.Fatalf("batch children %d", len(h.dispatch.requests))
	}

	h.dispatch.requests = nil
	h.exec("", "find", "tree", "-name", "*.go", "-exec", "tool-not-in-the-catalog", "{}", ";", "-print").expect(t, "external launches", "argv: tool-not-in-the-catalog|tree/a.go\ntree/a.go\nargv: tool-not-in-the-catalog|tree/sub/c.go\ntree/sub/c.go\n", "", 0)
	if h.dispatch.requests[0].OperationID != "external" {
		t.Fatalf("external operation %s", h.dispatch.requests[0].OperationID)
	}

	// A native child runs through the same owners; its status is the test.
	h.exec("", "find", "tree", "-type", "f", "-exec", "grep", "-q", "package c", "{}", ";", "-print").expect(t, "native child status", "tree/sub/c.go\n", "", 0)

	h.dispatch.status = func(argv []string) int { return 3 }
	h.exec("", "find", "tree/sub", "-type", "f", "-exec", "printf", "%s", "{}", "+").expect(t, "failed batch", "argv: printf|%s|tree/sub/c.go|tree/sub/empty.txt\n", "", 1)
	h.dispatch.status = nil

	h.owners.overrides = map[string]int64{"child_executions": 1}
	h.exec("", "find", "tree", "-name", "*.go", "-exec", "printf", "{}", ";").refused(t, "child limit", "COMMAND_LIMIT_EXCEEDED")
	h.owners.overrides = map[string]int64{"argv_bytes": 32}
	h.dispatch.requests = nil
	h.exec("", "find", "tree", "-type", "f", "-exec", "printf", "{}", "+").expect(t, "argv batches", "argv: printf|tree/.hid|tree/a.go\nargv: printf|tree/b.txt|tree/big.bin\nargv: printf|tree/sub/c.go\nargv: printf|tree/sub/empty.txt\n", "", 0)
	h.owners.overrides = nil

	r = h.exec("", "find", "tree", "-name", "a.go", "-exec", "grep", "-P", "(?R)", "{}", ";")
	r.refused(t, "refused child stops find", "GREP_REGEX_UNSUPPORTED")
}

func TestFindExecWithoutDispatcherRefuses(t *testing.T) {
	h := newHarness(t, findTree)
	request := requestFor(h, "find", "tree", "-exec", "printf", "{}", ";")
	result, err := h.owners.Invoke(t.Context(), h.tc, mustPaths(t, h), nil, request, streamsFor())
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "COMMAND_NATIVE_FORM_UNSUPPORTED" || result.Terminal != toolcontract.ProducerRefused {
		t.Fatalf("no dispatcher: %+v %v", result, err)
	}
}

func TestFindDeleteRoutesThroughRecoverableRemoval(t *testing.T) {
	h := newHarness(t, findTree)
	r := h.exec("", "find", "tree/sub", "-name", "*.txt", "-delete")
	r.expect(t, "delete", "", "", 0)
	if !slices.Equal(h.removals.removed, []string{"tree/sub/empty.txt"}) || r.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("removed %v effect %s", h.removals.removed, r.result.EffectCertainty)
	}
	if _, err := os.Lstat(filepath.Join(h.root, "tree", "sub", "empty.txt")); !os.IsNotExist(err) {
		t.Fatalf("entry still present: %v", err)
	}

	h.removals.removed = nil
	h.exec("", "find", "tree/sub", "-delete").expect(t, "depth first", "", "", 0)
	if !slices.Equal(h.removals.removed, []string{"tree/sub/c.go", "tree/sub"}) {
		t.Fatalf("depth-first order %v", h.removals.removed)
	}

	h.removals.removed = nil
	h.exec("", "find", "tree", "-name", "emptydir", "-delete", "-o", "-name", "a.go", "-print").expect(t, "delete then print", "tree/a.go\n", "", 0)

	r = h.exec("", "find", "tree", "-maxdepth", "0", "-delete")
	r.expect(t, "non-empty directory", "", "find: cannot delete 'tree': Directory not empty\n", 1)

	h.removals.refuse = map[string]error{"tree/b.txt": &toolrejection.ToolReject{Code: "DELETE_PATH_DENIED"}}
	h.exec("", "find", "tree", "-name", "b.txt", "-delete").refused(t, "owner refusal stops", "DELETE_PATH_DENIED")
	h.removals.refuse = nil

	h.owners.Remover = nil
	h.exec("", "find", "tree", "-delete").refused(t, "no remover", "COMMAND_NATIVE_FORM_UNSUPPORTED")
	if _, err := os.Lstat(filepath.Join(h.root, "tree", "a.go")); err != nil {
		t.Fatalf("refused -delete removed entries: %v", err)
	}
}

func TestFindVisitsLinksAsEntries(t *testing.T) {
	h := newHarness(t, map[string]string{"d/f": "x", "target/t": "y"})
	symlink(t, "../target", filepath.Join(h.root, "d", "ld"))
	h.exec("", "find", "d").expect(t, "links are not followed", "d\nd/f\nd/ld\n", "", 0)
	h.exec("", "find", "d", "-type", "l").expect(t, "type l", "d/ld\n", "", 0)
	h.exec("", "find", "-L", "d").expect(t, "-L follows", "d\nd/f\nd/ld\nd/ld/t\n", "", 0)
	h.exec("", "find", "-L", "d", "-type", "l").expect(t, "-L type l is unreachable links only", "", "", 0)
	h.exec("", "find", "-H", "d/ld").expect(t, "-H follows the command line", "d/ld\nd/ld/t\n", "", 0)
	h.exec("", "find", "-P", "d/ld").expect(t, "-P keeps links", "d/ld\n", "", 0)
	symlink(t, "..", filepath.Join(h.root, "d", "up"))
	h.exec("", "find", "-L", "d", "-name", "t").expect(t, "loop detected", "d/ld/t\nd/up/target/t\n", "find: File system loop detected; 'd/up/d' is part of the same file system loop as 'd'.\n", 1)
}

func TestEffectModesFollowTheCatalog(t *testing.T) {
	h := newHarness(t, nil)
	modes := map[string]bool{}
	for _, name := range h.owners.Commands() {
		for _, effect := range h.owners.specs[name].Effects {
			modes[name+" "+effect.Mode] = true
		}
	}
	for _, c := range []struct {
		argv []string
		mode string
	}{
		{[]string{"grep", "-r", "a"}, "always"},
		{[]string{"rg", "a"}, "always"},
		{[]string{"find", ".", "-print"}, "default-predicates-print"},
		{[]string{"find", ".", "-exec", "printf", "{}", ";"}, "-exec"},
		{[]string{"find", ".", "-exec", "printf", "{}", ";", "-delete"}, "-delete"},
		{[]string{"tree", "-L", "2"}, "always"},
	} {
		prepared, err := h.owners.Prepare(mustPaths(t, h), h.dispatch, requestFor(h, c.argv...))
		testutil.FailErr(t, "prepare", err)
		if got := EffectMode(prepared.Argv); got != c.mode || !modes[prepared.Argv.Command+" "+got] {
			t.Fatalf("%v mode %q, want catalogued %q", c.argv, got, c.mode)
		}
	}
	if !slices.Equal(h.owners.Commands(), []string{"find", "grep", "rg", "tree"}) {
		t.Fatalf("commands %v", h.owners.Commands())
	}
}

// TestFindDeleteUsesTheSharedDeleteOperation runs -delete through the same
// delete operation rm uses, against the real write door.
func TestFindDeleteUsesTheSharedDeleteOperation(t *testing.T) {
	h := newHarness(t, findTree)
	h.owners.Remover = DeleteRemover{Delete: &native.DeleteTool{Boundary: h.boundary}}
	h.exec("", "find", "tree/sub", "-delete").expect(t, "delete", "", "", 0)
	if _, err := os.Lstat(filepath.Join(h.root, "tree", "sub")); !os.IsNotExist(err) {
		t.Fatalf("tree/sub still present: %v", err)
	}
	r := h.exec("", "find", "tree", "-maxdepth", "0", "-delete")
	r.expect(t, "non-empty", "", "find: cannot delete 'tree': Directory not empty\n", 1)
}

func TestFindUserMatchesNumericOwner(t *testing.T) {
	h := newHarness(t, findTree)
	uid := strconv.Itoa(os.Getuid())
	h.exec("", "find", "tree/sub", "-type", "f", "-user", uid).expect(t, "own files", "tree/sub/c.go\ntree/sub/empty.txt\n", "", 0)
	h.exec("", "find", "tree/sub", "-type", "f", "-user", strconv.Itoa(os.Getuid()+1)).expect(t, "other owner", "", "", 0)
	h.exec("", "find", "tree/sub/c.go", "-printf", "%U\\n").expect(t, "printf uid", uid+"\n", "", 0)
	gidRun := h.exec("", "find", "tree/sub/c.go", "-printf", "%G")
	if gidRun.err != nil || gidRun.stdout == "" {
		t.Fatalf("printf gid: %q %v", gidRun.stdout, gidRun.err)
	}
	gid := gidRun.stdout
	h.exec("", "find", "tree/sub", "-type", "f", "-uid", uid, "-gid", gid).expect(t, "uid and gid", "tree/sub/c.go\ntree/sub/empty.txt\n", "", 0)
	h.exec("", "find", "tree/sub/c.go", "-group", gid).expect(t, "group", "tree/sub/c.go\n", "", 0)
}

func TestFindAccessAndChangeAges(t *testing.T) {
	h := newHarness(t, findTree)
	file := filepath.Join(h.root, "tree", "b.txt")
	testutil.FailErr(t, "access time", os.Chtimes(file, fixtureTime.Add(-48*time.Hour), fixtureTime))
	h.exec("", "find", "tree", "-type", "f", "-atime", "+2").expect(t, "atime", "tree/b.txt\n", "", 0)
	h.exec("", "find", "tree", "-type", "f", "-amin", "+3000").expect(t, "amin", "tree/b.txt\n", "", 0)
	h.owners.Now = time.Now
	h.exec("", "find", "tree/b.txt", "-cmin", "-5", "-ctime", "0").expect(t, "ctime", "tree/b.txt\n", "", 0)
}
