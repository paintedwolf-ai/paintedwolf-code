package adapter

import (
	"errors"
	"io/fs"
	"testing"
	"time"
)

func TestBoundedStatReportsAFilesystemThatNeverAnswers(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	_, err := boundedStat("/wedged", time.Millisecond, func(string) (fs.FileInfo, error) {
		<-release
		return nil, nil
	})
	if !errors.Is(err, ErrStatUnanswered) {
		t.Fatalf("err = %v", err)
	}
	if _, err := BoundedLstat(t.TempDir()); err != nil {
		t.Fatalf("answered stat: %v", err)
	}
}
