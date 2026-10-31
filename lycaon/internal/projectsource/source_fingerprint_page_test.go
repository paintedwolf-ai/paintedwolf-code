package projectsource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestFingerprintReadersOverlapAndJoinBeforeReleasingTheirArena(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", interrupted), func(t *testing.T) {
			dir := t.TempDir()
			var names [sourceFingerprintReaders]string
			var infos [sourceFingerprintReaders]fs.FileInfo
			var expected bytes.Buffer
			var totalBytes int64
			for i := range names {
				names[i] = fmt.Sprintf("f%d", i)
				body := []byte{byte(i), 0, 255}
				testutil.FailErr(t, "seed actual fingerprint input", os.WriteFile(filepath.Join(dir, names[i]), body, 0o640))
				info, err := os.Lstat(filepath.Join(dir, names[i]))
				testutil.FailErr(t, "observe selected file", err)
				infos[i] = info
				_, _ = fmt.Fprintf(&expected, "%s\x00%d\x00", names[i], uint32(info.Mode()))
				sha := sha256.Sum256(body)
				_, _ = expected.Write(sha[:])
				totalBytes += int64(len(body))
			}
			root, err := os.OpenRoot(dir)
			testutil.FailErr(t, "hold actual input root", err)
			defer func() { testutil.FailErr(t, "close joined root", root.Close()) }()
			memory := testSourceOperationMemory(t)
			releaseWorking, err := memory.Reserve(t.Context(), 36<<10)
			testutil.FailErr(t, "admit shared read arena", err)
			defer func() {
				if releaseWorking != nil {
					releaseWorking()
				}
			}()
			arena := make([]byte, 32<<10)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var reports []SourceProgress
			ctx = WithSourceProgress(ctx, func(p SourceProgress) { reports = append(reports, p) })
			progress := newSourceWorkProgress(ctx, "verifying")
			progress.last = time.Time{}
			allocation := &sourceTreeAllocation{known: true, memory: memory}
			defer func() { testutil.FailErr(t, "close allocation observer", allocation.close()) }()
			started := make(chan string, sourceFingerprintReaders)
			barrier := make(chan struct{})
			var once sync.Once
			releaseReaders := func() { once.Do(func() { close(barrier) }) }
			var output bytes.Buffer
			page, err := newSourceFingerprintPage(ctx, &output, func(rel string) (*os.File, error) {
				started <- rel
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-barrier:
					return root.Open(rel)
				}
			}, root.Readlink, progress, allocation, arena)
			testutil.FailErr(t, "construct admitted fingerprint page", err)
			done := make(chan error, 1)
			consumed := false
			defer func() {
				cancel()
				releaseReaders()
				if !consumed {
					<-done
				}
				page.Close()
			}()
			go func() {
				for i, name := range names {
					if err := page.Add(name, infos[i]); err != nil {
						done <- err
						return
					}
				}
				done <- page.Flush()
			}()
			seen := make(map[string]bool, sourceFingerprintReaders)
			for range sourceFingerprintReaders {
				select {
				case name := <-started:
					seen[name] = true
				case <-time.After(5 * time.Second):
					t.Fatal("bounded readers did not start before earlier input completed")
				}
			}
			if len(seen) != sourceFingerprintReaders {
				t.Fatalf("actual started inputs=%v", seen)
			}
			if interrupted {
				cancel()
			} else {
				releaseReaders()
			}
			err = <-done
			consumed = true
			if interrupted {
				if !errors.Is(err, context.Canceled) || output.Len() != 0 {
					t.Fatalf("canceled fingerprint output=%x err=%v", output.Bytes(), err)
				}
			} else {
				testutil.FailErr(t, "join actual fingerprint inputs", err)
				if !bytes.Equal(output.Bytes(), expected.Bytes()) {
					t.Fatalf("canonical framing changed: actual=%x expected=%x", output.Bytes(), expected.Bytes())
				}
				progress.report(true)
				last := reports[len(reports)-1]
				if last.Bytes != totalBytes || last.Entries != sourceFingerprintReaders {
					t.Fatalf("observed progress=%+v", last)
				}
			}
			page.Close()
			releaseWorking()
			releaseWorking = nil
			if used := memory.InUse(); used != (toolcontract.ProgramMemoryUsage{}) {
				t.Fatalf("joined fingerprint retained memory: %+v", used)
			}
		})
	}
}

func TestFingerprintPageRequiresItsArenaAndMetadataAdmission(t *testing.T) {
	for _, state := range []int64{1, 64 << 20} {
		memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 36 << 10, State: state})
		testutil.FailErr(t, "create actual fingerprint budget", err)
		release, err := memory.Reserve(t.Context(), 36<<10)
		testutil.FailErr(t, "admit test arena", err)
		var scratch []byte
		if state == 1 {
			scratch = make([]byte, 32<<10)
		}
		_, err = newSourceFingerprintPage(t.Context(), nil, nil, nil, nil, &sourceTreeAllocation{memory: memory}, scratch)
		if state == 1 {
			var limit *toolcontract.ProgramMemoryLimitError
			if !errors.As(err, &limit) || limit.Account != "state" {
				t.Fatalf("unadmitted page error=%v", err)
			}
		} else if !errors.Is(err, ErrSourceRecoveryFailed) {
			t.Fatalf("missing read arena error=%v", err)
		}
		release()
		if used := memory.InUse(); used != (toolcontract.ProgramMemoryUsage{}) {
			t.Fatalf("refused page retained memory: %+v", used)
		}
	}
}
