// Package portablepython composes the Portable Python vertical production
// path: verified WASI image discovery and compilation (PY02), manifest-backed
// catalog and read-only pip inventory (PY01), per-invocation workspace overlay
// and clean-success settlement (PY03), and admission, host-mediated HTTP, and
// static preview dispatch (PY04).
package portablepython

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythoncatalog"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonevidence"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	experimentalsys "github.com/tetratelabs/wazero/experimental/sys"
)

const (
	// EnvImageDir overrides the Portable Python image directory location.
	EnvImageDir = "PW_PORTABLE_PYTHON_IMAGE_DIR"

	// Production default resource limits applied whenever ToolRequest.Limits
	// leaves a field unset (zero).
	DefaultMaxMemoryPages    uint32 = 4096             // 256 MiB Wasm linear memory
	DefaultMaxGuestWork      uint64 = 50_000_000       // deterministic Wasm loop-fuel cap
	DefaultMaxHostWork       uint64 = 100_000          // host work unit cap
	DefaultMaxOutputBytes    int64  = 1024 * 1024      // 1 MiB WASI stream capture bound
	DefaultMaxStagedBytes    int64  = 16 * 1024 * 1024 // 16 MiB staged overlay bound
	DefaultInlineBudgetBytes int    = 65536            // 64 KiB inline tool output budget

	defaultExecutionTimeout = 120 * time.Second
)

// DefaultProtectedPaths are workspace paths always protected against guest
// reads/writes by default (git hooks/config/refs and credential directories).
var DefaultProtectedPaths = []string{
	".git/hooks",
	".git/config",
	".git/HEAD",
	".git/refs",
	".ssh",
	".gnupg",
	".aws",
	".kube",
}

// DiscoverImageDir resolves the Portable Python image directory from
// PW_PORTABLE_PYTHON_IMAGE_DIR or standard Painted Wolf app data locations.
func DiscoverImageDir() (string, error) {
	if envDir := strings.TrimSpace(os.Getenv(EnvImageDir)); envDir != "" {
		if hasImageManifest(envDir) {
			return filepath.Abs(envDir)
		}
		return "", fmt.Errorf("%s=%q does not contain image-manifest.json", EnvImageDir, envDir)
	}

	candidates := standardImageDirCandidates()
	for _, cand := range candidates {
		if hasImageManifest(cand) {
			return filepath.Abs(cand)
		}
	}
	return "", fmt.Errorf("portable python image not found (checked %s and %d standard locations)", EnvImageDir, len(candidates))
}

func standardImageDirCandidates() []string {
	var out []string
	// 1. Beside the running binary (installed bundle / packaged release layouts).
	if exe, err := os.Executable(); err == nil && exe != "" {
		exeDir := filepath.Dir(exe)
		out = append(out,
			filepath.Join(exeDir, "portable-python", "image"),
			filepath.Join(exeDir, "..", "share", "paintedwolf", "portable-python", "image"),
			filepath.Join(exeDir, "..", "Resources", "portable-python", "image"),
		)
	}
	// 2. Active Painted Wolf configdir root.
	if cfgRoot, err := configdir.Root(); err == nil && cfgRoot != "" {
		out = append(out, filepath.Join(cfgRoot, "portable-python", "image"))
	}
	// 3. Standard user data directories.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		channel := configdir.ChannelDirName()
		out = append(out,
			filepath.Join(home, ".local", "share", "paintedwolf-portable-python", "image"),
			filepath.Join(home, ".local", "share", channel, "portable-python", "image"),
		)
		if runtime.GOOS == "darwin" {
			out = append(out, filepath.Join(home, "Library", "Application Support", channel, "portable-python", "image"))
		}
	}
	return out
}

func hasImageManifest(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "image-manifest.json"))
	return err == nil && !info.IsDir()
}

