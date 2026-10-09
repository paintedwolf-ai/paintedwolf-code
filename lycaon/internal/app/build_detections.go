package app

import (
	"log/slog"
	"net/netip"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settings"
)

type detectionGeneration struct {
	matcher *detectionpack.Matcher
	gate    *detectionpack.GateSource
	egress  *detectionpack.EgressSource
}

// All detection consumers read one published catalog generation.
type detectionRuntime struct {
	current   atomic.Pointer[detectionGeneration]
	semantics *detectionpack.ActionSemanticsCatalog
}

func (r *detectionRuntime) publish(matcher *detectionpack.Matcher) {
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

func (r *detectionRuntime) gateSource() settings.DetectionSource {
	if current := r.current.Load(); current != nil {
		return current.gate
	}
	return nil
}

func (r *detectionRuntime) mintedCredentialSource() session.MintedCredentialSource {
	if current := r.current.Load(); current != nil {
		return current.gate
	}
	return nil
}

func (r *detectionRuntime) egressSource() *detectionpack.EgressSource {
	if current := r.current.Load(); current != nil {
		return current.egress
	}
	return nil
}

// Detection load failures leave approval facts incomplete.
func (b toolWiring) wireDetectionPacks() {
	semantics, semanticsErr := detectionpack.LoadActionSemantics(b.dataDir)
	if semanticsErr != nil {
		slog.Warn("detection action semantics unavailable", "error", semanticsErr)
	}
	if semantics != nil {
		for _, warning := range semantics.Warnings {
			slog.Warn("detection action semantics warning", "warning", warning)
		}
	}
	b.detections.semantics = semantics
	b.toolRuntime.Authority.SetDetectionSource(b.detections.gateSource)
	b.toolRuntime.Authority.SetEgressDetectionSource(egressDetectionAdapter{source: b.detections.egressSource})
	cat, err := detectionpack.LoadCatalog(detectionpack.Input{
		ConfigDir:   b.dataDir,
		Contributed: b.contributedDetectionPacks(),
	})
	if err != nil {
		slog.Warn("detection packs unavailable", "error", err)
		return
	}
	b.detections.publish(detectionpack.NewMatcher(cat))
}

func (b toolWiring) contributedDetectionPacks() []detectionpack.Pack {
	if b.serveBuilder == nil || b.deviceView == nil {
		return nil
	}
	return b.deviceView.DetectionPacks()
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
