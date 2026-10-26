package literal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// formFiles is the fixture tree every form case runs against. Expected bytes
// were observed from GNU coreutils 8.32, diffutils 3.8, xxd and util-linux
// hexdump over the same files, except where the catalog fixes a different
// representation (file's corpus and malformed manifests).
var formFiles = map[string]string{
	"input": "alpha\nbeta", "lines": "a\nb\n\n\n\nc\td\x01\x7f\xc3\xa9\xff\n", "crlf": "one\r\ntwo\r\n",
	"empty": "", "same-a": "same\n", "same-b": "same\n",
	"old": "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n", "new": "a\nB\nc\nd\ne\nf\ng\nh\nI\nj\nk\n",
	"noeol-a": "x\ny", "noeol-b": "x\nz", "ws-a": "a  b\nc\n", "ws-b": "a b \nc\n", "blank-a": "a\nb\n", "blank-b": "a\n\nb\n",
	"bin-a": "\x00\x01", "bin-b": "\x00\x02", "words": "  hello  world\n\tfoo\vbar\n", "utf8": "héllo wörld\nñ\n",
	"ten":      "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n",
	"tree-a/f": "1\n", "tree-b/f": "2\n", "tree-a/sub/g": "s\n", "tree-b/sub/g": "t\n", "tree-b/extra": "e\n", "tree-a/only-a/": "",
	"dir/a": "a", "dir/b": "bb", "dir/exe": "#!", "dir/.h": "", "dir/.hidden-dir/": "",
}

func formHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, formFiles)
	testutil.FailErr(t, "mode exe", os.Chmod(filepath.Join(h.root, "dir", "exe"), 0o755))
	newer := fixtureTime.Add(55 * time.Second)
	testutil.FailErr(t, "newer b", os.Chtimes(filepath.Join(h.root, "dir", "b"), newer, newer))
	linkAt(t, "input", filepath.Join(h.root, "link"), fixtureTime)
	linkAt(t, "a", filepath.Join(h.root, "dir", "link"), fixtureTime)
	for _, dir := range []string{"dir", "tree-a", "tree-b", "tree-a/sub", "tree-b/sub", "tree-a/only-a", "dir/.hidden-dir"} {
		testutil.FailErr(t, "dir time", os.Chtimes(filepath.Join(h.root, dir), fixtureTime, fixtureTime))
	}
	return h
}

type formCase struct {
	argv   string
	stdin  string
	stdout string
	stderr string
	status int
}

const stamp = "\t2026-01-02 03:04:05.000000000 +0000"

