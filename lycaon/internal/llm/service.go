package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/pkg/api"
)

// Service bundles provider settings dependencies for HTTP handlers and main.
type Service struct {
	mutationMu             sync.Mutex
	modelFeedRefreshCancel context.CancelFunc
	modelFeedRefreshDone   <-chan struct{}
	Catalog                *ProviderCatalog
	Credentials            *providercredentials.Store
	Registry               *Registry
	Policy                 *PolicyStore
	Router                 *StaticModelRouter
	Mock                   modelcall.LLMClient
	// Utility shares slot health, single-flight, and service budgets.
	Utility *UtilityPlane
	// Capacity is the coordinator/worker overload plane.
	Capacity *CapacityGate
	// Refusals records pairs a host declined to serve.
	Refusals *providerretry.ModelRefusalGate
	// Lifecycle publishes occupied utility calls onto the session llm topic.
	Lifecycle   CallLifecycle
	Preparation ConversationPreparation
}

type PolicyValidationError struct{ Err error }

func (e *PolicyValidationError) Error() string { return e.Err.Error() }
func (e *PolicyValidationError) Unwrap() error { return e.Err }

// ApplyModelPolicy validates and persists a partial policy assignment.
func (s *Service) ApplyModelPolicy(ctx context.Context, scope SettingsScope, projectDir string, patch ModelPolicyPatch) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if patch.empty() {
		return &PolicyValidationError{Err: errors.New("model policy patch is empty")}
	}
	if err := validatePolicyShape(policyFromPatch(patch)); err != nil {
		return &PolicyValidationError{Err: err}
	}
	if s.Policy == nil {
		return errors.New("model policy store is not configured")
	}
	overlay, err := s.Policy.Overlay(scope, projectDir)
	if err != nil {
		return err
	}
	previousThinking := overlay.ThinkingOverrides
	overlay = applyPolicyPatch(overlay, patch)
	if err := validatePolicyShape(overlay); err != nil {
		return &PolicyValidationError{Err: err}
	}
	effective, err := s.Policy.effectiveWithOverlay(scope, overlay)
	if err != nil {
		return &PolicyValidationError{Err: err}
	}
	if patch.Coordinator != nil || patch.Lite != nil || patch.AgentPool != nil {
		if err := s.ValidatePolicyModels(ctx, effective); err != nil {
			return &PolicyValidationError{Err: err}
		}
	}
	if patch.ThinkingOverrides != nil {
		if err := s.validateThinkingPolicy(ctx, *patch.ThinkingOverrides, previousThinking); err != nil {
			return &PolicyValidationError{Err: err}
		}
	}
	if scope == SettingsScopeProject {
		if err := s.Policy.PutProject(projectDir, overlay); err != nil {
			return err
		}
		s.ResetPlanes()
		return nil
	}
	if err := s.Policy.PutGlobal(overlay); err != nil {
		return err
	}
	s.ResetPlanes()
	return nil
}

// ResetPlanes clears transient routing state after configuration changes.
func (s *Service) ResetPlanes() {
	if s == nil {
		return
	}
	if s.Utility != nil {
		s.Utility.Reset()
	}
	s.Refusals.Reset()
	if s.Capacity != nil {
		if s.Catalog != nil {
			s.Capacity.SetPolicy(s.Catalog.CapacityPolicy())
		}
		s.Capacity.Reset()
	}
}

// PutProvider replaces one provider atomically.
func (s *Service) PutProvider(ctx context.Context, entry ProviderEntry) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	previous, existed := s.Catalog.Get(entry.ID)
	if err := s.Catalog.Put(entry); err != nil {
		return err
	}
	if err := s.Registry.ReloadProviderMutation(ctx, entry.ID); err != nil {
		rollbackErr := s.restoreProvider(entry.ID, previous, existed)
		rollbackErr = errors.Join(rollbackErr, s.Registry.ReloadProviderMutation(ctx, entry.ID))
		return mutationRollbackError("put provider", err, rollbackErr)
	}
	s.ResetPlanes()
	return nil
}