// ServiceConfig configures a reusable Portable Python Service.
type ServiceConfig struct {
	// ImageDir overrides DiscoverImageDir when non-empty.
	ImageDir string
	// ScratchRoot is the host directory mounted at /tmp inside WASI guests.
	// If empty, a temporary directory is created and removed on Close.
	ScratchRoot string
	// DefaultMemoryLimitPages optionally caps Wasm linear memory pages.
	DefaultMemoryLimitPages uint32
	// Clock supplies wall/monotonic time to WASI guests and admission records.
	Clock pythonruntime.Clock
	// Entropy supplies randomness to WASI guests.
	Entropy pythonruntime.Entropy
	// ParkedBudget caps aggregate parked-guest resources awaiting approval.
	ParkedBudget pythonruntime.ParkedGuestBudget
	// Broker is the default ApprovalBroker used when ToolRequest.Broker is nil.
	Broker *pythonadmission.ApprovalBroker
	// JobRegistry is the default SessionJobRegistry for preview listeners.
	JobRegistry pythonadmission.SessionJobRegistry
	// SecretScreener screens stdout/stderr/HTTP/evidence before returning or storing.
	// Defaults to pythonevidence.TestSecretScreener{} when nil.
	SecretScreener pythonevidence.SecretScreener
	// RetainedStore holds bounded overflow stdout/stderr artifacts.
	// Defaults to an in-memory RetainedArtifactStore backed by SecretScreener.
	RetainedStore *pythonevidence.RetainedArtifactStore
	// ReceiptSink optionally persists durable execution/admission/publication receipts.
	ReceiptSink pythonevidence.ReceiptSink
	// InlineBudgetBytes caps inline stdout/stderr returned to the caller (default 65536).
	InlineBudgetBytes int
}

// Service owns a verified, compiled Portable Python WASI runtime and
// constructs per-invocation document overlays, admission observers, host HTTP
// transports, and preview listeners.
type Service struct {
	imageDir        string
	ownedScratch    string
	runtime         *pythonruntime.WazeroRuntime
	runtimeManifest *pythonruntime.ImageManifest
	catalogManifest pythoncatalog.ImageManifest
	clock           pythonruntime.Clock
	broker          *pythonadmission.ApprovalBroker
	jobRegistry     pythonadmission.SessionJobRegistry
	screener        pythonevidence.SecretScreener
	retainedStore   *pythonevidence.RetainedArtifactStore
	receiptSink     pythonevidence.ReceiptSink
	inlineBudget    int
	execSeq         atomic.Uint64
}

// NewService discovers, loads, and verifies the Portable Python image, compiles
// python.wasm once with EnforceImportAudit=true and UseTrustedBootstrap=true,
// and returns a reusable Service.
func NewService(ctx context.Context, cfg ServiceConfig) (*Service, error) {
	imageDir := strings.TrimSpace(cfg.ImageDir)
	if imageDir == "" {
		var err error
		imageDir, err = DiscoverImageDir()
		if err != nil {
			return nil, err
		}
	}
	absImageDir, err := filepath.Abs(imageDir)
	if err != nil {
		return nil, fmt.Errorf("resolve image dir: %w", err)
	}

	manifestPath := filepath.Join(absImageDir, "image-manifest.json")
	rtManifest, err := pythonruntime.LoadImageManifest(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("load runtime image manifest: %w", err)
	}
	if err := pythonruntime.VerifyImage(absImageDir, rtManifest); err != nil {
		return nil, fmt.Errorf("verify portable python image: %w", err)
	}

	catManifest, err := pythoncatalog.LoadImageManifest(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("load catalog image manifest: %w", err)
	}

	wasmPath, _, _, err := pythonruntime.ResolveImagePaths(absImageDir, rtManifest)
	if err != nil {
		return nil, fmt.Errorf("resolve image wasm path: %w", err)
	}
	// #nosec G304 -- wasmPath is verified inside absImageDir via ResolveImagePaths and SHA-256 checked by VerifyImage.
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("read python.wasm: %w", err)
	}

	scratchRoot := strings.TrimSpace(cfg.ScratchRoot)
	var ownedScratch string
	if scratchRoot == "" {
		dir, err := os.MkdirTemp("", "pw-portable-python-scratch-*")
		if err != nil {
			return nil, fmt.Errorf("create scratch dir: %w", err)
		}
		scratchRoot = dir
		ownedScratch = dir
	}

	clk := cfg.Clock
	if clk == nil {
		clk = pythonruntime.NewSystemClock()
	}
	ent := cfg.Entropy
	if ent == nil {
		ent = pythonruntime.CryptoEntropy{}
	}

	defaultMemPages := cfg.DefaultMemoryLimitPages
	if defaultMemPages == 0 {
		defaultMemPages = DefaultMaxMemoryPages
	}
	rt, err := pythonruntime.NewWazeroRuntime(ctx, pythonruntime.RuntimeConfig{
		WasmBytes:               wasmBytes,
		ImageDir:                absImageDir,
		Manifest:                rtManifest,
		EnforceImportAudit:      true,
		UseTrustedBootstrap:     true,
		ScratchRoot:             scratchRoot,
		DefaultMemoryLimitPages: defaultMemPages,
		Clock:                   clk,
		Entropy:                 ent,
		ParkedBudget:            cfg.ParkedBudget,
	})
	if err != nil {
		if ownedScratch != "" {
			_ = os.RemoveAll(ownedScratch)
		}
		return nil, fmt.Errorf("initialize portable python wazero runtime: %w", err)
	}

	jobReg := cfg.JobRegistry
	if jobReg == nil {
		jobReg = pythonadmission.NewMemorySessionJobRegistry()
	}
	screener := cfg.SecretScreener
	if screener == nil {
		screener = pythonevidence.TestSecretScreener{}
	}
	retained := cfg.RetainedStore
	if retained == nil {
		retained = pythonevidence.NewTestRetainedArtifactStore(pythonevidence.RetainedArtifactConfig{
			Screener: screener,
			Now:      clk.Now,
		})
	}
	inlineBudget := cfg.InlineBudgetBytes
	if inlineBudget <= 0 {
		inlineBudget = DefaultInlineBudgetBytes
	}

	return &Service{
		imageDir:        absImageDir,
		ownedScratch:    ownedScratch,
		runtime:         rt,
		runtimeManifest: rtManifest,
		catalogManifest: catManifest,
		clock:           clk,
		broker:          cfg.Broker,
		jobRegistry:     jobReg,
		screener:        screener,
		retainedStore:   retained,
		receiptSink:     cfg.ReceiptSink,
		inlineBudget:    inlineBudget,
	}, nil
}

