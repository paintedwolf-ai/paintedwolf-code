package blobstore_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

const (
	stagingRaceRootEnv   = "LYCAON_BLOBSTORE_STAGING_RACE_ROOT"
	stagingRaceWorkerEnv = "LYCAON_BLOBSTORE_STAGING_RACE_WORKER"
	stagingRaceItersEnv  = "LYCAON_BLOBSTORE_STAGING_RACE_ITERS"
)

func TestConcurrentCrossProcessWritesShareStagingDir(t *testing.T) {
	root := t.TempDir()
	const workers = 4
	const iters = 80

	type workerResult struct {
		id     int
		output []byte
		err    error
	}

	results := make(chan workerResult, workers)
	var wg sync.WaitGroup
	for id := range workers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			cmd := exec.CommandContext(
				t.Context(),
				os.Args[0],
				"-test.run=^TestStagingRaceWorkerHelper$",
			)
			cmd.Env = append(
				os.Environ(),
				stagingRaceRootEnv+"="+root,
				stagingRaceWorkerEnv+"="+strconv.Itoa(workerID),
				stagingRaceItersEnv+"="+strconv.Itoa(iters),
			)
			out, err := cmd.CombinedOutput()
			results <- workerResult{id: workerID, output: out, err: err}
		}(id)
	}
	wg.Wait()
	close(results)

	for res := range results {
		if res.err != nil {
			t.Errorf("worker %d failed: %v\n%s", res.id, res.err, string(res.output))
		}
	}
}

func TestStagingRaceWorkerHelper(t *testing.T) {
	root := os.Getenv(stagingRaceRootEnv)
	if root == "" {
		return
	}
	workerID := os.Getenv(stagingRaceWorkerEnv)
	iters, err := strconv.Atoi(os.Getenv(stagingRaceItersEnv))
	testutil.FailErr(t, "parse staging race iterations", err)

	putStore := blobstore.Store{Root: root, Dir: "prompt-attachments"}
	putAtStore := blobstore.Store{Root: root}
	limit := bytebound.Materialization(1 << 20)

	for i := range iters {
		payload := fmt.Sprintf("worker-%s-iter-%d-payload", workerID, i)
		name := fmt.Sprintf("w%s-%d.txt", workerID, i)

		blob, err := putStore.Put(name, strings.NewReader(payload), limit)
		testutil.FailErr(t, fmt.Sprintf("worker %s put %d", workerID, i), err)
		rc, err := putStore.Open(blob)
		testutil.FailErr(t, fmt.Sprintf("worker %s open %d", workerID, i), err)
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		testutil.FailErr(t, fmt.Sprintf("worker %s read %d", workerID, i), err)
		if string(got) != payload {
			t.Fatalf("worker %s put %d body = %q want %q", workerID, i, string(got), payload)
		}

		rel := fmt.Sprintf("model-content/w%s/%d.blob", workerID, i)
		_, err = putAtStore.PutAt(context.Background(), rel, bytes.NewBufferString(payload), limit)
		testutil.FailErr(t, fmt.Sprintf("worker %s putAt %d", workerID, i), err)
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		testutil.FailErr(t, fmt.Sprintf("worker %s read raw %d", workerID, i), err)
		decoded, err := zstdcodec.Decompress(raw)
		testutil.FailErr(t, fmt.Sprintf("worker %s decompress %d", workerID, i), err)
		if string(decoded) != payload {
			t.Fatalf("worker %s putAt %d body = %q want %q", workerID, i, string(decoded), payload)
		}
	}
}
