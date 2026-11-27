package pythonruntime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/pythonnative"
)

type nativeActiveExec struct {
	cancel context.CancelFunc
}

// NativeEngineRuntime executes isolated Portable Python invocations using
// PY07's ahead-of-time compiled native CPython/WASI shared library loaded via
// the declared pythonnative platform adapter after complete fail-closed
// manifest, SHA-256, ABI, and image verification.
type NativeEngineRuntime struct {
	mu                  sync.Mutex
	closed              bool
	closeDone           chan struct{}
	activeWG            sync.WaitGroup
	activeExecs         map[uint64]*nativeActiveExec
	nextExecID          uint64
	lib                 *pythonnative.Library
	identity            NativeEngineIdentity
	manifest            *ImageManifest
	imageDir            string
	scratchRoot         string
	workspaceMount      string
	useTrustedBootstrap bool
	audit               *ImportAuditReport
	clock               Clock
	entropy             Entropy
	parked              *ParkedGuestTracker
	memLimitPgs         uint32
}

// NewNativeEngineRuntime verifies the staged native engine artifact, opens it
// via the pythonnative adapter, performs the fail-closed pw_py_engine_identity()
// + image pairing handshake, and initializes the engine. It fails explicitly
// when native capability is unavailable and NEVER silently falls back to wazero.
func NewNativeEngineRuntime(_ context.Context, cfg RuntimeConfig) (Runtime, error) {
	goos, goarch := pythonnative.HostPlatform()
	if !pythonnative.NativeAOTSupported() {
		return nil, &NativeEngineVerificationError{
			Stage:   "capability",
			Message: fmt.Sprintf("native portable python AOT engine is unsupported in this build (%s/%s)", goos, goarch),
			Err: &pythonnative.UnsupportedCapabilityError{
				GOOS:   goos,
				GOARCH: goarch,
				Reason: "native AOT loader requires CGO_ENABLED=1 on darwin/arm64 or linux/amd64",
			},
		}
	}

	bundled := cfg.EngineManifest
	if bundled == nil {
		var err error
		if strings.TrimSpace(cfg.EngineManifestPath) != "" {
			bundled, err = LoadBundledEngineManifestFile(cfg.EngineManifestPath)
		} else {
			bundled, err = LoadBundledEngineManifest()
		}
		if err != nil {
			return nil, &NativeEngineVerificationError{
				Stage:   "manifest",
				Message: "failed to load bundled portable python native engine manifest",
				Err:     err,
			}
		}
	}
	if err := ValidateBundledEngineManifest(bundled); err != nil {
		return nil, &NativeEngineVerificationError{
			Stage:   "manifest",
			Message: "bundled portable python native engine manifest is invalid or unpinned",
			Err:     err,
		}
	}

	engineDir := strings.TrimSpace(cfg.EngineDir)
	if engineDir == "" {
		var err error
		engineDir, err = DiscoverEngineDir("")
		if err != nil {
			return nil, err
		}
	}

	art, err := bundled.ArtifactForPlatform(goos, goarch)
	if err != nil {
		return nil, &NativeEngineVerificationError{
			Stage:   "platform",
			Message: fmt.Sprintf("no pinned native python engine artifact for %s/%s", goos, goarch),
			Err:     err,
		}
	}
	libPath, err := VerifyEngineDir(bundled, engineDir, goos, goarch, art)
	if err != nil {
		return nil, &NativeEngineVerificationError{
			Stage:   "artifact",
			Message: "staged native python engine failed SHA-256/manifest verification before dlopen",
			Err:     err,
		}
	}

	lib, err := pythonnative.Open(libPath)
	if err != nil {
		return nil, &NativeEngineVerificationError{
			Stage:   "dlopen",
			Message: fmt.Sprintf("failed to load verified native engine %s", libPath),
			Err:     err,
		}
	}

	rawID := lib.Identity()
	if rawID == nil {
		lib.Close()
		return nil, &NativeEngineVerificationError{
			Stage:   "abi",
			Message: "pw_py_engine_identity() returned NULL",
		}
	}
	reported := nativeIdentityToGo(rawID)

	verifiedID, err := VerifyNativeEngineHandshake(HandshakeVerificationInput{
		BundledManifest:  bundled,
		EngineDir:        engineDir,
		ImageDir:         cfg.ImageDir,
		ImageManifest:    cfg.Manifest,
		ReportedIdentity: reported,
		GOOS:             goos,
		GOARCH:           goarch,
	})
	if err != nil {
		lib.Close()
		return nil, err
	}

	audit, err := AuditNativeEngineImports(verifiedID, engineDir, cfg.ImageDir, cfg.Manifest, cfg.WasmBytes, cfg.ImportPolicy)
	if err != nil {
		lib.Close()
		return nil, err
	}
	if cfg.EnforceImportAudit && !audit.Passed {
		lib.Close()
		return nil, fmt.Errorf("native engine wasm import audit failed: %d denied, %d undeclared", len(audit.Denied), len(audit.Undeclared))
	}

	if st := lib.Init(); st != pythonnative.StatusOK {
		lib.Close()
		return nil, &NativeEngineVerificationError{
			Stage:   "init",
			Message: fmt.Sprintf("pw_py_engine_init() failed with status %d", int(st)),
		}
	}

	clk := cfg.Clock
	if clk == nil {
		clk = NewSystemClock()
	}
	ent := cfg.Entropy
	if ent == nil {
		ent = CryptoEntropy{}
	}
	wsMount := cfg.WorkspaceMount
	if strings.TrimSpace(wsMount) == "" {
		wsMount = DefaultGuestWorkspaceMount
	}
	if strings.TrimSpace(cfg.ScratchRoot) != "" {
		if err := os.MkdirAll(cfg.ScratchRoot, 0o700); err != nil {
			lib.Close()
			return nil, fmt.Errorf("create scratch root: %w", err)
		}
		_, _ = CleanOrphanedScratchDirs(cfg.ScratchRoot)
	}

	return &NativeEngineRuntime{
		activeExecs:         make(map[uint64]*nativeActiveExec),
		lib:                 lib,
		identity:            *verifiedID,
		manifest:            cfg.Manifest,
		imageDir:            cfg.ImageDir,
		scratchRoot:         cfg.ScratchRoot,
		workspaceMount:      wsMount,
		useTrustedBootstrap: cfg.UseTrustedBootstrap,
		audit:               audit,
		clock:               clk,
		entropy:             ent,
		parked:              NewParkedGuestTracker(cfg.ParkedBudget),
		memLimitPgs:         cfg.DefaultMemoryLimitPages,
	}, nil
}

