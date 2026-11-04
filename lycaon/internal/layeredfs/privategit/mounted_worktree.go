package privategit

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// SecondaryViewID names the managed view of a non-primary root of a mounted view. It has its own
// private repositories and worktree on the same Core as the primary root.
func SecondaryViewID(viewID, rootID string) string { return viewID + "-" + rootID }

// BindSecondaryRoot claims one non-primary root of a mounted view, which binding describes under the
// primary view's identity.
func (m *Manager) BindSecondaryRoot(binding MountedViewBinding, authorizedGitDirs ...string) error {
	binding.CoreViewID = binding.ViewID
	binding.ViewID = SecondaryViewID(binding.ViewID, binding.RootID)
	binding.WorkerID = binding.ViewID
	if err := m.ReleaseView(binding.ViewID); err != nil {
		return err
	}
	if err := m.MountedClaimWithBinding(binding, authorizedGitDirs...); err != nil {
		return err
	}
	view, err := getManagedView(m, binding.ViewID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	view.coreViewID = binding.CoreViewID
	m.mu.Unlock()
	return nil
}

// SecondaryViews names the managed views of a mounted view's non-primary roots.
func (m *Manager) SecondaryViews(viewID string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var ids []string
	for id, view := range m.views {
		if view.coreViewID == viewID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func mountBindingKey(viewID, rootID string) string {
	return strings.TrimSpace(viewID) + "\x00" + strings.TrimSpace(rootID)
}

func isValidMountedAdapterID(adapterID string) bool {
	switch strings.ToLower(strings.TrimSpace(adapterID)) {
	case "", "eagercopy", "materialized", "none":
		return false
	default:
		return true
	}
}

func hasMountIdentityFact(b MountedViewBinding) bool {
	return strings.TrimSpace(b.MountID) != "" || strings.TrimSpace(b.ConnectionID) != ""
}

func isBindingLive(b MountedViewBinding) bool {
	return b.Active && (b.IsActive == nil || b.IsActive())
}

// RegisterMountedViewBinding registers an adapter/supervisor-owned MountedViewBinding
// for (binding.ViewID, binding.RootID) after verifying its identity and lifecycle facts (#1671).
func (m *Manager) RegisterMountedViewBinding(binding MountedViewBinding) error {
	if m == nil {
		return fmt.Errorf("%w: nil manager", ErrMountedWorktreeUnavailable)
	}
	viewID := strings.TrimSpace(binding.ViewID)
	rootID := strings.TrimSpace(binding.RootID)
	if viewID == "" || rootID == "" {
		return fmt.Errorf("%w: binding ViewID and RootID are required", ErrMountedWorktreeUnavailable)
	}
	m.mu.RLock()
	existingView, hasView := m.views[viewID]
	m.mu.RUnlock()

	expWorker, expRoot, expTrunk, expCore := binding.WorkerID, rootID, binding.TrunkDir, binding.Core
	if hasView && existingView != nil {
		expWorker, expRoot, expTrunk, expCore = existingView.workerID, existingView.rootID, existingView.trunkDir, existingView.core
	}
	absWT, err := validateMountedViewBinding(binding, viewID, expWorker, expRoot, expTrunk, binding.MountPath, expCore)
	if err != nil {
		return err
	}
	binding.MountPath = absWT
	m.mu.Lock()
	m.mountBindings[mountBindingKey(viewID, rootID)] = binding
	m.mu.Unlock()
	return nil
}

// RevokeMountedViewBinding revokes and deactivates any registered MountedViewBinding
// for (viewID, rootID) when the mount supervisor releases or unmounts the view (#1671).
func (m *Manager) RevokeMountedViewBinding(viewID, rootID string) {
	if m == nil {
		return
	}
	if strings.TrimSpace(rootID) == "" {
		rootID = "primary"
	}
	m.mu.Lock()
	delete(m.mountBindings, mountBindingKey(viewID, rootID))
	delete(m.mountBindings, mountBindingKey(SecondaryViewID(viewID, rootID), rootID))
	m.mu.Unlock()
}

// MountedClaimWithBinding claims a mounted layeredfs.Core view and binds it to an
// adapter/supervisor-owned MountedViewBinding without scanning or copying trunk (#1671).
func (m *Manager) MountedClaimWithBinding(binding MountedViewBinding, authorizedGitDirs ...string) error {
	if m == nil {
		return fmt.Errorf("%w: nil manager", ErrMountedWorktreeUnavailable)
	}
	viewID := strings.TrimSpace(binding.ViewID)
	rootID := strings.TrimSpace(binding.RootID)
	workerID := strings.TrimSpace(binding.WorkerID)
	if workerID == "" {
		workerID = viewID
		binding.WorkerID = workerID
	}
	absWT, err := validateMountedViewBinding(binding, viewID, workerID, rootID, binding.TrunkDir, binding.MountPath, binding.Core)
	if err != nil {
		return err
	}
	absTrunk, err := filepath.Abs(binding.TrunkDir)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMountedWorktreeUnavailable, err)
	}
	binding.TrunkDir, binding.MountPath = absTrunk, absWT
	if err := registerMountedClaim(m, viewID, workerID, rootID, absTrunk, absWT, binding.Core, authorizedGitDirs...); err != nil {
		return fmt.Errorf("%w: %v", ErrMountedWorktreeUnavailable, err)
	}
	m.mu.Lock()
	m.mountBindings[mountBindingKey(viewID, rootID)] = binding
	m.mu.Unlock()
	return nil
}

// BindMountedView binds or updates the active adapter/supervisor-owned MountedViewBinding
// for an existing mounted view after verifying its ViewID, RootID, Core, TrunkDir, and liveness (#1671).
func (m *Manager) BindMountedView(binding MountedViewBinding) error {
	if m == nil {
		return fmt.Errorf("%w: nil manager", ErrMountedWorktreeUnavailable)
	}
	view, err := getManagedView(m, strings.TrimSpace(binding.ViewID))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMountedWorktreeUnavailable, err)
	}
	absWT, err := validateMountedViewBinding(binding, view.viewID, view.workerID, view.rootID, view.trunkDir, binding.MountPath, view.core)
	if err != nil {
		return err
	}
	binding.MountPath = absWT
	kernelMounted := isKernelMountedWorktree(absWT)
	unregisterActiveView(view)
	m.mu.Lock()
	if oldWT := canonicalCleanPath(view.worktreeDir); oldWT != "" {
		delete(m.viewsByWorktree, oldWT)
	}
	view.worktreeDir = absWT
	view.kernelMounted = kernelMounted
	if newWT := canonicalCleanPath(absWT); newWT != "" {
		m.viewsByWorktree[newWT] = view
	}
	m.mountBindings[mountBindingKey(view.viewID, view.rootID)] = binding
	m.mu.Unlock()
	registerActiveView(m, view)
	return nil
}

