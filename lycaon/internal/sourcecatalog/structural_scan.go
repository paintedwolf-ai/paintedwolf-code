package sourcecatalog

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
)

const (
	defaultStructuralScanWorkers       = 3
	maxStructuralScanWorkers           = 4
	maxStructuralScanQueuedDirs        = 4096
	maxStructuralScanQueuedBytes       = 4 << 20
	maxStructuralScanCachedDirectories = 256
)

var (
	errStructuralScanControl                   = errors.New("structural scan control failed")
	errStructuralScanDirectoryCacheUnsupported = errors.New("structural scan directory cache is unsupported")
)

type structuralScanOptions struct {
	store          *indexStore
	broker         *backgroundwork.Broker
	spoolDir       string
	epoch          repochange.Epoch
	mark           func(string) uint64
	request        func(string) backgroundwork.Request
	descend        func(string) (bool, error)
	directoryCache *structuralScanDirectoryCache
	workers        int
	// laneBoundary runs once every directory an expansion opens is listed,
	// before the first collapsed one is read. It never runs when nothing is
	// collapsed.
	laneBoundary func(context.Context) error
}

type structuralScanResult struct {
	dir      string
	listing  directoryDiscovery
	children []string
	failure  DirectoryObservation
	done     bool
	err      error
}

type structuralScanQueue struct {
	memory []string
	bytes  int
	spool  *structuralScanSpool
	dir    string
}

type structuralScanSpool struct {
	file  *os.File
	top   int64
	count int
	bytes int64
}

func scanStructure(ctx context.Context, root *os.Root, start string, options structuralScanOptions, emit func(directoryDiscovery) error) error {
	if root == nil {
		return os.ErrInvalid
	}
	start = normalizeDir(start)
	if _, err := observationPath(root, start); err != nil {
		return err
	}
	workers := options.workers
	if workers <= 0 {
		workers = defaultStructuralScanWorkers
	}
	if workers > maxStructuralScanWorkers {
		workers = maxStructuralScanWorkers
	}
	cache := newStructuralScanDirectoryCache(root, maxStructuralScanCachedDirectories)
	defer cache.close()
	options.directoryCache = cache
	queue := newStructuralScanFrontier(ctx, root.Name(), options)
	defer queue.close()
	if err := queue.push([]string{start}); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	drainCtx := ctx.Done()
	work := make(chan string, workers)
	results := make(chan structuralScanResult, workers)
	var workerGroup sync.WaitGroup
	var once sync.Once
	var scanErr error
	fail := func(err error) {
		if err == nil {
			return
		}
		once.Do(func() {
			scanErr = err
			drainCtx = nil
			cancel()
		})
	}
	for range workers {
		workerGroup.Add(1)
		go func() {
			defer workerGroup.Done()
			for dir := range work {
				failure, err := scanStructureDirectory(ctx, root, dir, options, results)
				results <- structuralScanResult{dir: dir, failure: failure, done: true, err: err}
			}
		}()
	}
	active := 0
	for queue.len() > 0 || active > 0 {
		for scanErr == nil && active < workers && queue.ready(active) {
			if options.laneBoundary != nil && queue.atLaneBoundary(active) {
				if err := options.laneBoundary(ctx); err != nil {
					fail(err)
					break
				}
			}
			next, err := queue.pop()
			if err != nil {
				fail(err)
				break
			}
			select {
			case work <- next:
				active++
			case <-drainCtx:
				fail(ctx.Err())
			}
		}
		if active == 0 {
			break
		}
		select {
		case result := <-results:
			if result.err != nil {
				if result.dir == start || ctx.Err() != nil || errors.Is(result.err, errStructuralScanControl) {
					fail(result.err)
				} else if scanErr == nil {
					if err := emit(structuralScanFailure(result.failure, result.err)); err != nil {
						fail(err)
					}
				}
			}
			if result.listing.observation.Path != "" && scanErr == nil {
				if err := emit(result.listing); err != nil {
					fail(err)
				}
			}
			if scanErr == nil {
				if err := queue.push(result.children); err != nil {
					fail(err)
				}
			} else {
				queue.clearMemory()
			}
			if result.done {
				active--
			}
		case <-drainCtx:
			fail(ctx.Err())
		}
	}
	close(work)
	workerGroup.Wait()
	if scanErr != nil {
		return scanErr
	}
	return ctx.Err()
}