// TrustProviderForSecrets records device-wide credential trust for the
// provider's current destination, attributed to the installing approval
// operation; a person's Settings choice passes none. It reports false when
// the provider resolves elsewhere or is already trusted.
func (s *Service) TrustProviderForSecrets(ctx context.Context, providerID, destination, operationID string) (bool, error) {
	existing, err := s.providerForTrust(providerID, destination)
	if err != nil || existing == nil || existing.SecretScreenTrust == destination {
		return false, err
	}
	entry := providerEntryOf(*existing)
	entry.SecretScreenTrust = destination
	entry.SecretScreenTrustOperation = strings.TrimSpace(operationID)
	if err := s.PutProvider(ctx, entry); err != nil {
		return false, err
	}
	return true, nil
}

// WithdrawProviderSecretTrust withdraws credential trust for the destination.
// A non-empty operationID withdraws only trust that operation installed, so a
// person's Settings choice survives an approval's rollback.
func (s *Service) WithdrawProviderSecretTrust(ctx context.Context, providerID, destination, operationID string) (bool, error) {
	existing, err := s.providerForTrust(providerID, destination)
	if err != nil || existing == nil || existing.SecretScreenTrust == "" {
		return false, err
	}
	if operationID = strings.TrimSpace(operationID); operationID != "" && existing.SecretScreenTrustOperation != operationID {
		return false, nil
	}
	entry := providerEntryOf(*existing)
	entry.SecretScreenTrust = ""
	entry.SecretScreenTrustOperation = ""
	if err := s.PutProvider(ctx, entry); err != nil {
		return false, err
	}
	return true, nil
}

// providerForTrust returns nil without error when the instance does not
// resolve to the named destination.
func (s *Service) providerForTrust(providerID, destination string) (*CatalogEntry, error) {
	providerID = strings.TrimSpace(providerID)
	destination = strings.TrimSpace(destination)
	if providerID == "" || destination == "" {
		return nil, fmt.Errorf("provider trust needs a provider and a destination")
	}
	existing, ok := s.Catalog.Get(providerID)
	if !ok {
		return nil, fmt.Errorf("provider %q is not configured", providerID)
	}
	if existing.SecretDestinationID() != destination {
		return nil, nil
	}
	return &existing, nil
}

// providerEntryOf projects a resolved catalog entry back onto the local
// instance shape PutProvider stores.
func providerEntryOf(e CatalogEntry) ProviderEntry {
	entry := ProviderEntry{
		ID: e.ID, Kind: e.Kind, Label: e.Label, BaseURL: e.BaseURL, EndpointStyle: e.EndpointStyle,
		APIKeyEnv: e.APIKeyEnv, RequiresAPIKey: e.LocalRequiresAPIKey(), AmbientAuth: e.AmbientAuth,
		Models: append([]modelinfo.Entry(nil), e.Models...), LocalFree: e.LocalFree,
		SecretScreenTrust: e.SecretScreenTrust, SecretScreenTrustOperation: e.SecretScreenTrustOperation,
		Platforms:        append([]string(nil), e.Platforms...),
		RejectionReasons: e.LocalRejectionReasons(),
	}
	if e.HTTPRetryOverride {
		retry := e.HTTPRetry.Clone()
		entry.HTTPRetryOverride = &retry
	}
	return entry
}

// DeleteProvider removes unreferenced provider state atomically.
func (s *Service) DeleteProvider(ctx context.Context, id string) (bool, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	previous, existed := s.Catalog.Get(id)
	if !existed {
		return false, nil
	}
	if scopes := s.Policy.ProviderReferenceScopes(id); len(scopes) != 0 {
		return false, &ProviderInUseError{ProviderID: id, Scopes: scopes}
	}
	credential, hadCredential := s.Credentials.Get(id)
	removed, err := s.Catalog.Remove(id)
	if err != nil || !removed {
		return removed, err
	}
	if err := s.Credentials.Delete(id); err != nil {
		rollbackErr := s.restoreProvider(id, previous, true)
		rollbackErr = errors.Join(rollbackErr, s.Registry.ReloadProviderMutation(ctx, id))
		return false, mutationRollbackError("delete provider credential", err, rollbackErr)
	}
	if err := s.Registry.ReloadProviderMutation(ctx, id); err != nil {
		rollbackErr := s.restoreProvider(id, previous, true)
		if hadCredential {
			rollbackErr = errors.Join(rollbackErr, s.Credentials.Set(id, credential.Value()))
		}
		rollbackErr = errors.Join(rollbackErr, s.Registry.ReloadProviderMutation(ctx, id))
		return false, mutationRollbackError("delete provider", err, rollbackErr)
	}
	return true, nil
}