// MountedClaimWithWorktree claims a mounted view against a pre-registered active
// MountedViewBinding matching (viewID, rootID, trunkDir, mountWorktreeDir, core) (#1671).
func (m *Manager) MountedClaimWithWorktree(
	viewID, workerID, rootID, trunkDir, mountWorktreeDir string,
	core *layeredfs.Core,
	authorizedGitDirs ...string,
) error {
	if viewID == "" || trunkDir == "" || core == nil {
		return fmt.Errorf("%w: viewID, trunkDir, and core are required", ErrMountedWorktreeUnavailable)
	}
	if strings.TrimSpace(rootID) == "" {
		rootID = "primary"
	}
	binding, ok := getMountedViewBinding(m, viewID, rootID)
	if !ok {
		return fmt.Errorf("%w: no active mounted view binding registered for view %q root %q", ErrMountedWorktreeUnavailable, viewID, rootID)
	}
	absWT, err := validateMountedViewBinding(binding, viewID, workerID, rootID, trunkDir, mountWorktreeDir, core)
	if err != nil {
		return err
	}
	absTrunk, err := filepath.Abs(trunkDir)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMountedWorktreeUnavailable, err)
	}
	return registerMountedClaim(m, viewID, workerID, rootID, absTrunk, absWT, core, authorizedGitDirs...)
}

