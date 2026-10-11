package hitl

import (
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/pkg/api"
)

// ApprovalGrantScope is the lifetime and subject boundary of reusable approval.
type ApprovalGrantScope string

const (
	ApprovalGrantScopeChat    ApprovalGrantScope = "chat"
	ApprovalGrantScopeProject ApprovalGrantScope = "project"
	ApprovalGrantScopeDevice  ApprovalGrantScope = "device"
)

// ApprovalGrantPredicate is a host-derived reuse predicate.
type ApprovalGrantPredicate struct {
	Category string `json:"category"`
	Pattern  string `json:"pattern"`
}

// ApprovalGrantCategoryActionSet is the lease-only category for exact planned calls.
const ApprovalGrantCategoryActionSet = "action_set"

// ApprovalGrantCategoryPackageCoordinate authorizes execution of verified package coordinates.
const ApprovalGrantCategoryPackageCoordinate = "package_coordinate"

// ApprovalGrantCategorySecret covers exact host-keyed secret identities.
const ApprovalGrantCategorySecret = "secret"

// ApprovalGrantCategorySecretRedact authorizes redaction, not disclosure.
const ApprovalGrantCategorySecretRedact = "secret_redact"

// ApprovalGrantCategorySocketCapability marks a synthetic chat offer for AF_UNIX grants.
// Socket authority is recorded by SocketCapabilityRuntime, not sessionGrants.
const ApprovalGrantCategorySocketCapability = "socket_capability"

// ApprovalGrantCategorySocketPath is a durable exact AF_UNIX local-service lease.
const ApprovalGrantCategorySocketPath = "socket_path"

// ApprovalGrantCategoryDirectIP marks a chat lease stored by the capability runtime.
const ApprovalGrantCategoryDirectIP = "direct_ip"

// ApprovalGrantCategoryHost covers outbound network host patterns.
const ApprovalGrantCategoryHost = "host"

// ApprovalGrantCategoryMCP covers MCP tool grants.
const ApprovalGrantCategoryMCP = "mcp"

// Provider trust is stored with the provider configuration.
const ApprovalGrantCategoryTrustDestination = "trust_destination"

// Command-network grants cover the exact command while retaining mediated observation.
const ApprovalGrantCategoryEgressCommand = "egress_command"

// ApprovalGrantCategoryWriteRoot marks chat-scoped write-root authority
// held by the write-root runtime and exposed through the shared grant inventory.
const ApprovalGrantCategoryWriteRoot = "write_root"

// ApprovalGrantCategoryReadPath marks chat-scoped protected-read authority.
const ApprovalGrantCategoryReadPath = "read_path"

// ApprovalGrantCategoryLocalListen marks chat-scoped local listener authority
// held by the local-listen runtime. Egress mode is untouched by this category.
const ApprovalGrantCategoryLocalListen = "local_listen"

// ApprovalGrantCategoryLoopbackConnect marks chat-scoped connections to
// TCP or UDP services on this machine.
const ApprovalGrantCategoryLoopbackConnect = "loopback_connect"

// ApprovalGrantWitness is the attached jail a grant was offered under.
type ApprovalGrantWitness struct {
	FSJailed    bool   `json:"fs_jailed"`
	Egress      string `json:"egress"`
	RootsDigest string `json:"roots_digest"`
}

