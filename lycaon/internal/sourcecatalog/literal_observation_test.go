package sourcecatalog

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFileBloomCannotPublishAcrossAnObservedWrite(t *testing.T) {
	for _, scope := range []string{"relative", "absolute", "root"} {
		t.Run(scope, func(t *testing.T) {
			root := t.TempDir()
			writeLiteralFixture(t, root, "a.txt", "old text")
			info, err := os.Stat(filepath.Join(root, "a.txt"))
			testutil.FailErr(t, "stat observation", err)
			entry := Entry{Path: "a.txt", Size: info.Size(), Mode: uint32(info.Mode()), Modified: info.ModTime()}
			cache := newLiteralIndexCache()
			entered, resume := make(chan struct{}), make(chan struct{})
			var release sync.Once
			defer release.Do(func() { close(resume) })
			done := make(chan error, 1)
			go func() {
				_, _, readErr := cache.fileBloom(t.Context(), root, entry, func(Entry) (io.ReadCloser, error) {
					close(entered)
					<-resume
					return io.NopCloser(strings.NewReader("old text")), nil
				})
				done <- readErr
			}()
			<-entered
			writeLiteralFixture(t, root, "a.txt", "new text")
			testutil.FailErr(t, "preserve metadata", os.Chtimes(filepath.Join(root, "a.txt"), info.ModTime(), info.ModTime()))
			paths := []string{"a.txt"}
			if scope == "absolute" {
				paths[0] = filepath.Join(root, "a.txt")
			}
			if scope == "root" {
				paths = nil
			}
			cache.invalidate(root, paths)
			release.Do(func() { close(resume) })
			testutil.FailErr(t, "finish old read", <-done)
			if _, _, cached := cache.cachedBloom(root, entry); cached {
				t.Fatal("published bytes from before the observed write")
			}
		})
	}
}