func scanStructureDirectory(ctx context.Context, root *os.Root, dir string, options structuralScanOptions, results chan<- structuralScanResult) (DirectoryObservation, error) {
	observation := structuralScanObservation(dir, options)
	if ctx.Err() != nil {
		return observation, ctx.Err()
	}
	directory, err := openStructuralScanDirectory(ctx, root, dir, options)
	if err != nil {
		return observation, err
	}
	defer directory.close()
	observation.Stamp = directory.stamp
	reader := directoryBatch{file: directory.entries}
	cachedDirectory := false
	for {
		releaseQuantum, err := acquireStructuralScanDirectory(ctx, dir, options)
		if err != nil {
			return observation, err
		}
		batch, complete, readErr := reader.read(indexBatchSize)
		if readErr != nil {
			releaseQuantum()
			return observation, readErr
		}
		nodes := readObservationNodes(root, directory, dir, batch)
		releaseQuantum()
		observation.Entries += len(batch)
		observation.Complete = complete
		children := make([]string, 0, len(nodes))
		for _, node := range nodes {
			if node.isDir && !node.isSymlink {
				descend, err := structuralScanDescend(options, node.path)
				if err != nil {
					return observation, errors.Join(errStructuralScanControl, err)
				}
				if descend {
					children = append(children, node.path)
				}
			}
		}
		if !cachedDirectory && len(children) > 0 && options.directoryCache != nil {
			options.directoryCache.remember(dir, directory.handle)
			cachedDirectory = true
		}
		if len(nodes) > 0 || complete {
			select {
			case results <- structuralScanResult{dir: dir, listing: directoryDiscovery{nodes: nodes, observation: observation}, children: children}:
			case <-ctx.Done():
				return observation, ctx.Err()
			}
		}
		if complete {
			return observation, nil
		}
	}
}

func openStructuralScanDirectory(ctx context.Context, root *os.Root, dir string, options structuralScanOptions) (*observationDirectory, error) {
	if err := validateStructuralScanDirectory(root, dir); err != nil {
		return nil, err
	}
	release, err := acquireStructuralScanDirectory(ctx, dir, options)
	if err != nil {
		return nil, err
	}
	defer release()
	directory, err := openStructuralScanDirectoryUnadmitted(root, dir, options.directoryCache)
	if err != nil {
		return nil, err
	}
	return directory, nil
}

