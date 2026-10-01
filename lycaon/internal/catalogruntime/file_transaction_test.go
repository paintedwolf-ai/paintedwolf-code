package catalogruntime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestUpdateFileSerializesReadModifyWrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "catalog.yaml")
	testutil.FailErr(t, "seed catalog", os.WriteFile(target, []byte("base\n"), 0o600))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, suffix := range []string{"one\n", "two\n"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := UpdateFile(context.Background(), FileUpdate{
				LockRoot: dir,
				Target:   target,
				Mode:     0o600,
				DirMode:  0o700,
				Apply: func() error {
					before, err := os.ReadFile(target)
					if err != nil {
						return err
					}
					_, err = fseffect.Replace(fseffect.ReplaceRequest{
						Location: fseffect.PathLocation(target),
						Source:   bytes.NewReader(append(before, suffix...)),
						Mode:     0o600,
						DirMode:  0o700,
					})
					return err
				},
			})
			if err != nil {
				t.Errorf("UpdateFile: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	body, err := os.ReadFile(target)
	testutil.FailErr(t, "read catalog", err)
	if !bytes.Contains(body, []byte("one\n")) || !bytes.Contains(body, []byte("two\n")) {
		t.Fatalf("catalog lost an update: %q", body)
	}
}

func TestProcessLockCancellationReleasesTableEntry(t *testing.T) {
	target := filepath.Join(t.TempDir(), "catalog.yaml")
	release, err := acquireCatalogProcessLock(t.Context(), target)
	testutil.FailErr(t, "acquire first lock", err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = acquireCatalogProcessLock(ctx, target)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("second acquire error = %v, want context cancellation", err)
	}
	release()
	catalogFileLocks.Lock()
	_, retained := catalogFileLocks.entries[target]
	catalogFileLocks.Unlock()
	if retained {
		t.Fatal("unused process lock entry was retained")
	}
}

func TestUpdateFileRollsBackDurableAndRuntimeGeneration(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "catalog.yaml")
	testutil.FailErr(t, "seed catalog", os.WriteFile(target, []byte("before\n"), 0o600))
	published := "before\n"
	wantErr := errors.New("publish failed")
	publishCalls := 0
	err := UpdateFile(context.Background(), FileUpdate{
		LockRoot: dir,
		Target:   target,
		Mode:     0o600,
		DirMode:  0o700,
		Apply: func() error {
			_, err := fseffect.Replace(fseffect.ReplaceRequest{
				Location: fseffect.PathLocation(target),
				Source:   bytes.NewReader([]byte("after\n")),
				Mode:     0o600,
				DirMode:  0o700,
			})
			return err
		},
		Publish: func() error {
			publishCalls++
			body, err := os.ReadFile(target)
			if err != nil {
				return err
			}
			published = string(body)
			if publishCalls == 1 {
				return wantErr
			}
			return nil
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want publication failure", err)
	}
	body, readErr := os.ReadFile(target)
	testutil.FailErr(t, "read rolled back catalog", readErr)
	if string(body) != "before\n" || published != "before\n" || publishCalls != 2 {
		t.Fatalf("rollback body=%q published=%q calls=%d", body, published, publishCalls)
	}
}

func TestUpdateFileValidationFailureRepublishesPriorGeneration(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "catalog.yaml")
	testutil.FailErr(t, "seed catalog", os.WriteFile(target, []byte("before\n"), 0o600))
	published := "before\n"
	wantErr := errors.New("published generation rejected")
	publishCalls := 0
	err := UpdateFile(t.Context(), FileUpdate{
		LockRoot: dir,
		Target:   target,
		Mode:     0o600,
		DirMode:  0o700,
		Apply: func() error {
			_, err := fseffect.Replace(fseffect.ReplaceRequest{
				Location: fseffect.PathLocation(target),
				Source:   bytes.NewReader([]byte("after\n")),
				Mode:     0o600,
				DirMode:  0o700,
			})
			return err
		},
		Publish: func() error {
			publishCalls++
			body, err := os.ReadFile(target)
			if err != nil {
				return err
			}
			published = string(body)
			return nil
		},
		Validate: func() error { return wantErr },
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want validation failure", err)
	}
	body, readErr := os.ReadFile(target)
	testutil.FailErr(t, "read rolled back catalog", readErr)
	if string(body) != "before\n" || published != "before\n" || publishCalls != 2 {
		t.Fatalf("rollback body=%q published=%q calls=%d", body, published, publishCalls)
	}
}