func nativeIdentityToGo(id *pythonnative.Identity) *NativeEngineIdentity {
	if id == nil {
		return nil
	}
	return &NativeEngineIdentity{
		ABIVersion:            id.ABIVersion,
		ImportCount:           id.ImportCount,
		EngineTag:             id.EngineTag,
		SourceCommit:          id.SourceCommit,
		Platform:              id.Platform,
		CPythonVersion:        id.CPythonVersion,
		CPythonWasmSHA256:     id.CPythonWasmSHA256,
		MeteredWasmSHA256:     id.MeteredWasmSHA256,
		WABTVersion:           id.WABTVersion,
		FuelAlgorithmVersion:  id.FuelAlgorithmVersion,
		ImportInventorySHA256: id.ImportInventorySHA256,
	}
}

func (r *NativeEngineRuntime) Manifest() *ImageManifest          { return r.manifest }
func (r *NativeEngineRuntime) ImportAudit() *ImportAuditReport   { return r.audit }
func (r *NativeEngineRuntime) ParkedGuests() *ParkedGuestTracker { return r.parked }

func (r *NativeEngineRuntime) Info() RuntimeInfo {
	id := r.identity
	return RuntimeInfo{
		Backend:      BackendNativeAOT,
		NativeEngine: &id,
	}
}

func (r *NativeEngineRuntime) Close(_ context.Context) error {
	r.mu.Lock()
	if r.closed {
		done := r.closeDone
		r.mu.Unlock()
		if done != nil {
			<-done
		}
		return nil
	}
	r.closed = true
	done := make(chan struct{})
	r.closeDone = done
	for _, act := range r.activeExecs {
		if act != nil && act.cancel != nil {
			act.cancel()
		}
	}
	lib := r.lib
	r.mu.Unlock()

	// Drain every admitted execution (including its cancellation watcher
	// goroutine and pw_py_instance_free) before unloading the native library.
	r.activeWG.Wait()

	r.parked.ReleaseAll()
	if lib != nil {
		lib.Close()
	}

	r.mu.Lock()
	r.lib = nil
	r.mu.Unlock()
	close(done)
	return nil
}

