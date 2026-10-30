package httpaction

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// Recipient identity includes routing overrides that can change the service reached.
func requestDestination(spec requestSpec) (id, label string) {
	if spec.socket != "" {
		return secretmatch.UnixDestination(spec.socket, spec.socketBinding.Key())
	}
	id, label = secretmatch.HTTPDestination(spec.target)
	overrides := spec.recipientOverrides()
	if len(overrides) == 0 {
		return id, label
	}
	return secretmatch.DestinationKey(id, "request_overrides", strings.Join(overrides, "\x00")), label + " (" + strings.Join(overrides, "; ") + ")"
}

func (spec requestSpec) recipientOverrides() []string {
	if spec.target == nil {
		return nil
	}
	var facts []string
	if spec.hostHeader != "" {
		override := &url.URL{Scheme: spec.target.Scheme, Host: spec.hostHeader}
		originalID, _ := secretmatch.HTTPDestination(spec.target)
		overrideID, _ := secretmatch.HTTPDestination(override)
		if originalID != overrideID {
			facts = append(facts, "Host: "+strings.ToLower(spec.hostHeader))
		}
	}
	if spec.socket == "" && spec.unixSocket == "" {
		port, err := outboundhttp.EffectivePort(spec.target)
		if err == nil {
			if dial, applies := outboundhttp.MappedDial(spec.resolve, spec.target.Hostname(), port); applies {
				destination := strings.ToLower(dial.Host)
				if dial.Address.IsValid() {
					destination = dial.Address.String()
				}
				facts = append(facts, "dial: "+destination+":"+strconv.Itoa(int(dial.Port)))
			}
		}
	}
	return facts
}