var formCases = []formCase{
	{argv: "cat -n lines", stdout: "     1\ta\n     2\tb\n     3\t\n     4\t\n     5\t\n     6\tc\td\x01\x7f\xc3\xa9\xff\n"},
	{argv: "cat -b lines", stdout: "     1\ta\n     2\tb\n\n\n\n     3\tc\td\x01\x7f\xc3\xa9\xff\n"},
	{argv: "cat -s lines", stdout: "a\nb\n\nc\td\x01\x7f\xc3\xa9\xff\n"},
	{argv: "cat -A lines", stdout: "a$\nb$\n$\n$\n$\nc^Id^A^?M-CM-)M-^?$\n"},
	{argv: "cat --show-all crlf", stdout: "one^M$\ntwo^M$\n"},
	{argv: "cat -v lines", stdout: "a\nb\n\n\n\nc\td^A^?M-CM-)M-^?\n"},
	{argv: "cat -E crlf", stdout: "one\r$\ntwo\r$\n"},
	{argv: "cat -T lines", stdout: "a\nb\n\n\n\nc^Id\x01\x7f\xc3\xa9\xff\n"},
	{argv: "cat -n input empty input", stdout: "     1\talpha\n     2\tbetaalpha\n     3\tbeta"},
	{argv: "cat - input", stdin: "from stdin\n", stdout: "from stdin\nalpha\nbeta"},
	{argv: "cat bin-a", stdout: "\x00\x01"},
	{argv: "head -n 2 lines", stdout: "a\nb\n"},
	{argv: "head --lines=2 lines", stdout: "a\nb\n"},
	{argv: "head -3 ten", stdout: "1\n2\n3\n"},
	{argv: "head ten", stdout: "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"},
	{argv: "head -c 3 input", stdout: "alp"},
	{argv: "head input empty", stdout: "==> input <==\nalpha\nbeta\n==> empty <==\n"},
	{argv: "head -q input empty", stdout: "alpha\nbeta"},
	{argv: "head -v input", stdout: "==> input <==\nalpha\nbeta"},
	{argv: "head -n 0 input", stdout: ""},
	{argv: "head -n 1", stdin: "a\nb\n", stdout: "a\n"},
	{argv: "head -c 2", stdin: "abc", stdout: "ab"},
	{argv: "tail -n 1 input", stdout: "beta"},
	{argv: "tail -n +2 lines", stdout: "b\n\n\n\nc\td\x01\x7f\xc3\xa9\xff\n"},
	{argv: "tail -3 ten", stdout: "13\n14\n15\n"},
	{argv: "tail -c 3 input", stdout: "eta"},
	{argv: "tail -c +3 input", stdout: "pha\nbeta"},
	{argv: "tail input empty", stdout: "==> input <==\nalpha\nbeta\n==> empty <==\n"},
	{argv: "tail -n 2", stdin: "1\n2\n3\n", stdout: "2\n3\n"},
	{argv: "nl lines", stdout: "     1\ta\n     2\tb\n       \n       \n       \n     3\tc\td\x01\x7f\xc3\xa9\xff\n"},
	{argv: "nl -ba -v 5 -w 3 -s : lines", stdout: "  5:a\n  6:b\n  7:\n  8:\n  9:\n 10:c\td\x01\x7f\xc3\xa9\xff\n"},
	{argv: "nl input", stdout: "     1\talpha\n     2\tbeta\n"},
	{argv: "wc -l input", stdout: "1 input\n"},
	{argv: "wc input", stdout: " 1  2 10 input\n"},
	{argv: "wc -l crlf", stdout: "2 crlf\n"},
	{argv: "wc -w words", stdout: "4 words\n"},
	{argv: "wc -c utf8", stdout: "17 utf8\n"},
	{argv: "wc -m utf8", stdout: "14 utf8\n"},
	{argv: "wc -L utf8", stdout: "11 utf8\n"},
	{argv: "wc -L crlf", stdout: "3 crlf\n"},
	{argv: "wc -lwc input crlf", stdout: " 1  2 10 input\n 2  2 10 crlf\n 3  4 20 total\n"},
	{argv: "wc -l input ten", stdout: " 1 input\n15 ten\n16 total\n"},
	{argv: "wc input ten", stdout: " 1  2 10 input\n15 15 36 ten\n16 17 46 total\n"},
	{argv: "wc -l", stdin: "a\nb", stdout: "1\n"},
	{argv: "wc -", stdin: "a\nb\n", stdout: "      2       2       4 -\n"},
	{argv: "wc missing input", stdout: " 1  2 10 input\n 1  2 10 total\n", stderr: "wc: missing: No such file or directory\n", status: 1},
	{argv: "wc -m lines", stdout: "", stderr: "wc: lines: invalid UTF-8 encoding\n", status: 1},
	{argv: "ls dir", stdout: "a\nb\nexe\nlink\n"},
	{argv: "ls -a dir", stdout: ".\n..\n.h\n.hidden-dir\na\nb\nexe\nlink\n"},
	{argv: "ls -A dir", stdout: ".h\n.hidden-dir\na\nb\nexe\nlink\n"},
	{argv: "ls -1 dir", stdout: "a\nb\nexe\nlink\n"},
	{argv: "ls -d dir", stdout: "dir\n"},
	{argv: "ls -F dir", stdout: "a\nb\nexe*\nlink@\n"},
	{argv: "ls -R tree-a", stdout: "tree-a:\nf\nonly-a\nsub\n\ntree-a/only-a:\n\ntree-a/sub:\ng\n"},
	{argv: "ls -t dir", stdout: "b\na\nexe\nlink\n"},
	{argv: "ls -S dir", stdout: "b\nexe\na\nlink\n"},
	{argv: "ls -r dir", stdout: "link\nexe\nb\na\n"},
	{argv: "ls -tr dir", stdout: "link\nexe\na\nb\n"},
	{argv: "ls missing", stderr: "ls: cannot access 'missing': No such file or directory\n", status: 2},
	{argv: "ls input dir", stdout: "input\n\ndir:\na\nb\nexe\nlink\n"},
	{argv: "ls link", stdout: "link\n"},
	{argv: "ls -F link", stdout: "link@\n"},
	{argv: "ls -p tree-a", stdout: "f\nonly-a/\nsub/\n"},
	{argv: "ls -pF dir", stdout: "a\nb\nexe*\nlink@\n"},
	{argv: "ls --group-directories-first tree-a", stdout: "only-a\nsub\nf\n"},
	{argv: "ls --group-directories-first -r tree-a", stdout: "sub\nonly-a\nf\n"},
	{argv: "ls -U dir", stdout: "a\nb\nexe\nlink\n"},
	{argv: "ls -Ut dir", stdout: "a\nb\nexe\nlink\n"},
	{argv: "ls -L dir", stdout: "a\nb\nexe\nlink\n"},
	{argv: "ls -LF dir", stdout: "a\nb\nexe*\nlink\n"},
	{argv: "ls --color=never dir", stdout: "a\nb\nexe\nlink\n"},
	{argv: "ls --color=auto dir", stdout: "a\nb\nexe\nlink\n"},
	{argv: "ls --color dir", stdout: "a\nb\nexe\nlink\n"},
	{argv: "ls -G dir", stdout: "a\nb\nexe\nlink\n"},
	{argv: "ls -m dir", stdout: "a, b, exe, link\n"},
	{argv: "ls -mp tree-a", stdout: "f, only-a/, sub/\n"},
	{argv: "ls -f dir", stdout: ".\n..\n.h\n.hidden-dir\na\nb\nexe\nlink\n"},
	{argv: "ls -k dir", stdout: "a\nb\nexe\nlink\n"},
	{argv: "ls -P link", stdout: "link\n"},
	{argv: "ls -H -F link", stdout: "link\n"},
	{argv: "stat -c %n|%s|%a|%A|%F|%Y input", stdout: "input|10|644|-rw-r--r--|regular file|1767323045\n"},
	{argv: "stat -f %N|%z|%Lp|%Sp|%HT|%m input", stdout: "input|10|644|-rw-r--r--|Regular File|1767323045\n"},
	{argv: "stat -c %F empty", stdout: "regular empty file\n"},
	{argv: "stat -c %F link", stdout: "symbolic link\n"},
	{argv: "stat -f %HT link", stdout: "Symbolic Link\n"},
	{argv: "stat -L -c %F link", stdout: "regular file\n"},
	{argv: "stat --dereference -c %s link", stdout: "10\n"},
	{argv: "stat -c %s link", stdout: "5\n"},
	{argv: "stat -c %%%s input", stdout: "%10\n"},
	{argv: "stat -f %%%z input", stdout: "%10\n"},
	{argv: "stat -c %F dir", stdout: "directory\n"},
	{argv: "stat --format=%s input", stdout: "10\n"},
	{argv: "stat -c %s missing input", stdout: "10\n", stderr: "stat: cannot stat 'missing': No such file or directory\n", status: 1},
	{argv: "diff same-a same-b"},
	{argv: "diff -u old new", status: 1, stdout: "--- old" + stamp + "\n+++ new" + stamp + "\n@@ -1,10 +1,11 @@\n a\n-b\n+B\n c\n d\n e\n f\n g\n h\n-i\n+I\n j\n+k\n"},
	{argv: "diff -U 1 old new", status: 1, stdout: "--- old" + stamp + "\n+++ new" + stamp + "\n@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n@@ -8,3 +8,4 @@\n h\n-i\n+I\n j\n+k\n"},
	{argv: "diff -U1 old new", status: 1, stdout: "--- old" + stamp + "\n+++ new" + stamp + "\n@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n@@ -8,3 +8,4 @@\n h\n-i\n+I\n j\n+k\n"},
	{argv: "diff -q old new", status: 1, stdout: "Files old and new differ\n"},
	{argv: "diff -u noeol-a noeol-b", status: 1, stdout: "--- noeol-a" + stamp + "\n+++ noeol-b" + stamp + "\n@@ -1,2 +1,2 @@\n x\n-y\n\\ No newline at end of file\n+z\n\\ No newline at end of file\n"},
	{argv: "diff -u -w ws-a ws-b"},
	{argv: "diff -u -b ws-a ws-b"},
	{argv: "diff -u -B blank-a blank-b"},
	{argv: "diff -u blank-a blank-b", status: 1, stdout: "--- blank-a" + stamp + "\n+++ blank-b" + stamp + "\n@@ -1,2 +1,3 @@\n a\n+\n b\n"},
	{argv: "diff bin-a bin-b", status: 1, stdout: "Binary files bin-a and bin-b differ\n"},
	{argv: "diff bin-a bin-a"},
	{argv: "diff -u missing input", status: 2, stderr: "diff: missing: No such file or directory\n"},
	{argv: "diff -u empty input", status: 1, stdout: "--- empty" + stamp + "\n+++ input" + stamp + "\n@@ -0,0 +1,2 @@\n+alpha\n+beta\n\\ No newline at end of file\n"},
	{argv: "diff -u input empty", status: 1, stdout: "--- input" + stamp + "\n+++ empty" + stamp + "\n@@ -1,2 +0,0 @@\n-alpha\n-beta\n\\ No newline at end of file\n"},
	{argv: "diff -r -u tree-a tree-b", status: 1, stdout: "Only in tree-b: extra\ndiff -r -u tree-a/f tree-b/f\n--- tree-a/f" + stamp + "\n+++ tree-b/f" + stamp + "\n@@ -1 +1 @@\n-1\n+2\nOnly in tree-a: only-a\ndiff -r -u tree-a/sub/g tree-b/sub/g\n--- tree-a/sub/g" + stamp + "\n+++ tree-b/sub/g" + stamp + "\n@@ -1 +1 @@\n-s\n+t\n"},
	{argv: "diff -u tree-a tree-b", status: 1, stdout: "Only in tree-b: extra\ndiff -u tree-a/f tree-b/f\n--- tree-a/f" + stamp + "\n+++ tree-b/f" + stamp + "\n@@ -1 +1 @@\n-1\n+2\nOnly in tree-a: only-a\nCommon subdirectories: tree-a/sub and tree-b/sub\n"},
	{argv: "diff -q tree-a tree-b", status: 1, stdout: "Only in tree-b: extra\nFiles tree-a/f and tree-b/f differ\nOnly in tree-a: only-a\nCommon subdirectories: tree-a/sub and tree-b/sub\n"},
	{argv: "diff -q old tree-a", status: 2, stderr: "diff: tree-a/old: No such file or directory\n"},
	{argv: "file -b empty", stdout: "empty\n"},
	{argv: "file input", stdout: "input: ASCII text\n"},
	{argv: "file -b -i input", stdout: "text/plain; charset=us-ascii\n"},
	{argv: "file --mime input", stdout: "input: text/plain; charset=us-ascii\n"},
	{argv: "file dir", stdout: "dir: directory\n"},
	{argv: "file -i dir", stdout: "dir: inode/directory; charset=binary\n"},
	{argv: "file link", stdout: "link: symbolic link to input\n"},
	{argv: "file -b bin-a", stdout: "data\n"},
	{argv: "file -b lines", stdout: "data\n"},
	{argv: "xxd lines", stdout: "00000000: 610a 620a 0a0a 0a63 0964 017f c3a9 ff0a  a.b....c.d......\n"},
	{argv: "xxd -g 1 input", stdout: "00000000: 61 6c 70 68 61 0a 62 65 74 61                    alpha.beta\n"},
	{argv: "xxd -g 4 lines", stdout: "00000000: 610a620a 0a0a0a63 0964017f c3a9ff0a  a.b....c.d......\n"},
	{argv: "xxd -p lines", stdout: "610a620a0a0a0a630964017fc3a9ff0a\n"},
	{argv: "xxd -l 4 -s 2 lines", stdout: "00000002: 620a 0a0a                                b...\n"},
	{argv: "xxd -s -3 lines", stdout: "0000000d: a9ff 0a                                  ...\n"},
	{argv: "xxd empty", stdout: ""},
	{argv: "xxd -p", stdin: "A", stdout: "41\n"},
	{argv: "hexdump -C input", stdout: "00000000  61 6c 70 68 61 0a 62 65  74 61                    |alpha.beta|\n0000000a\n"},
	{argv: "hexdump -C -n 4 -s 2 lines", stdout: "00000002  62 0a 0a 0a                                       |b...|\n00000006\n"},
	{argv: "hexdump -C empty", stdout: ""},
	{argv: "hexdump -C", stdin: strings.Repeat("a", 50), stdout: "00000000  61 61 61 61 61 61 61 61  61 61 61 61 61 61 61 61  |aaaaaaaaaaaaaaaa|\n*\n00000030  61 61                                             |aa|\n00000032\n"},
	{argv: "sha256sum empty input", stdout: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855  empty\nbbfb79e82216bd2db1ad2c507d44ddf80aeb12f64f9562056afe93aad43154d9  input\n"},
	{argv: "sha1sum empty", stdout: "da39a3ee5e6b4b0d3255bfef95601890afd80709  empty\n"},
	{argv: "md5sum empty", stdout: "d41d8cd98f00b204e9800998ecf8427e  empty\n"},
	{argv: "sha512sum empty", stdout: "cf83e1357eefb8bdf1542850d66d8007d620e4050b5715dc83f4a921d36ce9ce47d0d13c5d85f2b0ff8318d2877eec2f63b931bd47417a81a538327af927da3e  empty\n"},
	{argv: "shasum -a 256 input", stdout: "bbfb79e82216bd2db1ad2c507d44ddf80aeb12f64f9562056afe93aad43154d9  input\n"},
	{argv: "shasum -a 512 empty", stdout: "cf83e1357eefb8bdf1542850d66d8007d620e4050b5715dc83f4a921d36ce9ce47d0d13c5d85f2b0ff8318d2877eec2f63b931bd47417a81a538327af927da3e  empty\n"},
	{argv: "shasum empty", stdout: "da39a3ee5e6b4b0d3255bfef95601890afd80709  empty\n"},
	{argv: "sha256sum", stdin: "", stdout: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855  -\n"},
	{argv: "base64 lines", stdout: "YQpiCgoKCmMJZAF/w6n/Cg==\n"},
	{argv: "base64 -w 4 input", stdout: "YWxw\naGEK\nYmV0\nYQ==\n"},
	{argv: "base64 --wrap=4 input", stdout: "YWxw\naGEK\nYmV0\nYQ==\n"},
	{argv: "base64 -b 4 input", stdout: "YWxw\naGEK\nYmV0\nYQ==\n"},
	{argv: "base64 -w 0 input", stdout: "YWxwaGEKYmV0YQ=="},
	{argv: "base64 -b 0 input", stdout: "YWxwaGEKYmV0YQ=="},
	{argv: "base64 empty", stdout: ""},
	{argv: "base64 -d", stdin: "QU\nJD\n", stdout: "ABC"},
	{argv: "base64 --decode", stdin: " QUJD\t", stdout: "ABC"},
	{argv: "base64 -D", stdin: "QUJD", stdout: "ABC"},
	{argv: "base64 -d", stdin: "QQ=", stderr: "base64: invalid input\n", status: 1},
	{argv: "base64 -d", stdin: "Q!Q=", stderr: "base64: invalid input\n", status: 1},
}

// TestLiteralFormsProduceExactBytes runs every catalogued form and dialect
// against real files and compares stdout, stderr and status byte for byte.
func TestLiteralFormsProduceExactBytes(t *testing.T) {
	h := formHarness(t)
	for _, c := range formCases {
		t.Run(c.argv, func(t *testing.T) {
			h.exec(c.stdin, strings.Fields(c.argv)...).expect(t, c.argv, c.stdout, c.stderr, c.status)
		})
	}
}

func TestHumanSizesRoundUpLikeGNU(t *testing.T) {
	for size, want := range map[int64]string{0: "0", 1023: "1023", 1024: "1.0K", 1025: "1.1K", 10239: "10K", 10240: "10K", 10241: "11K", 1048575: "1.0M", 1 << 30: "1.0G"} {
		if got := humanSize(size); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", size, got, want)
		}
	}
}
