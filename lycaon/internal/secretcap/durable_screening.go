package secretcap

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/httpcookies"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// durableEvidence supports screening inside write transactions. Values carry
// the ledger's standing so a screen can tell live bytes from retired ones, but
// never a reference: spending one requires scoped evidence.
type durableEvidence struct {
	mu       sync.RWMutex
	versions map[string]durableVersion
}

type durableVersion struct {
	owner  secretIdentity
	values []secretmatch.Remembered
}

// secretIdentity is the capability a stored value belongs to.
type secretIdentity struct {
	id, projectID, name, origin string
}

func identityOf(row db.ManagedSecrets) secretIdentity {
	return secretIdentity{
		id: row.ID, projectID: row.ProjectID, name: row.Name, origin: row.Origin,
	}
}

// valueStanding is what the ledger says about one version's bytes for one audience.
type valueStanding int

const (
	// standingRetired bytes belong to a replaced version or a revoked capability.
	standingRetired valueStanding = iota
	// standingLive bytes are current, but the audience may not spend them.
	standingLive
	// standingReferenced bytes are current, and the audience spends them by reference.
	standingReferenced
)

func standingOf(row db.ManagedSecrets, version db.ManagedSecretVersions, spends bool) valueStanding {
	switch {
	case row.RevokedAt.Valid || version.RetiredAt.Valid:
		return standingRetired
	case spends:
		return standingReferenced
	default:
		return standingLive
	}
}

// versionEvidence screens one stored version. Jar entries are protected one by
// one and are never spent by reference.
func versionEvidence(owner secretIdentity, value string, standing valueStanding) []secretmatch.Remembered {
	switch owner.origin {
	case OriginCookieJar:
		cookies, err := httpcookies.Decode([]byte(value))
		if err != nil {
			return nil
		}
		values := make([]secretmatch.Remembered, 0, len(cookies))
		for _, cookie := range cookies {
			values = append(values, managedScreeningValue("cookie", owner.id, cookie.Value, min(standing, standingLive)))
		}
		return values
	case OriginTokenJar:
		var tokens map[string]Token
		if err := json.Unmarshal([]byte(value), &tokens); err != nil {
			return nil
		}
		values := make([]secretmatch.Remembered, 0, len(tokens))
		for _, token := range tokens {
			values = append(values, managedScreeningValue("token", owner.id, token.Value, min(standing, standingLive)))
		}
		return values
	default:
		return []secretmatch.Remembered{managedScreeningValue(owner.name, owner.id, value, standing)}
	}
}

func managedScreeningValue(name, id, value string, standing valueStanding) secretmatch.Remembered {
	reference := ""
	if standing == standingReferenced {
		reference = secretmatch.ReferenceToken(id)
	}
	return secretmatch.Remembered{
		Secret: value, Name: name, Origin: "managed secret " + id,
		RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
		Source: secretmatch.SourceRememberedMatch, NonDisclosable: true,
		Reference: reference, Retired: standing == standingRetired,
	}
}

// protectDurableVersion publishes a new current version.
func (s *Service) protectDurableVersion(versionID string, owner secretIdentity, value string) {
	version := durableVersion{owner: owner, values: versionEvidence(owner, value, standingLive)}
	s.durable.mu.Lock()
	defer s.durable.mu.Unlock()
	if s.durable.versions == nil {
		s.durable.versions = make(map[string]durableVersion)
	}
	s.durable.versions[versionID] = version
}

func (s *Service) forgetDurableVersion(versionID string) {
	s.durable.mu.Lock()
	defer s.durable.mu.Unlock()
	delete(s.durable.versions, versionID)
}

// retireDurable mirrors a committed retirement onto the versions it covers.
func (s *Service) retireDurable(covers func(versionID string, owner secretIdentity) bool) {
	s.durable.mu.Lock()
	defer s.durable.mu.Unlock()
	for versionID, version := range s.durable.versions {
		if !covers(versionID, version.owner) {
			continue
		}
		values := make([]secretmatch.Remembered, len(version.values))
		for i, value := range version.values {
			value.Retired = true
			values[i] = value
		}
		version.values = values
		s.durable.versions[versionID] = version
	}
}

// Protects reports whether value holds bytes a managed secret of the project
// protects, retired versions included. File marks ask; the editor already
// draws those bytes as tracked.
func (s *Service) Protects(projectID, value string) bool {
	if s == nil || strings.TrimSpace(projectID) == "" || value == "" {
		return false
	}
	for _, held := range s.DurableScreeningValues(projectID) {
		if held.Secret != "" && strings.Contains(value, held.Secret) {
			return true
		}
	}
	return false
}

// DurableScreeningValues returns retained values without acquiring a database lock.
// An empty project selects all device values for unattributed diagnostics.
func (s *Service) DurableScreeningValues(projectID string) []secretmatch.Remembered {
	projectID = strings.TrimSpace(projectID)
	s.durable.mu.RLock()
	defer s.durable.mu.RUnlock()
	var values []secretmatch.Remembered
	for _, version := range s.durable.versions {
		if projectID == "" || projectID == version.owner.projectID {
			values = append(values, version.values...)
		}
	}
	return values
}

// restoreDurableEvidence rebuilds the memory projection before the host accepts
// traffic. Retired values remain evidence even after their chat is gone.
func (s *Service) restoreDurableEvidence(ctx context.Context) error {
	projects, err := s.queries.ListProjects(ctx)
	if err != nil {
		return err
	}
	versions := make(map[string]durableVersion)
	for _, project := range projects {
		rows, err := s.queries.ListProjectManagedSecrets(ctx, project.ID)
		if err != nil {
			return err
		}
		bySecret := make(map[string]db.ManagedSecrets, len(rows))
		for _, row := range rows {
			bySecret[row.ID] = row
		}
		retained, err := s.queries.ListProjectManagedSecretVersions(ctx, project.ID)
		if err != nil {
			return err
		}
		for _, version := range retained {
			row, exists := bySecret[version.SecretID]
			if !exists {
				continue
			}
			if entry, ok := s.values.get(version.ID); ok {
				owner := identityOf(row)
				versions[version.ID] = durableVersion{
					owner: owner, values: versionEvidence(owner, entry.Value, standingOf(row, version, false)),
				}
			}
		}
	}
	s.durable.mu.Lock()
	s.durable.versions = versions
	s.durable.mu.Unlock()
	return nil
}