func (r *NativeEngineRuntime) Execute(ctx context.Context, inv Invocation) (*ExecutionResult, error) {
	start := r.clock.Now()

	// Serialize admission against Close so no invocation can touch lib after
	// Close begins draining, and capture lib locally for the duration of this
	// invocation (kept alive until r.activeWG.Done()).
	r.mu.Lock()
	if r.closed || r.lib == nil {
		r.mu.Unlock()
		return nil, ErrRuntimeClosed
	}
	execCtx, cancel := context.WithCancel(ctx)
	if inv.Limits.Timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, inv.Limits.Timeout)
	}
	r.nextExecID++
	execSeq := r.nextExecID
	if r.activeExecs == nil {
		r.activeExecs = make(map[uint64]*nativeActiveExec)
	}
	r.activeExecs[execSeq] = &nativeActiveExec{cancel: cancel}
	lib := r.lib
	r.activeWG.Add(1)
	r.mu.Unlock()

	defer func() {
		cancel()
		r.mu.Lock()
		delete(r.activeExecs, execSeq)
		r.mu.Unlock()
		r.activeWG.Done()
	}()

	latch := NewStopLatch(cancel)
	latch.now = r.clock.Now
	if inv.StopLatch != nil {
		inv.StopLatch.RegisterSealer(func() {
			if stop := inv.StopLatch.Stop(); stop != nil {
				latch.Trigger(*stop)
			}
		})
		latch.RegisterSealer(func() {
			if stop := latch.Stop(); stop != nil {
				inv.StopLatch.Trigger(*stop)
			}
		})
	}
	meter := NewWorkMeter(inv.Limits.MaxGuestWork, inv.Limits.MaxHostWork, inv.Limits.MaxStagedBytes, latch)

	var (
		argv []string
		err  error
	)
	if r.useTrustedBootstrap {
		cwd := normalizeGuestCWD(r.workspaceMount, inv.CWD)
		argv, err = BuildBootstrapArgv(BootstrapConfig{
			Mode:           inv.Mode,
			Code:           inv.Code,
			Script:         inv.Script,
			Module:         inv.Module,
			CLI:            inv.CLI,
			CWD:            cwd,
			Args:           inv.Args,
			PackagesMount:  DefaultGuestPackagesMount,
			ScratchMount:   DefaultGuestScratchMount,
			HTTPBridgePath: inv.HTTPBridgePath,
		})
	} else {
		argv, err = BuildArgv(inv)
	}
	if err != nil {
		return nil, err
	}

	invScratch, err := os.MkdirTemp(r.scratchRoot, "pw-py-scratch-*")
	if err != nil {
		return nil, fmt.Errorf("create per-invocation scratch dir: %w", err)
	}
	defer os.RemoveAll(invScratch)

	var stderrCapture bytes.Buffer
	stderrTarget := io.Writer(&stderrCapture)
	if inv.Stderr != nil {
		stderrTarget = io.MultiWriter(inv.Stderr, &stderrCapture)
	}
	sealedStdout := NewSealedWriter(inv.Stdout, latch, inv.Limits.MaxOutputBytes)
	sealedStderr := NewSealedWriter(stderrTarget, latch, inv.Limits.MaxOutputBytes)

	preopens, wsFS := r.buildPreopens(execCtx, inv, latch, meter, invScratch)
	wasiHost := newNativeWASIHost(
		execCtx,
		argv,
		inv.Env,
		inv.Stdin,
		sealedStdout,
		sealedStderr,
		r.clock,
		r.entropy,
		latch,
		meter,
		preopens,
	)
	defer wasiHost.closeAll()

	if inv.ExecutionID != "" {
		defer r.parked.Release(inv.ExecutionID)
	}

	maxPages := inv.Limits.MaxMemoryPages
	if maxPages == 0 {
		maxPages = r.memLimitPgs
	}

	// Lock the calling goroutine to one OS thread for single-thread affinity
	// across pw_py_instance_new -> pw_py_instance_run -> pw_py_instance_free.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if execCtx.Err() != nil {
		reason := "execution canceled"
		if execCtx.Err() == context.DeadlineExceeded {
			reason = "execution timed out"
		}
		latch.Trigger(TerminalStop{Code: StopCancelled, Reason: reason})
		res := &ExecutionResult{
			ExecutionID:     inv.ExecutionID,
			Backend:         BackendNativeAOT,
			EngineTag:       r.identity.EngineTag,
			ExitCode:        1,
			CleanCompletion: false,
			Stop:            latch.Stop(),
			Usage: ResourceUsage{
				Duration: r.clock.Now().Sub(start),
			},
		}
		if inv.Workspace != nil {
			report, settleErr := inv.Workspace.Settle(context.Background(), false)
			res.Settlement = report
			if settleErr != nil {
				res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("workspace settlement failed: %v", settleErr))
			}
		}
		return res, nil
	}

	inst, st := lib.NewInstance(pythonnative.InstanceConfig{
		MaxMemoryPages: maxPages,
		FuelBudget:     inv.Limits.MaxGuestWork,
	}, wasiHost.dispatch)
	if st != pythonnative.StatusOK {
		return nil, fmt.Errorf("pw_py_instance_new failed with status %d", int(st))
	}
	defer inst.Free()

	// Asynchronous cancellation watcher: interrupts pure CPU loops via
	// pw_py_instance_cancel(inst) when execCtx is canceled or times out.
	// Deferred stop runs BEFORE inst.Free() (and BEFORE activeWG.Done())
	// so cancel is never called on a freed instance or unloaded library.
	doneCh := make(chan struct{})
	watcherDone := make(chan struct{})
	var stopWatcherOnce sync.Once
	stopWatcher := func() {
		stopWatcherOnce.Do(func() {
			close(doneCh)
			<-watcherDone
		})
	}
	go func() {
		defer close(watcherDone)
		select {
		case <-execCtx.Done():
			inst.Cancel()
			ticker := time.NewTicker(1 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-doneCh:
					return
				case <-ticker.C:
					inst.Cancel()
				}
			}
		case <-doneCh:
		}
	}()
	defer stopWatcher()

	runRes := inst.Run()
	stopWatcher()

	// Capture open write descriptor count BEFORE closing host FDs.
	var unclosedWriteDescs int64
	if wsFS != nil {
		unclosedWriteDescs = wsFS.OpenWriteDescriptors()
	}

	// Deterministic exactly-once guest teardown after run returns.
	inst.Free()
	wasiHost.closeAll()

	meter.SetGuestWork(runRes.GuestWorkUsed)
	res := &ExecutionResult{
		ExecutionID: inv.ExecutionID,
		Backend:     BackendNativeAOT,
		EngineTag:   r.identity.EngineTag,
		GuestStderr: stderrCapture.String(),
		Usage: ResourceUsage{
			GuestWork:       meter.GuestWork(),
			HostWork:        meter.HostWork(),
			PeakMemoryPages: runRes.PeakMemoryPages,
			StdoutBytes:     sealedStdout.WrittenBytes(),
			StderrBytes:     sealedStderr.WrittenBytes(),
			StagedBytes:     meter.StagedBytes(),
			Duration:        r.clock.Now().Sub(start),
		},
	}

	r.classifyNativeOutcome(execCtx, latch, runRes, inv.Limits.MaxGuestWork, res)

	if r.useTrustedBootstrap && wasiHost.procExit {
		res.CleanCompletion = false
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("abrupt guest exit via proc_exit(%d) bypassed clean finalization", wasiHost.exitCode))
	}
	if sealedStdout.HadError() || sealedStderr.HadError() {
		res.CleanCompletion = false
		res.Diagnostics = append(res.Diagnostics, "guest output stream reported write or seal error")
	}
	if unclosedWriteDescs > 0 {
		res.CleanCompletion = false
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("guest exited with %d unclosed workspace write descriptor(s)", unclosedWriteDescs))
	}

	if inv.Workspace != nil {
		report, settleErr := inv.Workspace.Settle(context.Background(), res.CleanCompletion)
		res.Settlement = report
		if settleErr != nil {
			res.CleanCompletion = false
			res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("workspace settlement failed: %v", settleErr))
		}
		if report != nil {
			if len(report.Conflicts) > 0 {
				res.CleanCompletion = false
				latch.Trigger(TerminalStop{
					Code:   StopRevisionConflict,
					Reason: fmt.Sprintf("workspace settlement detected %d revision conflict(s)", len(report.Conflicts)),
				})
				res.Stop = latch.Stop()
				res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("workspace settlement revision conflicts: %d", len(report.Conflicts)))
			}
			if report.PartialFailure != nil {
				res.CleanCompletion = false
				res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("workspace settlement partial failure: %v", report.PartialFailure))
			}
		}
	}

	return res, nil
}

