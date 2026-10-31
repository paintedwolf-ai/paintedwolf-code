package literal

import "testing"

func TestWcColumnsAndTabWidthMatchGNU(t *testing.T) {
	h := newHarness(t, map[string]string{"notes": "alpha line 1\nbeta line 2\n\nalpha line 4\ngamma\twith\ttabs\n"})
	h.exec("", "wc", "-l", "-w", "-c", "notes").expect(t, "file columns", " 5 12 55 notes\n", "", 0)
	h.exec("", "wc", "-m", "-L", "notes").expect(t, "display columns", "55 20 notes\n", "", 0)
	h.exec("a b\n", "wc", "-lwc").expect(t, "pipe columns", "      1       2       4\n", "", 0)
}

func TestFileReportsTextCharsetAndRequiresCompletePNGHeader(t *testing.T) {
	h := newHarness(t, map[string]string{
		"ascii":  "plain\n",
		"utf8":   "héllo\n",
		"prefix": "\x89PNG\r\n\x1a\n\x00\x01\x02\x03",
	})
	h.exec("", "file", "-b", "ascii", "prefix").expect(t, "descriptions", "ASCII text\ndata\n", "", 0)
	h.exec("", "file", "-bi", "ascii", "utf8", "prefix").expect(t, "MIME charsets", "text/plain; charset=us-ascii\ntext/plain; charset=utf-8\napplication/octet-stream; charset=binary\n", "", 0)
}
