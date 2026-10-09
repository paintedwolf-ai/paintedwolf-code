package security

import (
	"log/slog"
	"net/netip"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/toolhost"
)

type detectionGeneration struct {
	matcher *detectionpack.Matcher
	gate    *detectionpack.GateSource
	egress  *detectionpack.EgressSource
}

// All detection consumers read one published catalog generation.
type Detections struct {
	current   atomic.Pointer[detectionGeneration]
	semantics *detectionpack.ActionSemanticsCatalog
}

func (r *Detections) Publish(matcher *detectionpack.Matcher) {
	if matcher == nil {
		r.current.Store(nil)
		return
	}
	r.current.Store(&detectionGeneration{
		matcher: matcher,
		gate:    detectionpack.NewGateSource(matcher, r.semantics),
		egress:  detectionpack.NewEgressSource(matcher),
	})
}

func (r *Detections) GateSource() settings.DetectionSource {
	if current := r.current.Load(); current != nil {
		return current.gate
	}
	return nil
}

func (r *Detections) MintedCredentialSource() session.MintedCredentialSource {
	if current := r.current.Load(); current != nil {
		return current.gate
	}
	return nil
}

func (r *Detections) EgressSource() *detectionpack.EgressSource {
	if current := r.current.Load(); current != nil {
		return current.egress
	}
	return nil
}

// Detection load failures leave approval facts incomplete.
func (b *Detections) Load(configDir string, contributed []detectionpack.Pack, authority *toolhost.AuthorityServices) {
	semantics, semanticsErr := detectionpack.LoadActionSemantics(configDir)
	if semanticsErr != nil {
		slog.Warn("detection action semantics unavailable", "error", semanticsErr)
	}
	if semantics != nil {
		for _, warning := range semantics.Warnings {
			slog.Warn("detection action semantics warning", "warning", warning)
		}
	}
	b.semantics = semantics
	authority.SetDetectionSource(b.GateSource)
	authority.SetEgressDetectionSource(egressDetectionAdapter{source: b.EgressSource})
	cat, err := detectionpack.LoadCatalog(detectionpack.Input{
		ConfigDir:   configDir,
		Contributed: contributed,
	})
	if err != nil {
		slog.Warn("detection packs unavailable", "error", err)
		return
	}
	b.Publish(detectionpack.NewMatcher(cat))
}

// egressDetectionAdapter avoids a package cycle.
type egressDetectionAdapter struct {
	source func() *detectionpack.EgressSource
}

func (a egressDetectionAdapter) Match(observation confine.EgressDetectionObservation) (confine.EgressDetectionCitation, bool) {
	source := a.source()
	if source == nil {
		return confine.EgressDetectionCitation{}, false
	}
	destinationIP := ""
	if addr, err := netip.ParseAddr(observation.Endpoint.Host); err == nil {
		destinationIP = addr.String()
	}
	m, ok := source.Match(detectionpack.EgressObservation{
		DestinationHostname: observation.Endpoint.Host,
		DestinationPort:     observation.Endpoint.Port,
		DestinationIP:       destinationIP,
		Transport:           string(observation.Endpoint.Transport),
		SessionID:           observation.SessionID,
		ActionID:            observation.ActionID,
		Origin:              observation.Origin,
		DecisionStage:       observation.DecisionStage,
		RequestMethod:       observation.Endpoint.RequestMethod,
		RequestPath:         observation.Endpoint.RequestPath,
	}, observation.Image, observation.CommandLine)
	if !ok {
		return confine.EgressDetectionCitation{}, false
	}
	return confine.EgressDetectionCitation{
		PackID: m.PackID, RuleID: m.RuleID, RuleTitle: m.RuleTitle, Level: m.Level,
		External: m.External, Local: m.Local, Unrecoverable: m.Unrecoverable, Tagged: m.Tagged,
		CorrelationID: m.CorrelationID,
	}, true
}

func (a egressDetectionAdapter) Escalates(match confine.EgressDetectionCitation, posture gate.Posture) bool {
	return detectionpack.Escalates(detectionpack.Level(match.Level), posture)
}