func (r *NativeEngineRuntime) classifyNativeOutcome(
	execCtx context.Context,
	latch *StopLatch,
	runRes pythonnative.RunResult,
	fuelBudget uint64,
	res *ExecutionResult,
) {
	if stop := latch.Stop(); stop != nil {
		res.Stop = stop
		res.ExitCode = 1
		res.CleanCompletion = false
		return
	}
	switch runRes.Status {
	case pythonnative.StatusOK:
		res.ExitCode = 0
		res.CleanCompletion = true
	case pythonnative.StatusProcExit:
		res.ExitCode = int(runRes.ExitCode)
		res.CleanCompletion = runRes.ExitCode == 0
	case pythonnative.StatusFuelExhausted:
		latch.Trigger(TerminalStop{
			Code:   StopWorkLimitExceeded,
			Reason: fmt.Sprintf("guest work fuel exhausted (limit %d)", fuelBudget),
		})
		res.Stop = latch.Stop()
		res.ExitCode = 1
		res.CleanCompletion = false
	case pythonnative.StatusCanceled:
		reason := "execution canceled"
		if execCtx.Err() == context.DeadlineExceeded {
			reason = "execution timed out"
		}
		latch.Trigger(TerminalStop{Code: StopCancelled, Reason: reason})
		res.Stop = latch.Stop()
		res.ExitCode = 1
		res.CleanCompletion = false
	case pythonnative.StatusMemoryLimit:
		latch.Trigger(TerminalStop{
			Code:   StopMemoryLimitExceeded,
			Reason: "guest linear memory limit exceeded",
		})
		res.Stop = latch.Stop()
		res.ExitCode = 1
		res.CleanCompletion = false
	case pythonnative.StatusHostStop:
		latch.Trigger(TerminalStop{
			Code:   StopEffectNotAllowed,
			Reason: "host stopped guest execution",
		})
		res.Stop = latch.Stop()
		res.ExitCode = 1
		res.CleanCompletion = false
	default:
		res.ExitCode = 1
		res.CleanCompletion = false
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("native engine trap status=%d trap_code=%d", int(runRes.Status), runRes.TrapCode))
	}
}