// ImageDir returns the verified image directory path.
func (s *Service) ImageDir() string { return s.imageDir }

// CatalogManifest returns the verified pythoncatalog.ImageManifest loaded from
// the image's shared image-manifest.json.
func (s *Service) CatalogManifest() pythoncatalog.ImageManifest { return s.catalogManifest }

// RuntimeManifest returns the verified pythonruntime.ImageManifest.
func (s *Service) RuntimeManifest() *pythonruntime.ImageManifest { return s.runtimeManifest }

// Runtime returns the underlying compiled WazeroRuntime.
func (s *Service) Runtime() *pythonruntime.WazeroRuntime { return s.runtime }

// RetainedStore returns the bounded, secret-screened RetainedArtifactStore
// holding overflow stdout/stderr artifacts.
func (s *Service) RetainedStore() *pythonevidence.RetainedArtifactStore { return s.retainedStore }

// PackageHelp renders on-demand package discovery help from the verified manifest.
func (s *Service) PackageHelp() string {
	return pythoncatalog.RenderPackageHelp(s.catalogManifest)
}

// Close shuts down the compiled Wasm runtime and cleans up owned scratch state.
func (s *Service) Close(ctx context.Context) error {
	var err error
	if s.runtime != nil {
		err = s.runtime.Close(ctx)
	}
	if s.ownedScratch != "" {
		_ = os.RemoveAll(s.ownedScratch)
	}
	return err
}

// ToolRequest configures one invocation of Service.ExecuteTool.
type ToolRequest struct {
	// RawArgs, when non-nil, is validated via pythoncatalog.ValidateDirectToolArgs
	// and populates Mode, Code, Script, Module, CWD, and Args.
	RawArgs map[string]any

	ExecutionID string
	CallID      string
	SessionID   string
	CheckoutID  string
	Actor       string
	View        string
	Surface     string
	ReadOnly    bool

	Mode   pythonruntime.ExecutionMode
	Code   string
	Script string
	Module string
	CLI    string
	CWD    string
	Args   []string
	Env    map[string]string
	Stdin  io.Reader

	// Workspace backing: supply either Store (+ RootSpecs) or Roots (+ optional Editor / MutationDoor).
	Store        pythondocuments.DocumentStore
	RootSpecs    []pythondocuments.RootSpec
	Roots        []pythondocuments.HostRootConfig
	Editor       pythondocuments.EditorBridge
	MutationDoor pythondocuments.MutationDoor

	// Admission & authority configuration.
	AllowedEffects *pythonadmission.AllowedEffects
	AllowedRoots   []string
	ProtectedPaths []string
	Policy         pythonadmission.PolicyEvaluator
	Recheck        pythonadmission.RecheckFunc
	Broker         *pythonadmission.ApprovalBroker
	JobRegistry    pythonadmission.SessionJobRegistry

	// Host-mediated HTTP configuration for requests.Session.
	HTTPClient         *http.Client
	AllowPlainHTTP     bool
	AllowLoopbackHTTP  bool
	CredentialScreener pythonadmission.CredentialScreener

	Limits            pythonruntime.ResourceLimits
	InlineBudgetBytes int
	AdmissionEvidence pythonadmission.EvidenceSink
	DocumentEvidence  pythondocuments.EvidenceSink
}