// BindMountedWorktree binds mountWorktreeDir to viewID after verifying that an active
// MountedViewBinding is registered for (viewID, view.rootID) and matches mountWorktreeDir (#1671).
func (m *Manager) BindMountedWorktree(viewID, mountWorktreeDir string) error {
	view, err := getManagedView(m, viewID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMountedWorktreeUnavailable, err)
	}
	binding, ok := getMountedViewBinding(m, view.viewID, view.rootID)
	if !ok {
		return fmt.Errorf("%w: no active mounted view binding registered for view %q root %q", ErrMountedWorktreeUnavailable, view.viewID, view.rootID)
	}
	if _, err := validateMountedViewBinding(binding, view.viewID, view.workerID, view.rootID, view.trunkDir, mountWorktreeDir, view.core); err != nil {
		return err
	}
	return m.BindMountedView(binding)
}

func getMountedViewBinding(m *Manager, viewID, rootID string) (MountedViewBinding, bool) {
	if m == nil {
		return MountedViewBinding{}, false
	}
	m.mu.RLock()
	b, ok := m.mountBindings[mountBindingKey(viewID, rootID)]
	m.mu.RUnlock()
	return b, ok
}

func registerMountedClaim(
	m *Manager,
	viewID, workerID, rootID, absTrunk, absWT string,
	core *layeredfs.Core,
	authorizedGitDirs ...string,
) error {
	if rootID == "" {
		rootID = "primary"
	}
	if workerID == "" {
		workerID = viewID
	}
	view := &managedView{
		viewID: viewID, workerID: workerID, rootID: rootID,
		trunkDir: absTrunk, worktreeDir: absWT, materialized: false,
		kernelMounted: isKernelMountedWorktree(absWT),
		core:          core, authorizedGitDirs: append([]string(nil), authorizedGitDirs...),
		accessOrigin: OriginFilesystem, repos: make(map[string]*initializedRepo),
	}
	if err := core.RegisterLowerProvider(rootID, &gitAwareLowerProvider{
		mgr: m, viewID: viewID, rootID: rootID, trunkDir: absTrunk,
	}); err != nil {
		return err
	}
	registerManagedView(m, view)
	return nil
}

func verifyActiveMountedView(mgr *Manager, view *managedView, repoLayoutWorktreeDir, targetWorktreeDir string) error {
	if mgr == nil || view == nil || strings.TrimSpace(view.worktreeDir) == "" {
		return fmt.Errorf("%w: mounted view has no bound worktree", ErrMountedWorktreeUnavailable)
	}
	binding, ok := getMountedViewBinding(mgr, view.viewID, view.rootID)
	if !ok {
		return fmt.Errorf("%w: view %q root %q has no registered mounted view binding", ErrMountedWorktreeUnavailable, view.viewID, view.rootID)
	}
	if view.kernelMounted {
		if binding.ViewID != view.viewID || binding.RootID != view.rootID || (view.workerID != "" && binding.WorkerID != "" && binding.WorkerID != view.workerID) || binding.Core != view.core || !isValidMountedAdapterID(binding.AdapterID) || !hasMountIdentityFact(binding) || !isBindingLive(binding) || filepath.Clean(binding.MountPath) != filepath.Clean(view.worktreeDir) {
			return fmt.Errorf("%w: kernel-mounted view %q binding is inactive or mismatched", ErrMountedWorktreeUnavailable, view.viewID)
		}
		return nil
	}
	if _, err := validateMountedViewBinding(binding, view.viewID, view.workerID, view.rootID, view.trunkDir, view.worktreeDir, view.core); err != nil {
		return err
	}
	if repoLayoutWorktreeDir != "" && targetWorktreeDir != "" {
		_, err := validateMountedWorktreeDir(repoLayoutWorktreeDir, targetWorktreeDir)
		return err
	}
	return nil
}