func (s *Service) SetProviderCredential(ctx context.Context, id, apiKey string) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if _, exists := s.Catalog.Get(id); !exists {
		return &ProviderNotFoundError{ProviderID: id}
	}
	previous, existed := s.Credentials.Get(id)
	if err := s.Credentials.Set(id, apiKey); err != nil {
		return err
	}
	if err := s.Registry.ReloadProviderMutation(ctx, id); err != nil {
		var rollbackErr error
		if existed {
			rollbackErr = s.Credentials.Set(id, previous.Value())
		} else {
			rollbackErr = s.Credentials.Delete(id)
		}
		rollbackErr = errors.Join(rollbackErr, s.Registry.ReloadProviderMutation(ctx, id))
		return mutationRollbackError("set provider credential", err, rollbackErr)
	}
	return nil
}

func (s *Service) DeleteProviderCredential(ctx context.Context, id string) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if _, exists := s.Catalog.Get(id); !exists {
		return &ProviderNotFoundError{ProviderID: id}
	}
	previous, existed := s.Credentials.Get(id)
	if err := s.Credentials.Delete(id); err != nil {
		return err
	}
	if err := s.Registry.ReloadProviderMutation(ctx, id); err != nil {
		var rollbackErr error
		if existed {
			rollbackErr = s.Credentials.Set(id, previous.Value())
		}
		rollbackErr = errors.Join(rollbackErr, s.Registry.ReloadProviderMutation(ctx, id))
		return mutationRollbackError("delete provider credential", err, rollbackErr)
	}
	return nil
}

func (s *Service) restoreProvider(id string, entry CatalogEntry, existed bool) error {
	if !existed {
		_, err := s.Catalog.Remove(id)
		return err
	}
	requires := entry.RequiresAPIKey
	var httpRetryOverride *providerretry.ProviderHTTPRetry
	if entry.HTTPRetryOverride {
		cloned := entry.HTTPRetry.Clone()
		httpRetryOverride = &cloned
	}
	return s.Catalog.Put(ProviderEntry{
		ID: entry.ID, Kind: entry.Kind, Label: entry.Label, BaseURL: entry.BaseURL,
		EndpointStyle: entry.EndpointStyle, APIKeyEnv: entry.APIKeyEnv,
		RequiresAPIKey: &requires, AmbientAuth: entry.AmbientAuth,
		Models: entry.Models, LocalFree: entry.LocalFree,
		Platforms: entry.Platforms, HTTPRetryOverride: httpRetryOverride,
		RejectionReasons: entry.LocalRejectionReasons(),
	})
}

func mutationRollbackError(action string, cause, rollback error) error {
	if rollback == nil {
		return fmt.Errorf("%s: %w", action, cause)
	}
	return fmt.Errorf("%s: %w (rollback failed: %w)", action, cause, rollback)
}

