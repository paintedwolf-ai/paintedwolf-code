package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
)

type virtual2MLower struct {
	total int
}

func (v *virtual2MLower) Lstat(rel string) (layeredfs.LowerEntry, error) {
	if rel == "." || rel == "dir" {
		return layeredfs.LowerEntry{
			Kind:    layeredfs.EntryDirectory,
			Mode:    fs.ModeDir | 0o755,
			ModTime: time.Unix(1700000000, 0),
			Dev:     1,
			Ino:     1,
		}, nil
	}
	var idx int
	if _, err := fmt.Sscanf(rel, "dir/file_%07d.txt", &idx); err == nil && idx >= 0 && idx < v.total {
		s := fmt.Sprintf("lower-line-1-%07d\nlower-line-2\n", idx)
		return layeredfs.LowerEntry{
			Kind:      layeredfs.EntryRegular,
			Mode:      0o644,
			Size:      int64(len(s)),
			ModTime:   time.Unix(1700000000, 0),
			Dev:       1,
			Ino:       uint64(100 + idx),
			CtimeSec:  1700000000,
			CtimeNsec: 0,
		}, nil
	}
	return layeredfs.LowerEntry{}, fs.ErrNotExist
}

func (v *virtual2MLower) ReadDir(_ string) ([]layeredfs.LowerDirEntry, error) {
	return nil, errors.New("unexpected lower directory walk")
}

func (v *virtual2MLower) OpenRead(rel string) (layeredfs.ReadSeekCloser, layeredfs.LowerEntry, error) {
	le, err := v.Lstat(rel)
	if err != nil {
		return nil, layeredfs.LowerEntry{}, err
	}
	var idx int
	_, _ = fmt.Sscanf(rel, "dir/file_%07d.txt", &idx)
	s := []byte(fmt.Sprintf("lower-line-1-%07d\nlower-line-2\n", idx))
	return &bytesReaderCloser{Reader: bytes.NewReader(s)}, le, nil
}

func (v *virtual2MLower) Readlink(_ string) (string, error)             { return "", fs.ErrInvalid }
func (v *virtual2MLower) ListXattr(_ string) (map[string][]byte, error) { return nil, nil }

type bytesReaderCloser struct{ *bytes.Reader }

func (b *bytesReaderCloser) Close() error { return nil }

func TestCheckpointScale2MLowerAndParentLayer(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	backing := filepath.Join(tmp, "backing")
	storeDir := filepath.Join(tmp, "store")
	testutil.FailErr(t, "mkdir backing", os.MkdirAll(backing, 0o700))

	core, err := layeredfs.New(layeredfs.Config{
		ViewID:     "view-2m",
		BackingDir: backing,
		Roots:      []layeredfs.RootSpec{{ID: "primary", Label: "primary", IsPrimary: true}},
	})
	testutil.FailErr(t, "New Core", err)
	defer func() { _ = core.ReleaseView() }()

	testutil.FailErr(t, "RegisterLowerProvider", core.RegisterLowerProvider("primary", &virtual2MLower{total: 2_000_000}))

	target := layeredfs.QualifiedPath{RootID: "primary", Rel: "dir/file_0500000.txt"}
	_, err = core.WriteFile(target, []byte("worker-modified-1-in-2M\n"), 0o644)
	testutil.FailErr(t, "WriteFile 1 in 2M", err)

	store, err := NewStore(storeDir)
	testutil.FailErr(t, "NewStore", err)

	pub, err := NewPublisher(PublisherConfig{
		Core:             core,
		Store:            store,
		WorkerID:         "worker-1",
		TaskID:           "task-1663",
		ProjectID:        "proj-1",
		SourceGeneration: "gen-1",
		SelectedRoots:    []layeredfs.RootSpec{{ID: "primary", Label: "primary", IsPrimary: true}},
	})
	testutil.FailErr(t, "NewPublisher", err)

	core.ResetStepStats()
	res, err := pub.Publish(context.Background(), PublishRequest{
		CheckpointID: "chk-scale-1",
		Trigger:      TriggerForegroundCommand,
		CommandID:    "cmd-1",
	})
	testutil.FailErr(t, "Publish 1 change in 2M", err)

	stats := core.Stats()
	if stats.LowerDirReads != 0 || stats.LowerLookups != 0 {
		t.Fatalf("expected 0 lower dir reads and 0 lower lookups during checkpoint publication, got DirReads=%d Lookups=%d",
			stats.LowerDirReads, stats.LowerLookups)
	}
	if len(res.Manifest.Entries) != 1 || res.Manifest.ResourceAccounting.ChangedFiles != 1 {
		t.Fatalf("expected exactly 1 changed entry, got %+v", res.Manifest)
	}

	// Mount published checkpoint as read-only ParentLayer in a stacked child view (#1666).
	parentLayer, err := store.OpenParentLayer("chk-scale-1")
	testutil.FailErr(t, "OpenParentLayer", err)

	childBacking := filepath.Join(tmp, "child-backing")
	testutil.FailErr(t, "mkdir child-backing", os.MkdirAll(childBacking, 0o700))
	childCore, err := layeredfs.New(layeredfs.Config{
		ViewID:       "view-child",
		BackingDir:   childBacking,
		Roots:        []layeredfs.RootSpec{{ID: "primary", Label: "primary", IsPrimary: true}},
		ParentLayers: []layeredfs.ParentLayer{parentLayer},
	})
	testutil.FailErr(t, "New child Core", err)
	defer func() { _ = childCore.ReleaseView() }()

	gotFromParent, err := childCore.ReadFile(target)
	testutil.FailErr(t, "ReadFile from CheckpointParentLayer", err)
	if string(gotFromParent) != "worker-modified-1-in-2M\n" {
		t.Fatalf("unexpected parent layer content: %q", string(gotFromParent))
	}
}

