package sweeps

import (
	wire "github.com/lycaon/lycaon/pkg/api"
	openapi "github.com/lycaon/lycaon/test/openapi"
)

// checkEventPayloads: every published payload validates against
// docs/schemas/events/<topic>.json.
func (s *conformanceSweep) checkEventPayloads(published []wire.EventEnvelope) {
	if s.eventSchemas == nil {
		s.findings.add(ruleEventPayloadsMatchSchemas, "sweep", "event schemas", "no event schemas were loaded")
		return
	}
	for _, env := range published {
		if err := s.eventSchemas.Validate(env.Topic, env.Data); err != nil {
			s.findings.add(ruleEventPayloadsMatchSchemas, string(env.Topic), "payload", "%s", openapi.SchemaErrorLine(err))
		}
	}
}