// NewService loads catalog, credentials, policy, registry, and router.
func NewService(mock modelcall.LLMClient) (*Service, error) {
	catalog, err := NewProviderCatalog()
	if err != nil {
		return nil, err
	}
	credentials, err := providercredentials.New()
	if err != nil {
		return nil, err
	}
	policy, err := NewPolicyStore()
	if err != nil {
		return nil, err
	}
	registry, err := NewRegistry(catalog, credentials)
	if err != nil {
		return nil, err
	}
	registry.thinkingPolicy.Store(policy)
	feed, feedErr := modelfeed.New(modelfeed.Options{})
	if feedErr != nil {
		return nil, fmt.Errorf("modelfeed: %w", feedErr)
	}
	registry.SetModelFeed(feed)
	var refreshCancel context.CancelFunc
	var refreshDone <-chan struct{}
	if ProviderUtilityCallsEnabled() {
		refreshCancel, refreshDone = startModelFeedRefresh(feed)
	}
	router := NewStaticModelRouter(policy)

	svc := &Service{
		modelFeedRefreshCancel: refreshCancel,
		modelFeedRefreshDone:   refreshDone,
		Catalog:                catalog,
		Credentials:            credentials,
		Registry:               registry,
		Policy:                 policy,
		Router:                 router,
		Mock:                   WrapLLMClientIfDebug(mock, "mock"),
		Utility:                NewUtilityPlane(),
		Capacity:               NewCapacityGate(catalog.CapacityPolicy()),
		Refusals:               providerretry.NewModelRefusalGate(),
	}
	return svc, nil
}

func startModelFeedRefresh(feed *modelfeed.Feed) (context.CancelFunc, <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = feed.Refresh(ctx)
	}()
	return cancel, done
}