// ToolResponse carries the complete trusted host outcome of ExecuteTool.
type ToolResponse struct {
	ExecutionID              string                                   `json:"execution_id"`
	ExitCode                 int                                      `json:"exit_code"`
	CleanCompletion          bool                                     `json:"clean_completion"`
	Stdout                   string                                   `json:"stdout,omitempty"`
	Stderr                   string                                   `json:"stderr,omitempty"`
	InlineBudgetBytes        int                                      `json:"inline_budget_bytes,omitempty"`
	StdoutTruncated          bool                                     `json:"stdout_truncated,omitempty"`
	StderrTruncated          bool                                     `json:"stderr_truncated,omitempty"`
	RetainedStdoutArtifactID string                                   `json:"retained_stdout_artifact_id,omitempty"`
	RetainedStderrArtifactID string                                   `json:"retained_stderr_artifact_id,omitempty"`
	RedactedCount            int                                      `json:"redacted_count,omitempty"`
	Stop                     *pythonruntime.TerminalStop              `json:"stop,omitempty"`
	Settlement               *pythonruntime.SettlementReport          `json:"settlement,omitempty"`
	PreviewRecord            *pythonadmission.PreviewListenerRecord   `json:"preview,omitempty"`
	Diagnostic               *pythoncatalog.Diagnostic                `json:"diagnostic,omitempty"`
	Usage                    pythonruntime.ResourceUsage              `json:"usage"`
	EvidenceSummary          *pythonevidence.PythonActivityProjection `json:"evidence_summary,omitempty"`
	Preview                  *pythonadmission.PreviewListener         `json:"-"`
	AdmissionEvidence        *pythonadmission.MemoryEvidenceStore     `json:"-"`
	DocumentEvidence         *pythondocuments.EvidenceLog             `json:"-"`
}