func openStructuralScanDirectoryUnadmitted(root *os.Root, dir string, cache *structuralScanDirectoryCache) (*observationDirectory, error) {
	name := filepath.FromSlash(normalizeDir(dir))
	file, err := openStructuralScanCachedDirectory(dir, cache)
	if err != nil {
		return nil, normalizeStructuralScanDirectoryOpenError(err)
	}
	// Cached child handles are ordinary files and already avoid Root's eager stat.
	entries := &directoryKindReader{file: file}
	if file == nil {
		file, err = openStructuralScanDirectoryFile(root, name)
		if err != nil {
			return nil, normalizeStructuralScanDirectoryOpenError(err)
		}
		entries, err = directoryKinds(file)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	info, err := file.Stat()
	if err != nil {
		if entries.file != file {
			_ = entries.file.Close()
		}
		_ = file.Close()
		return nil, err
	}
	resolved := normalizeDir(dir)
	return &observationDirectory{
		handle:        file,
		entries:       entries,
		resolved:      resolved,
		privateFilter: repochange.NewPrivateDirectoryFilter(filepath.Join(root.Name(), filepath.FromSlash(resolved))),
		stamp:         directoryStampOf(info),
	}, nil
}

func validateStructuralScanDirectory(root *os.Root, dir string) error {
	if filepath.IsAbs(dir) || sandbox.HasParentTraversal(dir) {
		return os.ErrPermission
	}
	if repochange.IsPrivatePath(filepath.Join(root.Name(), filepath.FromSlash(dir))) {
		return os.ErrPermission
	}
	return nil
}

func acquireStructuralScanDirectory(ctx context.Context, dir string, options structuralScanOptions) (func(), error) {
	broker := options.broker
	if broker == nil && options.store != nil {
		broker = options.store.catalog.broker
	}
	if broker == nil {
		broker = backgroundwork.Process()
	}
	request := structuralScanRequest(dir, options)
	return broker.Acquire(ctx, request)
}

func structuralScanRequest(dir string, options structuralScanOptions) backgroundwork.Request {
	if options.request != nil {
		request := options.request(dir)
		request.Resources = []backgroundwork.Resource{backgroundwork.ResourceDirectory}
		return request
	}
	if options.store != nil {
		request := options.store.observationRequest(dir)
		request.Resources = []backgroundwork.Resource{backgroundwork.ResourceDirectory}
		return request
	}
	return backgroundwork.Request{Priority: backgroundwork.PriorityProactive, Resources: []backgroundwork.Resource{backgroundwork.ResourceDirectory}}
}

func structuralScanFailure(observation DirectoryObservation, err error) directoryDiscovery {
	observation.Failure = err.Error()
	if len(observation.Failure) > 4096 {
		observation.Failure = observation.Failure[:4096]
	}
	return directoryDiscovery{observation: observation}
}

func structuralScanObservation(dir string, options structuralScanOptions) DirectoryObservation {
	return DirectoryObservation{Path: dir, Invalidation: structuralScanMark(options, dir), Epoch: structuralScanEpoch(options)}
}

func structuralScanDescend(options structuralScanOptions, dir string) (bool, error) {
	if options.descend == nil {
		return true, nil
	}
	return options.descend(dir)
}

func structuralScanMark(options structuralScanOptions, dir string) uint64 {
	if options.mark != nil {
		return options.mark(dir)
	}
	if options.store != nil {
		return options.store.observationMark(dir)
	}
	return 0
}

func structuralScanEpoch(options structuralScanOptions) repochange.Epoch {
	if options.epoch.BootID != "" || options.epoch.Value != 0 {
		return options.epoch
	}
	if options.store != nil {
		return repochange.CurrentEpoch(options.store.root.Path)
	}
	return repochange.Epoch{}
}

func structuralScanSpoolDir(options structuralScanOptions) string {
	if options.spoolDir != "" {
		return options.spoolDir
	}
	if options.store != nil {
		if dir, err := options.store.catalog.treeDirPath(); err == nil {
			return dir
		}
	}
	return os.TempDir()
}

func structuralScanQueueEntrySize(rel string) int { return len(rel) + 8 }

func (q *structuralScanQueue) len() int { return len(q.memory) + q.spoolCount() }

func (q *structuralScanQueue) spoolCount() int {
	if q.spool == nil {
		return 0
	}
	return q.spool.count
}

func (q *structuralScanQueue) push(paths []string) error {
	for _, rel := range paths {
		if len(rel) > 1<<20 {
			return os.ErrInvalid
		}
		size := structuralScanQueueEntrySize(rel)
		if len(q.memory) < maxStructuralScanQueuedDirs && q.bytes+size <= maxStructuralScanQueuedBytes {
			q.memory = append(q.memory, rel)
			q.bytes += size
			continue
		}
		if err := q.writeSpool(rel); err != nil {
			return err
		}
	}
	return nil
}

func (q *structuralScanQueue) pop() (string, error) {
	if n := len(q.memory); n > 0 {
		rel := q.memory[n-1]
		q.memory = q.memory[:n-1]
		q.bytes -= structuralScanQueueEntrySize(rel)
		return rel, nil
	}
	if q.spool == nil || q.spool.count == 0 {
		return "", io.EOF
	}
	return q.readSpool()
}

func (q *structuralScanQueue) clearMemory() {
	q.memory = nil
	q.bytes = 0
}

func (q *structuralScanQueue) close() {
	if q.spool == nil {
		return
	}
	_ = q.spool.file.Close()
	q.spool = nil
}

func (q *structuralScanQueue) writeSpool(rel string) error {
	if q.spool == nil {
		if err := os.MkdirAll(q.dir, 0o700); err != nil { // #nosec G703 -- dir is a host-owned catalog or temporary directory.
			return err
		}
		file, err := createStructuralTempFile(q.dir, "structural-scan-*.tmp")
		if err != nil {
			return err
		}
		q.spool = &structuralScanSpool{file: file, top: -1}
	}
	recordBytes := int64(12 + len(rel))
	offset, err := q.spool.file.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if offset != q.spool.bytes {
		return os.ErrInvalid
	}
	if q.spool.top < -1 {
		return os.ErrInvalid
	}
	previous := uint64(math.MaxUint64)
	if q.spool.top >= 0 {
		previous = uint64(q.spool.top) // #nosec G115 -- q.spool.top is checked non-negative.
	}
	if len(rel) > math.MaxUint32 {
		return os.ErrInvalid
	}
	var header [12]byte
	binary.BigEndian.PutUint64(header[:8], previous)
	binary.BigEndian.PutUint32(header[8:], uint32(len(rel))) // #nosec G115 -- len(rel) is checked above.
	if _, err := q.spool.file.Write(header[:]); err != nil {
		return err
	}
	if _, err := q.spool.file.WriteString(rel); err != nil {
		return err
	}
	q.spool.top = offset
	q.spool.count++
	q.spool.bytes += recordBytes
	return nil
}

func (q *structuralScanQueue) readSpool() (string, error) {
	if q.spool.top < 0 {
		return "", io.EOF
	}
	if _, err := q.spool.file.Seek(q.spool.top, io.SeekStart); err != nil {
		return "", err
	}
	var header [12]byte
	if _, err := io.ReadFull(q.spool.file, header[:]); err != nil {
		return "", err
	}
	encodedPrevious := binary.BigEndian.Uint64(header[:8])
	previous := int64(-1)
	if encodedPrevious != math.MaxUint64 {
		if encodedPrevious > math.MaxInt64 {
			return "", os.ErrInvalid
		}
		previous = int64(encodedPrevious) // #nosec G115 -- encodedPrevious is checked against math.MaxInt64.
	}
	length := binary.BigEndian.Uint32(header[8:])
	if length > 1<<20 {
		return "", os.ErrInvalid
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(q.spool.file, buf); err != nil {
		return "", err
	}
	recordBytes := int64(len(header) + int(length))
	if q.spool.top+recordBytes != q.spool.bytes {
		return "", os.ErrInvalid
	}
	if err := q.spool.file.Truncate(q.spool.top); err != nil {
		return "", err
	}
	if _, err := q.spool.file.Seek(q.spool.top, io.SeekStart); err != nil {
		return "", err
	}
	q.spool.top = previous
	q.spool.count--
	q.spool.bytes -= recordBytes
	if q.spool.count == 0 {
		err := q.resetSpool()
		return string(buf), err
	}
	return string(buf), nil
}

func (q *structuralScanQueue) resetSpool() error {
	if q.spool == nil {
		return nil
	}
	if err := q.spool.file.Truncate(0); err != nil && !errors.Is(err, os.ErrClosed) {
		return err
	}
	if _, err := q.spool.file.Seek(0, io.SeekStart); err != nil && !errors.Is(err, os.ErrClosed) {
		return err
	}
	q.spool.top = -1
	q.spool.bytes = 0
	return nil
}
