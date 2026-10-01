package sourceview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/odvcencio/gotreesitter/grammars"
)

func TestCommentsKeepExecutableNeighbors(t *testing.T) {
	for _, tc := range []struct {
		language, source string
		commentLines     []int
		codeLine         int
	}{
		{"powershell", "<#\niex $example\n#>\n# iex $another\niex $command\n", []int{2, 4}, 5},
		{"powershell", "# comment\n\\\n", []int{1}, 2},
		{"perl", "=pod\neval $example;\n=cut\n# eval $another;\neval $command;\n", []int{2, 4}, 5},
		{"groovy", "/* evaluate(params.code) */\n// evaluate(params.code)\nevaluate(params.code)\n", []int{1, 2}, 3},
	} {
		t.Run(tc.language, func(t *testing.T) {
			context, err := ParseNonCode(t.Context(), tc.language, []byte(tc.source))
			testutil.FailErr(t, "parse comments", err)
			for _, line := range tc.commentLines {
				if !context.Contains(line, 2) {
					t.Errorf("comment line %d not recognized", line)
				}
			}
			if context.Contains(tc.codeLine, 2) {
				t.Fatal("comment swallowed executable neighbor")
			}
			if context.Contains(999, 1) {
				t.Fatal("out-of-bounds location treated as comment")
			}
		})
	}
	context, err := ParseNonCode(t.Context(), "powershell", []byte("Write-Output '# text'\n"))
	testutil.FailErr(t, "quoted hash", err)
	if !context.Contains(1, strings.Index("Write-Output '# text'", "#")+1) {
		t.Fatal("literal string was treated as executable code")
	}
}

func TestMalformedContextCannotSuppressFindings(t *testing.T) {
	source := []byte("eval(user_input)\nif (\n")
	if _, err := ParseNonCode(t.Context(), "python", source); err == nil {
		grammar := grammars.DetectLanguageByName("python").Language()
		tree, parseErr := tsparse.Parse(context.Background(), grammar, source, tsparse.Analysis)
		testutil.FailErr(t, "inspect malformed parse", parseErr)
		defer tree.Release()
		t.Fatalf("accepted incomplete context: stop=%s bytes=%d tree=%s", tree.ParseStopReason(), tree.RootNode().EndByte(), tree.RootNode().SExpr(grammar))
	}
}

func TestReadNonCodeRecognizesPerlCGIShebang(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, "example.cgi")
	source := "#!/usr/bin/perl\n# eval $example;\neval $command;\n"
	testutil.FailErr(t, "write CGI fixture", os.WriteFile(path, []byte(source), 0o600))
	comments, err := ReadNonCode(t.Context(), project, path)
	testutil.FailErr(t, "read CGI comments", err)
	if !comments.Contains(2, 3) || comments.Contains(3, 1) {
		t.Fatal("CGI shebang did not preserve comment and code boundaries")
	}
}

func TestLiteralTextAndExecutableInterpolation(t *testing.T) {
	for _, tc := range []struct {
		language, source, needle string
		nonCode                  bool
	}{
		{"powershell", "Write-Output '[System.Net.ServicePointManager]::ServerCertificateValidationCallback = { $true }'", "[System", true},
		{"powershell", "Write-Output \"iex ((New-Object Net.WebClient).DownloadString($url))\"", "iex", true},
		{"powershell", "Write-Output \"$(iex ((New-Object Net.WebClient).DownloadString($url)))\"", "iex", false},
		{"groovy", "println 'evaluate(params.code)'", "evaluate", true},
		{"groovy", "println \"${evaluate(params.code)}\"", "evaluate", false},
		{"perl", "print 'eval(CGI::param(\"code\"));';", "eval", true},
	} {
		t.Run(tc.language+tc.source, func(t *testing.T) {
			context, err := ParseNonCode(t.Context(), tc.language, []byte(tc.source))
			testutil.FailErr(t, "parse source context", err)
			if got := context.Contains(1, strings.Index(tc.source, tc.needle)+1); got != tc.nonCode {
				t.Fatalf("non-code=%v, want %v for %q", got, tc.nonCode, tc.source)
			}
		})
	}
}

func TestNonCodeRequiresEntireMatch(t *testing.T) {
	source := "# dangerous text\nexecute(input)\n"
	context, err := ParseNonCode(t.Context(), "python", []byte(source))
	testutil.FailErr(t, "parse comment boundary", err)
	if !context.CoversSpan(1, 3, 1, 17) {
		t.Fatal("comment span was executable")
	}
	for _, span := range [][4]int{{1, 3, 2, 15}, {1, 3, 0, 0}, {1, 3, 1, 99}, {1, 3, 1, 2}, {2, 1, 2, 15}} {
		if context.CoversSpan(span[0], span[1], span[2], span[3]) {
			t.Fatalf("suppressed unproven non-code span %v", span)
		}
	}
}

func TestPowerShellCommentOnlySourceContext(t *testing.T) {
	for _, source := range []string{
		"# iex ((New-Object Net.WebClient).DownloadString($url))\n",
		"# [System.Net.ServicePointManager]::ServerCertificateValidationCallback = { $true }\n",
		"# Write-Output \"$(iex ((New-Object Net.WebClient).DownloadString($url)))\"\n",
		"<# iex $example #>\n",
		"# comment\r\n",
		"# comment",
	} {
		comments, err := ParseNonCode(t.Context(), "powershell", []byte(source))
		testutil.FailErr(t, "parse comment-only source", err)
		if !comments.CoversSpan(1, 1, 1, len(strings.TrimRight(source, "\r\n"))+1) {
			t.Fatalf("comment-only source did not establish comment coverage: %q", source)
		}
	}
}

func TestPowerShellCommentsDoNotHideBrokenCode(t *testing.T) {
	for _, source := range []string{
		"# comment\n$x = (\n",
		"<# unterminated\n",
		"# comment\n}\n",
	} {
		if _, err := ParseNonCode(t.Context(), "powershell", []byte(source)); err == nil {
			t.Fatalf("broken source received complete comment context: %q", source)
		}
	}
}