func TestPublicationFaultInjectionAndLimits(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	backing := filepath.Join(tmp, "backing")
	storeDir := filepath.Join(tmp, "store")
	for _, d := range []string{trunk, backing} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(d, 0o755))
	}
	testutil.FailErr(t, "seed a.txt", os.WriteFile(filepath.Join(trunk, "a.txt"), []byte("seed-a"), 0o644))

	core, err := layeredfs.New(layeredfs.Config{
		ViewID:     "view-fault",
		BackingDir: backing,
		Roots:      []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true}},
	})
	testutil.FailErr(t, "New Core", err)
	defer func() { _ = core.ReleaseView() }()

	store, err := NewStore(storeDir)
	testutil.FailErr(t, "NewStore", err)

	var resumedCount int
	pub, err := NewPublisher(PublisherConfig{
		Core:             core,
		Store:            store,
		WorkerID:         "w-fault",
		TaskID:           "t-fault",
		ProjectID:        "p-fault",
		SourceGeneration: "gen-1",
		Callbacks: BarrierCallbacks{
			ResumeWorkers: func(_ context.Context, _ error) {
				resumedCount++
			},
		},
	})
	testutil.FailErr(t, "NewPublisher", err)

	qp := func(rel string) layeredfs.QualifiedPath {
		return layeredfs.QualifiedPath{RootID: "primary", Rel: rel}
	}

	_, err = core.WriteFile(qp("a.txt"), []byte("v1-published"), 0o644)
	testutil.FailErr(t, "write v1", err)
	_, err = pub.Publish(context.Background(), PublishRequest{
		CheckpointID: "chk-1",
		Trigger:      TriggerExplicitCapture,
	})
	testutil.FailErr(t, "Publish chk-1", err)

	_, err = core.WriteFile(qp("a.txt"), []byte("v2-pending"), 0o644)
	testutil.FailErr(t, "write v2", err)

	stages := []FaultStage{
		FaultAfterBarrier,
		FaultOnFlush,
		FaultAfterBlobWrite,
		FaultBeforeManifestSync,
		FaultOnRetentionCommit,
	}
	for _, stage := range stages {
		_, fErr := pub.Publish(context.Background(), PublishRequest{
			CheckpointID: "chk-2-failed-" + string(stage),
			Trigger:      TriggerForegroundCommand,
			FaultInjector: func(s FaultStage) error {
				if s == stage {
					return fmt.Errorf("injected fault at %s", stage)
				}
				return nil
			},
		})
		if fErr == nil {
			t.Fatalf("expected failure at stage %s", stage)
		}
		if stage == FaultOnFlush && !errors.Is(fErr, ErrFlushFailed) {
			t.Fatalf("expected ErrFlushFailed on flush fault, got %v", fErr)
		}
		if stage == FaultOnRetentionCommit && !errors.Is(fErr, ErrRetentionFailed) {
			t.Fatalf("expected ErrRetentionFailed on retention fault, got %v", fErr)
		}
		latest, lErr := store.LatestCheckpointID()
		testutil.FailErr(t, "LatestCheckpointID", lErr)
		if latest != "chk-1" {
			t.Fatalf("expected latest checkpoint to remain chk-1 after %s fault, got %q", stage, latest)
		}
		if _, mErr := store.LoadManifest("chk-2-failed-" + string(stage)); !errors.Is(mErr, ErrCheckpointNotFound) {
			t.Fatalf("expected partial checkpoint to be absent at stage %s, got %v", stage, mErr)
		}
	}

	// Verify overlay byte limit refusal (simulating budget/disk-full boundary).
	pub.cfg.MaxCheckpointBytes = 4
	if _, err := pub.Publish(context.Background(), PublishRequest{CheckpointID: "chk-over-bytes"}); !errors.Is(err, ErrOverlayByteLimitExceeded) {
		t.Fatalf("expected ErrOverlayByteLimitExceeded, got %v", err)
	}
	pub.cfg.MaxCheckpointBytes = 0

	// Working data is preserved and barrier was released on every failure path; next publish succeeds.
	_, err = pub.Publish(context.Background(), PublishRequest{
		CheckpointID: "chk-2",
		Trigger:      TriggerWorkerCompletion,
	})
	testutil.FailErr(t, "Publish chk-2 after faults", err)
	gotV2, err := store.ReadEntryContent("chk-2", qp("a.txt"))
	testutil.FailErr(t, "ReadEntryContent chk-2", err)
	if string(gotV2) != "v2-pending" {
		t.Fatalf("unexpected chk-2 content: %q", string(gotV2))
	}
}