// ExecuteTool validates direct tool arguments (when RawArgs is supplied),
// handles manifest-backed read-only pip inventory and on-demand help,
// dispatches unshadowed `python -m http.server` to StartPreviewListener, and
// executes code/script/module/cli/stdin inside real CPython/WASI with full
// overlay settlement, admission observation, and host-mediated requests.Session.
func (s *Service) ExecuteTool(ctx context.Context, req ToolRequest) (*ToolResponse, error) {
	if s == nil || s.runtime == nil {
		return nil, errors.New("portable python service is not initialized")
	}

	if req.RawArgs != nil {
		validated, err := pythoncatalog.ValidateDirectToolArgs(req.RawArgs)
		if err != nil {
			return nil, err
		}
		req.Mode = catalogModeToRuntimeMode(validated.Mode)
		req.Code = validated.Code
		req.Script = validated.Script
		req.Module = validated.Module
		req.CWD = validated.Cwd
		req.Args = validated.Args
	}

	execID := strings.TrimSpace(req.ExecutionID)
	if execID == "" {
		execID = fmt.Sprintf("py-exec-%d", s.execSeq.Add(1))
	}

	// 1. Read-only pip inventory and on-demand package help directly from the
	// verified manifest (no WASI startup required).
	if req.Mode == pythonruntime.ModeModule && strings.TrimSpace(req.Module) == "pip" {
		return s.executePipModule(execID, req.Args)
	}
	if req.Mode == pythonruntime.ModeModule && strings.TrimSpace(req.Module) == "portablepython.help" {
		return &ToolResponse{
			ExecutionID:     execID,
			ExitCode:        0,
			CleanCompletion: true,
			Stdout:          s.PackageHelp(),
		}, nil
	}

	// Build per-invocation evidence sinks.
	var memAdm *pythonadmission.MemoryEvidenceStore
	admEvidence := req.AdmissionEvidence
	if admEvidence == nil {
		memAdm = pythonadmission.NewMemoryEvidenceStore()
		admEvidence = memAdm
	}
	var memDoc *pythondocuments.EvidenceLog
	docEvidence := req.DocumentEvidence
	if docEvidence == nil {
		memDoc = pythondocuments.NewEvidenceLog()
		docEvidence = memDoc
	}

	// Apply production default limits whenever any limit field is unset.
	limits := applyDefaultLimits(req.Limits)

	// Build per-invocation WorkspaceAdapter (PY03 OverlayAdapter).
	store, rootSpecs := buildDocumentStore(req)
	overlay := pythondocuments.NewOverlayAdapter(pythondocuments.OverlayConfig{
		ExecutionID: execID,
		Store:       store,
		Roots:       rootSpecs,
		CWD:         req.CWD,
		Evidence:    docEvidence,
		Now:         s.clock.Now,
	})

	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopLatch := pythonruntime.NewStopLatch(cancel)

	broker := req.Broker
	if broker == nil {
		broker = s.broker
	}
	protectedPaths := mergeProtectedPaths(DefaultProtectedPaths, req.ProtectedPaths)
	observer := pythonadmission.NewAdmissionObserver(pythonadmission.ObserverConfig{
		ExecutionID:    execID,
		CallID:         req.CallID,
		Actor:          req.Actor,
		View:           req.View,
		Surface:        req.Surface,
		ReadOnly:       req.ReadOnly,
		AllowedEffects: req.AllowedEffects,
		AllowedRoots:   req.AllowedRoots,
		ProtectedPaths: protectedPaths,
		MaxHostWork:    limits.MaxHostWork,
		StopLatch:      stopLatch,
		Broker:         broker,
		Clock:          s.clock,
		Policy:         req.Policy,
		Recheck:        req.Recheck,
		Evidence:       admEvidence,
	})

	// 2. Dispatch `python -m http.server` to host-managed StartPreviewListener
	// unless shadowed by a workspace module (http/server.py or http.py).
	if req.Mode == pythonruntime.ModeModule && strings.TrimSpace(req.Module) == "http.server" {
		selected, _ := pythonadmission.CheckStdlibHTTPServerSelected(execCtx, overlay, req.CWD)
		if selected {
			jobReg := req.JobRegistry
			if jobReg == nil {
				jobReg = s.jobRegistry
			}
			pl, err := pythonadmission.StartPreviewListener(execCtx, pythonadmission.PreviewConfig{
				ExecutionID: execID,
				CallID:      req.CallID,
				CWD:         req.CWD,
				Args:        req.Args,
				Workspace:   overlay,
				Observer:    observer,
				JobRegistry: jobReg,
				StopLatch:   stopLatch,
				Clock:       s.clock,
				Evidence:    admEvidence,
			})
			if err != nil {
				return &ToolResponse{
					ExecutionID:       execID,
					ExitCode:          1,
					CleanCompletion:   false,
					Stderr:            err.Error() + "\n",
					Stop:              stopLatch.Stop(),
					AdmissionEvidence: memAdm,
					DocumentEvidence:  memDoc,
				}, err
			}
			rec := pl.Record()
			return &ToolResponse{
				ExecutionID:       execID,
				ExitCode:          0,
				CleanCompletion:   true,
				Stdout:            fmt.Sprintf("Serving preview %s on http://%s (directory: %s)\n", rec.JobID, rec.Address, rec.Directory),
				Preview:           pl,
				PreviewRecord:     &rec,
				AdmissionEvidence: memAdm,
				DocumentEvidence:  memDoc,
			}, nil
		}
	}

	// 3. Build per-invocation fail-closed HTTPTransport + GuestHTTPAdapter + WASI bridge.
	credScreener := req.CredentialScreener
	if credScreener == nil {
		credScreener = pythonadmission.StrictCredentialScreener()
	}
	httpTransport := pythonadmission.NewHTTPTransport(pythonadmission.HTTPTransportConfig{
		ExecutionID:        execID,
		CallID:             req.CallID,
		Observer:           observer,
		Client:             req.HTTPClient,
		AllowPlainHTTP:     req.AllowPlainHTTP,
		AllowLoopbackHTTP:  req.AllowLoopbackHTTP,
		CredentialScreener: credScreener,
		Clock:              s.clock,
		Evidence:           admEvidence,
	})
	guestHTTPAdapter := pythonadmission.NewGuestHTTPAdapter(httpTransport)
	httpBridgeFS := NewGuestHTTPBridgeFS(execCtx, guestHTTPAdapter, stopLatch)

	guestCWD := pythonruntime.DefaultGuestWorkspaceMount
	if relCWD := strings.Trim(strings.TrimSpace(req.CWD), "/"); relCWD != "" && relCWD != "." {
		guestCWD = pythonruntime.DefaultGuestWorkspaceMount + "/" + relCWD
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	res, err := s.runtime.Execute(execCtx, pythonruntime.Invocation{
		ExecutionID: execID,
		Actor:       req.Actor,
		View:        req.View,
		Surface:     req.Surface,
		Mode:        req.Mode,
		Code:        req.Code,
		Script:      req.Script,
		Module:      req.Module,
		CLI:         req.CLI,
		CWD:         guestCWD,
		Args:        req.Args,
		Env:         req.Env,
		Stdin:       req.Stdin,
		Stdout:      &stdoutBuf,
		Stderr:      &stderrBuf,
		Limits:      limits,
		Workspace:   overlay,
		Observer:    observer,
		StopLatch:   stopLatch,
		ExtraSysMounts: map[string]experimentalsys.FS{
			GuestHTTPBridgeMount: httpBridgeFS,
		},
		HTTPBridgePath: GuestHTTPBridgePath,
	})
	if err != nil {
		return nil, err
	}

	stderrOut := stderrBuf.String()
	if stderrOut == "" && res.GuestStderr != "" {
		stderrOut = res.GuestStderr
	}

	resp := &ToolResponse{
		ExecutionID:       res.ExecutionID,
		ExitCode:          res.ExitCode,
		CleanCompletion:   res.CleanCompletion,
		Stdout:            stdoutBuf.String(),
		Stderr:            stderrOut,
		Stop:              res.Stop,
		Settlement:        res.Settlement,
		Usage:             res.Usage,
		AdmissionEvidence: memAdm,
		DocumentEvidence:  memDoc,
	}
	s.finalizeResponse(ctx, req, resp, memAdm, memDoc)
	return resp, nil
}

func applyDefaultLimits(in pythonruntime.ResourceLimits) pythonruntime.ResourceLimits {
	out := in
	if out.Timeout <= 0 {
		out.Timeout = defaultExecutionTimeout
	}
	if out.MaxMemoryPages == 0 {
		out.MaxMemoryPages = DefaultMaxMemoryPages
	}
	if out.MaxGuestWork == 0 {
		out.MaxGuestWork = DefaultMaxGuestWork
	}
	if out.MaxHostWork == 0 {
		out.MaxHostWork = DefaultMaxHostWork
	}
	if out.MaxOutputBytes <= 0 {
		out.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if out.MaxStagedBytes <= 0 {
		out.MaxStagedBytes = DefaultMaxStagedBytes
	}
	return out
}

func mergeProtectedPaths(defaults, extra []string) []string {
	seen := make(map[string]struct{}, len(defaults)+len(extra))
	out := make([]string, 0, len(defaults)+len(extra))
	for _, p := range defaults {
		if p == "" {
			continue
		}
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	for _, p := range extra {
		if p == "" {
			continue
		}
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out
}

// finalizeResponse screens stdout/stderr/stop text for secrets, enforces the
// inline output budget (retaining overflow in RetainedArtifactStore), records
// execution/admission/document/publication evidence into pythonevidence, and
// attaches a non-nil Den-ready EvidenceSummary to resp.
func (s *Service) finalizeResponse(
	ctx context.Context,
	req ToolRequest,
	resp *ToolResponse,
	memAdm *pythonadmission.MemoryEvidenceStore,
	memDoc *pythondocuments.EvidenceLog,
) {
	if resp == nil {
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = "default"
	}
	actor := strings.TrimSpace(req.Actor)
	if actor == "" {
		actor = "agent"
	}
	view := strings.TrimSpace(req.View)
	if view == "" {
		view = "workspace"
	}
	parentOpID := strings.TrimSpace(req.CallID)
	if parentOpID == "" {
		parentOpID = resp.ExecutionID
	}

	budget := req.InlineBudgetBytes
	if budget <= 0 {
		budget = s.inlineBudget
	}
	if budget <= 0 {
		budget = DefaultInlineBudgetBytes
	}
	resp.InlineBudgetBytes = budget

	screenedStdout, redStdout := pythonevidence.ScreenText(s.screener, resp.Stdout)
	screenedStderr, redStderr := pythonevidence.ScreenText(s.screener, resp.Stderr)
	redactedTotal := redStdout + redStderr

	if resp.Stop != nil && resp.Stop.Reason != "" {
		cp := *resp.Stop
		screenedReason, redStop := pythonevidence.ScreenText(s.screener, cp.Reason)
		cp.Reason = screenedReason
		redactedTotal += redStop
		resp.Stop = &cp
	}

	// Enforce inline budget and retain overflow in RetainedArtifactStore.
	if len(screenedStdout) > budget {
		resp.StdoutTruncated = true
		if s.retainedStore != nil {
			if summary, err := s.retainedStore.Put(ctx, pythonevidence.PutRequest{
				SessionID:         sessionID,
				CheckoutID:        req.CheckoutID,
				Actor:             actor,
				View:              view,
				ExecutionID:       resp.ExecutionID,
				ParentOperationID: parentOpID,
				Producer:          pythonevidence.ProducerPython,
				MediaType:         "text/plain",
				Data:              []byte(screenedStdout),
			}); err == nil {
				resp.RetainedStdoutArtifactID = summary.ArtifactID
			}
		}
		resp.Stdout = screenedStdout[:budget]
	} else {
		resp.Stdout = screenedStdout
	}

	if len(screenedStderr) > budget {
		resp.StderrTruncated = true
		if s.retainedStore != nil {
			if summary, err := s.retainedStore.Put(ctx, pythonevidence.PutRequest{
				SessionID:         sessionID,
				CheckoutID:        req.CheckoutID,
				Actor:             actor,
				View:              view,
				ExecutionID:       resp.ExecutionID,
				ParentOperationID: parentOpID,
				Producer:          pythonevidence.ProducerPython,
				MediaType:         "text/plain",
				Data:              []byte(screenedStderr),
			}); err == nil {
				resp.RetainedStderrArtifactID = summary.ArtifactID
			}
		}
		resp.Stderr = screenedStderr[:budget]
	} else {
		resp.Stderr = screenedStderr
	}
	resp.RedactedCount = redactedTotal

	// Populate pythonevidence.ExecutionLedger and project EvidenceSummary.
	submittedSource := req.Code
	if submittedSource == "" {
		submittedSource = req.Script
	}
	if submittedSource == "" {
		submittedSource = req.Module
	}
	execRec := pythonevidence.ExecutionRecord{
		ExecutionID:     resp.ExecutionID,
		CallID:          parentOpID,
		SessionID:       sessionID,
		CheckoutID:      req.CheckoutID,
		Actor:           actor,
		View:            view,
		Surface:         req.Surface,
		SubmittedSource: submittedSource,
		RuntimeImageID:  s.runtimeManifest.ImageID,
		StartedAt:       s.clock.Now().UTC(),
	}
	var ledger *pythonevidence.ExecutionLedger
	if s.receiptSink != nil {
		if prodLedger, err := pythonevidence.NewProductionExecutionLedger(ctx, execRec, s.screener, s.receiptSink, s.clock.Now); err == nil {
			ledger = prodLedger
		}
	}
	if ledger == nil {
		ledger = pythonevidence.NewExecutionLedger(execRec, s.screener, s.clock.Now)
	}

	if memAdm != nil {
		for _, adm := range memAdm.Admissions() {
			ledger.RecordAdmission(adm, parentOpID, nil)
		}
		for _, h := range memAdm.HTTP() {
			_ = ledger.RecordHTTP(h, parentOpID)
		}
		for _, pl := range memAdm.PreviewListeners() {
			_ = ledger.RecordPreviewListener(pl, parentOpID)
		}
		for _, sub := range memAdm.Subprocesses() {
			_ = ledger.RecordSubprocess(sub, parentOpID)
		}
	}
	if memDoc != nil {
		for _, r := range memDoc.Reads() {
			witnesses := 0
			if r.BytesDeliveredToGuest > 0 {
				witnesses = 1
			}
			_ = ledger.RecordDocumentRead(r, parentOpID, witnesses, nil, nil)
		}
		for _, ev := range memDoc.OverlayEvents() {
			_ = ledger.RecordOverlayEvent(ev, parentOpID, nil, nil)
		}
		for i, pub := range memDoc.Publications() {
			pubID := fmt.Sprintf("pub-%s-%d", resp.ExecutionID, i+1)
			_ = ledger.RecordPublication(pub, parentOpID, pubID, pythonevidence.AttributionPythonWorkspacePublication, nil, nil)
		}
	}

	if resp.Stdout != "" || resp.RetainedStdoutArtifactID != "" {
		ledger.RecordDerivedOutput("stdout", resp.Stdout, resp.RetainedStdoutArtifactID)
		ledger.RecordScreenedModelOutput(resp.Stdout)
	}
	if resp.Stderr != "" || resp.RetainedStderrArtifactID != "" {
		ledger.RecordDerivedOutput("stderr", resp.Stderr, resp.RetainedStderrArtifactID)
	}
	ledger.RecordCompletion(resp.CleanCompletion, resp.ExitCode, resp.Stop)

	scope := pythonevidence.ViewerScope{
		Role:       pythonevidence.RoleCoordinator,
		SessionID:  sessionID,
		CheckoutID: req.CheckoutID,
		Actor:      actor,
		View:       view,
	}
	if proj, err := pythonevidence.ProjectForDen(ledger, scope); err == nil {
		resp.EvidenceSummary = &proj
	}
}

func (s *Service) executePipModule(execID string, args []string) (*ToolResponse, error) {
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		return &ToolResponse{
			ExecutionID:     execID,
			ExitCode:        0,
			CleanCompletion: true,
			Stdout:          s.PackageHelp(),
		}, nil
	}
	pipRes, err := pythoncatalog.ExecuteReadOnlyPip(s.catalogManifest, args)
	if err != nil {
		return nil, err
	}
	return &ToolResponse{
		ExecutionID:     execID,
		ExitCode:        pipRes.ExitStatus,
		CleanCompletion: pipRes.ExitStatus == 0,
		Stdout:          pipRes.Stdout,
		Stderr:          pipRes.Stderr,
		Diagnostic:      pipRes.Diagnostic,
	}, nil
}

func buildDocumentStore(req ToolRequest) (pythondocuments.DocumentStore, []pythondocuments.RootSpec) {
	if req.Store != nil {
		return req.Store, req.RootSpecs
	}
	if len(req.Roots) == 0 {
		mem := pythondocuments.NewMemoryDocumentStore()
		specs := req.RootSpecs
		if len(specs) == 0 {
			specs = []pythondocuments.RootSpec{{
				ID:       "default",
				Label:    "workspace",
				Default:  true,
				ReadOnly: req.ReadOnly,
			}}
		}
		return mem, specs
	}
	// Fail closed: never construct a parallel disk/fseffect write path here.
	// Callers must inject Painted Wolf's single apply-and-record MutationDoor
	// (e.g. native.NewPythonMutationDoor). When MutationDoor is nil,
	// HostDocumentStore allows reads and rejects any workspace write with
	// pythondocuments.ErrMutationDoorUnavailable.
	store := pythondocuments.NewHostDocumentStore(req.Roots, req.Editor, req.MutationDoor)
	specs := req.RootSpecs
	if len(specs) == 0 {
		specs = make([]pythondocuments.RootSpec, 0, len(req.Roots))
		for i, r := range req.Roots {
			id := r.ID
			if id == "" {
				id = "default"
			}
			specs = append(specs, pythondocuments.RootSpec{
				ID:       id,
				Label:    id,
				Branch:   r.Branch,
				Default:  i == 0,
				ReadOnly: r.ReadOnly,
			})
		}
	}
	return store, specs
}

func catalogModeToRuntimeMode(m pythoncatalog.ExecutionMode) pythonruntime.ExecutionMode {
	switch m {
	case pythoncatalog.ModeCode:
		return pythonruntime.ModeCode
	case pythoncatalog.ModeScript:
		return pythonruntime.ModeScript
	case pythoncatalog.ModeModule:
		return pythonruntime.ModeModule
	case pythoncatalog.ModeStdin:
		return pythonruntime.ModeStdin
	default:
		return pythonruntime.ExecutionMode(m)
	}
}