// Close stops the model catalog refresh.
func (s *Service) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.modelFeedRefreshCancel != nil {
		s.modelFeedRefreshCancel()
	}
	if s.Registry != nil && s.Registry.discovery != nil {
		if err := s.Registry.discovery.Close(ctx); err != nil {
			return err
		}
	}
	if s.modelFeedRefreshDone == nil {
		return nil
	}
	select {
	case <-s.modelFeedRefreshDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// BindSummarizer attaches this service's shared utility plane and call lifecycle.
func (s *Service) BindSummarizer(sum *RegistrySummarizer) *RegistrySummarizer {
	if s == nil || sum == nil {
		return sum
	}
	sum.Plane = s.Utility
	sum.Refusals = s.Refusals
	if sum.Registry == nil {
		sum.Registry = s.Registry
	}
	if sum.Policy == nil {
		sum.Policy = s.Policy
	}
	if sum.Lifecycle == nil {
		sum.Lifecycle = serviceCallLifecycle{svc: s}
	}
	return sum
}

type serviceCallLifecycle struct {
	svc *Service
}

func (s serviceCallLifecycle) PublishCall(ctx context.Context, ev api.LLMCallEvent) {
	if s.svc == nil || s.svc.Lifecycle == nil {
		return
	}
	s.svc.Lifecycle.PublishCall(ctx, ev)
}

// ModelPolicyToDTO emits a concrete pool selection.
func ModelPolicyToDTO(p ModelPolicy) api.ModelPolicy {
	models := make([]api.ModelRefDTO, 0, len(p.AgentPool.Models))
	for _, m := range p.AgentPool.Models {
		models = append(models, api.ModelRefDTO{ProviderID: m.ProviderID, Model: m.Model})
	}
	selection := p.AgentPool.Selection
	if selection == "" {
		selection = PoolSelectionRoundRobin
	}
	return api.ModelPolicy{
		ThinkingOverrides: thinkingOverridesToDTO(p.ThinkingOverrides),
		Coordinator:       modelRefToDTO(p.Coordinator),
		Lite:              modelRefToDTO(p.Lite),
		AgentPool: api.AgentPoolDTO{
			Selection: string(selection),
			Models:    models,
		},
	}
}

// modelRefToDTO omits a slot this layer does not assign.
func modelRefToDTO(ref ModelRef) *api.ModelRefDTO {
	if ref.ProviderID == "" {
		return nil
	}
	return &api.ModelRefDTO{ProviderID: ref.ProviderID, Model: ref.Model}
}

// ModelPolicyPatchFromDTO preserves omitted fields as nil assignments.
func ModelPolicyPatchFromDTO(d api.ModelPolicyPatch) ModelPolicyPatch {
	patch := ModelPolicyPatch{}
	if d.ThinkingOverrides != nil {
		overrides := thinkingOverridesFromDTO(*d.ThinkingOverrides)
		patch.ThinkingOverrides = &overrides
	}
	if d.Coordinator != nil {
		patch.Coordinator = &ModelRef{ProviderID: d.Coordinator.ProviderID, Model: d.Coordinator.Model}
	}
	if d.Lite != nil {
		patch.Lite = &ModelRef{ProviderID: d.Lite.ProviderID, Model: d.Lite.Model}
	}
	if d.AgentPool != nil {
		models := make([]ModelRef, 0, len(d.AgentPool.Models))
		for _, model := range d.AgentPool.Models {
			models = append(models, ModelRef{ProviderID: model.ProviderID, Model: model.Model})
		}
		patch.AgentPool = &AgentPool{
			Selection: PoolSelection(d.AgentPool.Selection),
			Models:    models,
		}
	}
	return patch
}

// ValidateModelRef verifies one role assignment.
func (s *Service) ValidateModelRef(ctx context.Context, ref ModelRef, role string) error {
	ref.ProviderID = strings.TrimSpace(ref.ProviderID)
	ref.Model = strings.TrimSpace(ref.Model)
	if ref.ProviderID == "" {
		if ref.Model != "" {
			return fmt.Errorf("model %q has no provider", ref.Model)
		}
		return nil
	}
	if ref.Model == "" {
		return fmt.Errorf("provider %q has no model", ref.ProviderID)
	}
	if s == nil || s.Catalog == nil || s.Registry == nil {
		return fmt.Errorf("AI provider settings are not configured")
	}
	provider, ok := s.Catalog.Get(ref.ProviderID)
	if !ok {
		return fmt.Errorf("unknown provider %q", ref.ProviderID)
	}
	resolved := s.Registry.resolveSelectedModel(ctx, ref.ProviderID, ref.Model)
	if err := ctx.Err(); err != nil {
		return err
	}
	found := false
	for _, model := range resolved.Models {
		if !modelinfo.EquivalentID(provider.Kind, model.ID, ref.Model) {
			continue
		}
		found = true
		eligibility := ModelRoleEligibility(provider.Kind, model, role, s.Registry.RoleExclusions())
		if !eligibility.Selectable {
			return fmt.Errorf("model %q for provider %q cannot serve %s: %s", ref.Model, ref.ProviderID, role, eligibility.Reason)
		}
		break
	}
	if !found {
		return s.unassignableModelError(provider.Kind, ref, resolved)
	}
	return nil
}

// unassignableModelError reports host-declined assignments precisely.
func (s *Service) unassignableModelError(kind string, ref ModelRef, resolved mergeResult) error {
	for _, refused := range resolved.Refused {
		if !modelinfo.EquivalentID(kind, refused.ID, ref.Model) {
			continue
		}
		return fmt.Errorf("provider %q lists model %q but does not serve it", ref.ProviderID, ref.Model)
	}
	if refusal, ok := s.Refusals.Refused(ref.ProviderID, ref.Model); ok {
		return fmt.Errorf("provider %q refused model %q (%s)", ref.ProviderID, ref.Model, refusal.Code)
	}
	if resolved.DiscoveryError != nil {
		return &ModelCatalogUnavailableError{ProviderID: ref.ProviderID, Model: ref.Model, Cause: resolved.DiscoveryError}
	}
	return fmt.Errorf("unknown model %q for provider %q", ref.Model, ref.ProviderID)
}

// ValidatePolicyModels verifies every policy assignment.
func (s *Service) ValidatePolicyModels(ctx context.Context, p ModelPolicy) error {
	check := func(ref ModelRef, role string) error {
		return s.ValidateModelRef(ctx, ref, role)
	}
	if err := check(p.Coordinator, PolicySlotCoordinator); err != nil {
		return err
	}
	if err := check(p.Lite, PolicySlotLite); err != nil {
		return err
	}
	for _, m := range p.AgentPool.Models {
		if err := check(m, PolicySlotAgentPool); err != nil {
			return err
		}
	}
	return nil
}

// ConversationPreparation supplies deterministic setup calls before a harness
// hands the unchanged conversation to its configured provider.
type ConversationPreparation interface {
	Wrap(modelcall.LLMClient) modelcall.LLMClient
}
