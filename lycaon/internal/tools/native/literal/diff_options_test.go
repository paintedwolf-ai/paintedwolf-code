package literal

import "testing"

const (
	fixtureStamp = "\t2026-01-02 03:04:05.000000000 +0000"
	epochStamp   = "\t1970-01-01 00:00:00.000000000 +0000"
)

// The trees hold a shared file, a removed file, an added file, an added
// directory, differing object files and a CRLF/LF pair; every mtime is the
// fixture time.
var addedRemovedTrees = map[string]string{
	"a/same": "x\n", "b/same": "x\n", "a/removed": "gone\n", "b/added": "new\n",
	"b/newdir/g": "n\n", "a/only/f": "deep\n", "a/x.o": "o\n", "b/x.o": "p\n",
	"a/crlf": "crlf\r\nline\r\n", "b/crlf": "crlf\nline\n", "a/empty/": "",
}

func TestDiffNewFileReportsAddedAndRemovedFilesAsFullHunks(t *testing.T) {
	h := newHarness(t, addedRemovedTrees)
	want := "diff -ruN -x '*.o' a/added b/added\n--- a/added" + epochStamp + "\n+++ b/added" + fixtureStamp + "\n@@ -0,0 +1 @@\n+new\n" +
		"diff -ruN -x '*.o' a/crlf b/crlf\n--- a/crlf" + fixtureStamp + "\n+++ b/crlf" + fixtureStamp + "\n@@ -1,2 +1,2 @@\n-crlf\r\n-line\r\n+crlf\n+line\n" +
		"diff -ruN -x '*.o' a/newdir/g b/newdir/g\n--- a/newdir/g" + epochStamp + "\n+++ b/newdir/g" + fixtureStamp + "\n@@ -0,0 +1 @@\n+n\n" +
		"diff -ruN -x '*.o' a/only/f b/only/f\n--- a/only/f" + fixtureStamp + "\n+++ b/only/f" + epochStamp + "\n@@ -1 +0,0 @@\n-deep\n" +
		"diff -ruN -x '*.o' a/removed b/removed\n--- a/removed" + fixtureStamp + "\n+++ b/removed" + epochStamp + "\n@@ -1 +0,0 @@\n-gone\n"
	h.exec("", "diff", "-ruN", "-x", "*.o", "a", "b").expect(t, "diff -ruN -x", want, "", 1)
	h.exec("", "diff", "-rN", "--strip-trailing-cr", "--exclude=*.o", "--exclude", "only", "a", "b").expect(t, "strip CR",
		"diff -rN --strip-trailing-cr '--exclude=*.o' --exclude only a/added b/added\n--- a/added"+epochStamp+"\n+++ b/added"+fixtureStamp+"\n@@ -0,0 +1 @@\n+new\n"+
			"diff -rN --strip-trailing-cr '--exclude=*.o' --exclude only a/newdir/g b/newdir/g\n--- a/newdir/g"+epochStamp+"\n+++ b/newdir/g"+fixtureStamp+"\n@@ -0,0 +1 @@\n+n\n"+
			"diff -rN --strip-trailing-cr '--exclude=*.o' --exclude only a/removed b/removed\n--- a/removed"+fixtureStamp+"\n+++ b/removed"+epochStamp+"\n@@ -1 +0,0 @@\n-gone\n", "", 1)
	h.exec("", "diff", "-N", "a", "b").expect(t, "diff -N without -r", "diff -N a/added b/added\n--- a/added"+epochStamp+"\n+++ b/added"+fixtureStamp+"\n@@ -0,0 +1 @@\n+new\n"+
		"diff -N a/crlf b/crlf\n--- a/crlf"+fixtureStamp+"\n+++ b/crlf"+fixtureStamp+"\n@@ -1,2 +1,2 @@\n-crlf\r\n-line\r\n+crlf\n+line\n"+
		"Common subdirectories: a/empty and b/empty\nCommon subdirectories: a/newdir and b/newdir\nCommon subdirectories: a/only and b/only\n"+
		"diff -N a/removed b/removed\n--- a/removed"+fixtureStamp+"\n+++ b/removed"+epochStamp+"\n@@ -1 +0,0 @@\n-gone\n"+
		"diff -N a/x.o b/x.o\n--- a/x.o"+fixtureStamp+"\n+++ b/x.o"+fixtureStamp+"\n@@ -1 +1 @@\n-o\n+p\n", "", 1)
}

func TestDiffExclusionsAndAbsentOperands(t *testing.T) {
	files := map[string]string{"ignore.lst": "*.o\nonly\nnewdir\n"}
	for name, content := range addedRemovedTrees {
		files[name] = content
	}
	h := newHarness(t, files)
	h.exec("", "diff", "-rq", "-X", "ignore.lst", "a", "b").expect(t, "diff -X",
		"Only in b: added\nFiles a/crlf and b/crlf differ\nOnly in a: empty\nOnly in a: removed\n", "", 1)
	h.exec("", "diff", "-x", "crlf", "a/crlf", "b/crlf").expect(t, "operands are never excluded",
		"--- a/crlf"+fixtureStamp+"\n+++ b/crlf"+fixtureStamp+"\n@@ -1,2 +1,2 @@\n-crlf\r\n-line\r\n+crlf\n+line\n", "", 1)
	h.exec("", "diff", "--strip-trailing-cr", "a/crlf", "b/crlf").expect(t, "strip CR operands", "", "", 0)
	h.exec("", "diff", "-uN", "a/removed", "b/removed").expect(t, "absent right operand",
		"--- a/removed"+fixtureStamp+"\n+++ b/removed"+epochStamp+"\n@@ -1 +0,0 @@\n-gone\n", "", 1)
	h.exec("", "diff", "--new-file", "a/nope", "b/added").expect(t, "absent left operand",
		"--- a/nope"+epochStamp+"\n+++ b/added"+fixtureStamp+"\n@@ -0,0 +1 @@\n+new\n", "", 1)
	h.exec("", "diff", "-N", "a/nope", "b/nope").expect(t, "both absent", "",
		"diff: a/nope: No such file or directory\ndiff: b/nope: No such file or directory\n", 2)
	h.exec("", "diff", "-u", "a/nope", "b/added").expect(t, "absent without -N", "", "diff: a/nope: No such file or directory\n", 2)
}
