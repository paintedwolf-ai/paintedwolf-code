package secretmatch

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/egress"
)

// Recipient names a reviewed handoff. It does not identify onward recipients.
type Recipient struct {
	ID      string          `json:"id" yaml:"id"`
	Surface ScreenSurface   `json:"surface" yaml:"surface"`
	Label   string          `json:"label" yaml:"label"`
	Kind    DestinationKind `json:"kind" yaml:"kind"`
}

// MaxRecipients bounds one permission's receiver set.
const MaxRecipients = 16

// CanonicalRecipients validates and orders a permission's complete receiver set.
func CanonicalRecipients(in []Recipient) ([]Recipient, error) {
	if len(in) == 0 || len(in) > MaxRecipients {
		return nil, fmt.Errorf("a secret permission requires 1 to %d recipients", MaxRecipients)
	}
	out := make([]Recipient, 0, len(in))
	seen := map[string]Recipient{}
	for _, recipient := range in {
		if strings.TrimSpace(recipient.ID) == "" || strings.TrimSpace(recipient.ID) != recipient.ID ||
			strings.TrimSpace(recipient.Label) == "" || len(recipient.Label) > 1024 {
			return nil, fmt.Errorf("secret recipient requires a canonical identity and label")
		}
		fact, ok := surfaceFacts[recipient.Surface]
		if !ok || fact.destination != recipient.Kind {
			return nil, fmt.Errorf("secret recipient surface and kind disagree")
		}
		key := string(recipient.Surface) + "\x00" + recipient.ID
		if previous, found := seen[key]; found {
			if previous != recipient {
				return nil, fmt.Errorf("secret recipient has conflicting labels")
			}
			continue
		}
		seen[key] = recipient
		out = append(out, recipient)
	}
	sort.Slice(out, func(i, j int) bool {
		return string(out[i].Surface)+"\x00"+out[i].ID < string(out[j].Surface)+"\x00"+out[j].ID
	})
	return out, nil
}

// LocalRecipient is the canonical local recipient; through RecipientCovered it
// covers command, terminal, and loopback HTTP destinations.
var LocalRecipient = Recipient{
	ID:      "local",
	Surface: SurfaceCommand,
	Label:   "local execution",
	Kind:    DestinationProcess,
}

// IsLocal reports whether this recipient is a local process. An HTTP recipient
// is never local, so a loopback origin grant stays bound to its own origin.
func (r Recipient) IsLocal() bool {
	if r.ID == LocalRecipient.ID {
		return true
	}
	if r.Kind == DestinationProcess && (r.Surface == SurfaceCommand || r.Surface == SurfaceTerminal) {
		return true
	}
	return false
}

// IsLocalDestination reports whether destinationID and surface represent local execution or loopback HTTP.
func IsLocalDestination(destinationID, surface string) bool {
	if destinationID == LocalRecipient.ID {
		return true
	}
	surface = strings.TrimSpace(surface)
	if surface == string(SurfaceCommand) || surface == string(SurfaceTerminal) {
		return true
	}
	if surface == string(SurfaceHTTPRequest) || surface == string(SurfaceFetchURL) {
		return isDestinationLoopbackLiteral(destinationID)
	}
	return false
}

func isDestinationLoopbackLiteral(raw string) bool {
	raw = strings.TrimSpace(raw)
	// Strip the transport digest DestinationKey appends.
	if at := strings.Index(raw, "@"); at != -1 {
		raw = raw[:at]
	}
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		raw = u.Hostname()
	} else if h, _, err := net.SplitHostPort(raw); err == nil {
		raw = h
	}
	return egress.SyntacticLoopback(raw)
}

// RecipientCovered reports whether recipients include the destination; any
// local recipient covers every local destination.
func RecipientCovered(recipients []Recipient, destinationID, surface string) bool {
	isLocalDest := IsLocalDestination(destinationID, surface)
	for _, recipient := range recipients {
		if recipient.ID == destinationID && string(recipient.Surface) == surface {
			return true
		}
		if isLocalDest && recipient.IsLocal() {
			return true
		}
	}
	return false
}

// RecipientDigest binds identity to the complete set, independent of display copy.
func RecipientDigest(recipients []Recipient) string {
	facts := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		facts = append(facts, string(recipient.Surface)+"\x00"+recipient.ID)
	}
	sort.Strings(facts)
	return DestinationKey("secret-recipients", facts...)
}