func (r *NativeEngineRuntime) buildPreopens(
	execCtx context.Context,
	inv Invocation,
	latch *StopLatch,
	meter *WorkMeter,
	invScratch string,
) ([]nativePreopen, *WorkspaceSysFS) {
	var out []nativePreopen
	out = append(out, nativePreopen{guestPath: "/dev", fs: NullDeviceFS{}})
	if r.imageDir != "" && r.manifest != nil {
		if stdlibAbs, err := resolveInsideDir(r.imageDir, r.manifest.Interpreter.StdlibPath); err == nil {
			out = append(out, nativePreopen{guestPath: "/usr/local/lib", fs: newHostDirSysFS(stdlibAbs, true)})
		}
		if r.manifest.PackagesRoot != "" {
			if pkgAbs, err := resolveInsideDir(r.imageDir, r.manifest.PackagesRoot); err == nil {
				out = append(out, nativePreopen{guestPath: DefaultGuestPackagesMount, fs: newHostDirSysFS(pkgAbs, true)})
			}
		}
	}
	if invScratch != "" {
		out = append(out, nativePreopen{guestPath: DefaultGuestScratchMount, fs: newHostDirSysFS(invScratch, false)})
	}
	var wsFS *WorkspaceSysFS
	if inv.Workspace != nil {
		wsFS = NewWorkspaceSysFS(WorkspaceSysFSConfig{
			Context:     execCtx,
			ExecutionID: inv.ExecutionID,
			Namespace:   NamespaceWorkspace,
			Adapter:     inv.Workspace,
			Observer:    inv.Observer,
			Latch:       latch,
			Meter:       meter,
		})
		out = append(out, nativePreopen{guestPath: r.workspaceMount, fs: wsFS})
	}
	if len(inv.ExtraSysMounts) > 0 {
		mounts := make([]string, 0, len(inv.ExtraSysMounts))
		for k := range inv.ExtraSysMounts {
			mounts = append(mounts, k)
		}
		sort.Strings(mounts)
		for _, guestMount := range mounts {
			if fsMount := inv.ExtraSysMounts[guestMount]; fsMount != nil {
				out = append(out, nativePreopen{guestPath: guestMount, fs: fsMount})
			}
		}
	}
	return out, wsFS
}