func TestHeldWritableHandleMappedWriterAndCoalescing(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	trunk := filepath.Join(tmp, "trunk")
	backing := filepath.Join(tmp, "backing")
	storeDir := filepath.Join(tmp, "store")
	for _, d := range []string{trunk, backing} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(d, 0o755))
	}
	testutil.FailErr(t, "seed live.txt", os.WriteFile(filepath.Join(trunk, "live.txt"), []byte("initial"), 0o644))

	core, err := layeredfs.New(layeredfs.Config{
		ViewID:     "view-held",
		BackingDir: backing,
		Roots:      []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true}},
	})
	testutil.FailErr(t, "New Core", err)
	defer func() { _ = core.ReleaseView() }()

	store, err := NewStore(storeDir)
	testutil.FailErr(t, "NewStore", err)

	pub, err := NewPublisher(PublisherConfig{
		Core:             core,
		Store:            store,
		WorkerID:         "w-held",
		TaskID:           "t-held",
		ProjectID:        "p-held",
		SourceGeneration: "gen-1",
	})
	testutil.FailErr(t, "NewPublisher", err)

	qp := layeredfs.QualifiedPath{RootID: "primary", Rel: "live.txt"}
	hid, _, err := core.Open(qp, layeredfs.OpenReadWrite|layeredfs.OpenTruncate, 0o644)
	testutil.FailErr(t, "Open O_RDWR", err)
	defer func() { _ = core.CloseHandle(hid) }()

	mappedBuf := []byte("checkpointed-from-mapped-and-fd")
	pub.RegisterMappedRegion(&MappedRegion{
		Path: qp,
		Flush: func() error {
			_, wErr := core.WriteHandle(hid, mappedBuf, 0)
			return wErr
		},
	})

	// Launch concurrent Publish requests to verify coalescing and trigger attribution.
	var wg sync.WaitGroup
	results := make([]PublishResult, 3)
	triggers := []TriggerKind{TriggerForegroundCommand, TriggerNativeMutationBatch, TriggerExplicitCapture}
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			r, pErr := pub.Publish(context.Background(), PublishRequest{
				Trigger:   triggers[idx],
				CommandID: fmt.Sprintf("cmd-%d", idx),
			})
			testutil.FailErr(t, "concurrent Publish", pErr)
			results[idx] = r
		}(i)
	}
	wg.Wait()

	chkID := results[0].Manifest.CheckpointID
	// Mutate both the held writable handle and the mapped buffer after continuation!
	copy(mappedBuf, []byte("OVERWRITTEN-IN-MMAP-AFTER-CONT!"))
	_, err = core.WriteHandle(hid, []byte("OVERWRITTEN-VIA-HELD-FD-AFTER!!"), 0)
	testutil.FailErr(t, "WriteHandle after continuation", err)

	gotPublished, err := store.ReadEntryContent(chkID, qp)
	testutil.FailErr(t, "ReadEntryContent after post-continuation write", err)
	if string(gotPublished) != "checkpointed-from-mapped-and-fd" {
		t.Fatalf("published checkpoint was corrupted by post-continuation write: %q", string(gotPublished))
	}
}