func validateMountedViewBinding(
	b MountedViewBinding,
	expectedViewID, expectedWorkerID, expectedRootID, expectedTrunkDir, expectedMountDir string,
	expectedCore *layeredfs.Core,
) (string, error) {
	if strings.TrimSpace(b.ViewID) == "" || b.ViewID != expectedViewID {
		return "", fmt.Errorf("%w: binding ViewID %q does not match view %q", ErrMountedWorktreeUnavailable, b.ViewID, expectedViewID)
	}
	if strings.TrimSpace(b.RootID) == "" || b.RootID != expectedRootID {
		return "", fmt.Errorf("%w: binding RootID %q does not match root %q", ErrMountedWorktreeUnavailable, b.RootID, expectedRootID)
	}
	if expectedWorkerID != "" && strings.TrimSpace(b.WorkerID) != "" && b.WorkerID != expectedWorkerID {
		return "", fmt.Errorf("%w: binding WorkerID %q does not match worker %q", ErrMountedWorktreeUnavailable, b.WorkerID, expectedWorkerID)
	}
	coreViewID := b.CoreViewID
	if coreViewID == "" {
		coreViewID = expectedViewID
	}
	if b.Core == nil || expectedCore == nil || b.Core != expectedCore || b.Core.ViewID() != coreViewID {
		return "", fmt.Errorf("%w: binding Core instance does not match view %q", ErrMountedWorktreeUnavailable, expectedViewID)
	}
	if !isValidMountedAdapterID(b.AdapterID) || !hasMountIdentityFact(b) || !isBindingLive(b) {
		return "", fmt.Errorf("%w: binding for view %q has invalid adapter, missing MountID, or inactive lifecycle", ErrMountedWorktreeUnavailable, expectedViewID)
	}
	cleanBT, cleanET := canonicalCleanPath(b.TrunkDir), canonicalCleanPath(expectedTrunkDir)
	cleanBM, cleanEM := canonicalCleanPath(b.MountPath), canonicalCleanPath(expectedMountDir)
	if cleanBT == "" || cleanBT != cleanET || cleanBM == "" || cleanBM != cleanEM {
		return "", fmt.Errorf("%w: binding TrunkDir/MountPath mismatch for view %q", ErrMountedWorktreeUnavailable, expectedViewID)
	}
	return validateMountedWorktreeDir(expectedTrunkDir, expectedMountDir)
}

func validateMountedWorktreeDir(trunkDir, mountWorktreeDir string) (string, error) {
	if strings.TrimSpace(mountWorktreeDir) == "" || strings.TrimSpace(trunkDir) == "" {
		return "", fmt.Errorf("%w: trunk or mounted worktree path is empty", ErrMountedWorktreeUnavailable)
	}
	absWT, err := filepath.Abs(mountWorktreeDir)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrMountedWorktreeUnavailable, err)
	}
	absTrunk, err := filepath.Abs(trunkDir)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrMountedWorktreeUnavailable, err)
	}
	wtInfo, wtErr := os.Lstat(absWT)
	trunkInfo, tErr := os.Lstat(absTrunk)
	if wtErr != nil || !wtInfo.IsDir() || tErr != nil || !trunkInfo.IsDir() || os.SameFile(trunkInfo, wtInfo) {
		return "", fmt.Errorf("%w: %q is not an isolated mounted directory", ErrMountedWorktreeUnavailable, absWT)
	}
	cleanWT, cleanTrunk := canonicalCleanPath(absWT), canonicalCleanPath(absTrunk)
	if cleanWT == "" || cleanTrunk == "" || cleanWT == cleanTrunk || isUnderAny(cleanWT, []string{filepath.Join(cleanTrunk, ".git")}) {
		return "", fmt.Errorf("%w: mounted worktree %q aliases canonical trunk %q", ErrMountedWorktreeUnavailable, absWT, trunkDir)
	}
	return absWT, nil
}