// ApprovalGrant is reusable authority minted by the host from one pending checkpoint.
type ApprovalGrant struct {
	ElevatedEffects []api.ElevatedAccessEffect `json:"elevated_effects,omitempty"`
	ID              string                     `json:"id"`
	Scope           ApprovalGrantScope         `json:"scope"`
	Predicate       ApprovalGrantPredicate     `json:"predicate"`
	ChatSessionID   string                     `json:"chat_session_id,omitempty"`
	// ProjectID is the identity a project-scoped lease binds to.
	ProjectID  string     `json:"project_id,omitempty"`
	ProjectDir string     `json:"project_dir,omitempty"`
	Title      string     `json:"title"`
	Coverage   string     `json:"coverage"`
	GrantedAt  time.Time  `json:"granted_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	// TTLSeconds is set on a time-boxed lease before apply; ResolveExpiry
	// materializes ExpiresAt as approval time + TTL (capped by scope expiry).
	TTLSeconds     int                  `json:"ttl_seconds,omitempty"`
	ExpiresWhen    string               `json:"expires_when"`
	ReaskWhen      string               `json:"reask_when"`
	Witness        ApprovalGrantWitness `json:"witness"`
	ExactActionSet []string             `json:"exact_action_set,omitempty"`
	// SecretFingerprints contains host-only secret identities.
	SecretFingerprints []string `json:"secret_fingerprints,omitempty"`
	// SecretDestinationID binds configured provider trust.
	SecretDestinationID string `json:"secret_destination_id,omitempty"`
	// SecretRecipients is the complete reviewed set for a secret-use permission.
	SecretRecipients []secretmatch.Recipient `json:"secret_recipients,omitempty"`
	SecretNames      []string                `json:"secret_names,omitempty"`
	// GrantedPath is the filesystem access carried by the lease.
	GrantedPath *GrantedPathDelta `json:"granted_path,omitempty"`
	// Socket fields retain the approved and resolved targets.
	ApprovedPath string `json:"approved_path,omitempty"`
	ResolvedPath string `json:"resolved_path,omitempty"`
	Source       string `json:"source,omitempty"`
	// GrantedByPersonID and GrantedByPolicy name whoever approved the grant:
	// exactly one is set once the grant is installed.
	GrantedByPersonID string                      `json:"granted_by_person_id,omitempty"`
	GrantedByPolicy   *authzledger.PolicyIdentity `json:"granted_by_policy,omitempty"`
	// OwnerOperationID confines rollback to the operation that installed the grant.
	OwnerOperationID string `json:"-"`
}

// Granted reports whether the grant names the person or policy that approved it.
func (g ApprovalGrant) Granted() bool {
	return (strings.TrimSpace(g.GrantedByPersonID) != "") != (g.GrantedByPolicy != nil && g.GrantedByPolicy.Complete())
}

// DayRungTTLSeconds is the one-day rung lifetime.
const DayRungTTLSeconds = 24 * 60 * 60

// TimeBounded includes chat lifetime and explicit expiry, independently of scope.
func (g ApprovalGrant) TimeBounded() bool {
	return g.Scope == ApprovalGrantScopeChat || g.TTLSeconds > 0
}

// ValidateDurableIdentity requires project_id on a project-scoped grant.
func (g ApprovalGrant) ValidateDurableIdentity() error {
	if err := ValidateElevatedEffects(g.ElevatedEffects); err != nil {
		return err
	}
	if g.Scope == ApprovalGrantScopeProject && strings.TrimSpace(g.ProjectID) == "" {
		return fmt.Errorf("project grant requires project_id")
	}
	return nil
}

// ResolveExpiry calculates expiry from approval time and the scope ceiling.
func (g ApprovalGrant) ResolveExpiry(approvedAt time.Time) *time.Time {
	if g.TTLSeconds <= 0 {
		return g.ExpiresAt
	}
	ttl := approvedAt.UTC().Add(time.Duration(g.TTLSeconds) * time.Second)
	if g.ExpiresAt != nil && g.ExpiresAt.Before(ttl) {
		return g.ExpiresAt
	}
	return &ttl
}

// DayRung derives the one-day option from a base offer.
func DayRung(base ApprovalGrantOffer) ApprovalGrantOffer {
	offer := base
	offer.Rung = ApprovalRungDay
	offer.TTLSeconds = DayRungTTLSeconds
	offer.Grant.TTLSeconds = DayRungTTLSeconds
	offer.Title = TitleAllowFor1Day
	offer.Grant.Title = offer.Title
	if base.Scope == ApprovalGrantScopeChat {
		offer.ExpiresWhen = ExpiresIn1DayOrChatDeleted
	} else {
		offer.ExpiresWhen = ExpiresIn1DayOrRevoked
	}
	offer.Grant.ExpiresWhen = offer.ExpiresWhen
	offer.ID = base.ID + "-1d"
	offer.Grant.ID = offer.ID
	return offer
}

// ApprovalGrantOffer is the bounded choice attached to a checkpoint. Grant contains
// host-only authority; the wire exposes only the opaque offer id and reviewed copy.
type ApprovalGrantOffer struct {
	ID             string             `json:"id"`
	Rung           ApprovalOptionRung `json:"rung"`
	Scope          ApprovalGrantScope `json:"scope"`
	Group          string             `json:"group,omitempty"`
	DirectoryScope string             `json:"directory_scope,omitempty"`
	Title          string             `json:"title"`
	Coverage       string             `json:"coverage"`
	ExpiresWhen    string             `json:"expires_when"`
	ReaskWhen      string             `json:"reask_when"`
	// Subject is the reuse shape installed by this offer.
	Subject gate.ReuseShape `json:"-"`
	// TTLSeconds starts at approval and respects the scope expiry.
	TTLSeconds int           `json:"ttl_seconds,omitempty"`
	Grant      ApprovalGrant `json:"grant"`
	// Authority lists runtime-specific deltas installed with the lease.
	Authority []ApprovalAuthorityDelta `json:"authority,omitempty"`
	// Disabled keeps the offer in its ladder slot without making it selectable;
	// Note says why, in host copy.
	Disabled bool   `json:"disabled,omitempty"`
	Note     string `json:"note,omitempty"`
}

// DisabledOffer marks an offer as present-but-unavailable with its reason.
func DisabledOffer(offer ApprovalGrantOffer, note string) ApprovalGrantOffer {
	offer.Disabled = true
	offer.Note = note
	return offer
}
