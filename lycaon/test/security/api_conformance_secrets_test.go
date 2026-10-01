package security

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// conformanceSentinel is one secret value the sweep configured, with every
// encoding that would reveal it.
type conformanceSentinel struct {
	origin string
	forms  []string
}

// conformanceSecrets issues sentinels and scans for them; safe for concurrent
// use.
type conformanceSecrets struct {
	mu     sync.Mutex
	issued []conformanceSentinel
	seeded []string
}

// issue mints a sentinel for origin (`operationId.property`).
func (c *conformanceSecrets) issue(origin string) string {
	raw := make([]byte, 12)
	_, _ = rand.Read(raw)
	value := "pwsentinel" + hex.EncodeToString(raw)
	forms := []string{value, hex.EncodeToString([]byte(value))}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
		// An embedded value encodes one of three ways by its offset; the
		// middle of each is independent of the bytes around it.
		for shift := range 3 {
			encoded := enc.EncodeToString(append(make([]byte, shift), value...))
			forms = append(forms, encoded[4:len(encoded)-4])
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.issued = append(c.issued, conformanceSentinel{origin: origin, forms: forms})
	return value
}

// leaks names every sentinel data reveals, in any form.
func (c *conformanceSecrets) leaks(data []byte) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, sentinel := range c.issued {
		for _, form := range sentinel.forms {
			if bytes.Contains(data, []byte(form)) {
				out = append(out, sentinel.origin)
				break
			}
		}
	}
	return out
}

// scanExchange: no response body or header carries a configured secret.
func (c *conformanceSecrets) scanExchange(findings *conformanceFindings, ex conformanceExchange) {
	for _, origin := range c.leaks(ex.body) {
		findings.add(ruleNoSecretMaterial, ex.req.op.ID, ex.req.probe, "response body reveals the secret set through %s", origin)
	}
	for name, values := range ex.header {
		for _, origin := range c.leaks([]byte(strings.Join(values, "\n"))) {
			findings.add(ruleNoSecretMaterial, ex.req.op.ID, ex.req.probe, "header %s reveals the secret set through %s", name, origin)
		}
	}
}

// scanEvents: no event payload, which reaches clients over the event stream,
// carries a configured secret.
func (c *conformanceSecrets) scanEvents(findings *conformanceFindings, events []wire.EventEnvelope) {
	for _, env := range events {
		for _, origin := range c.leaks(env.Data) {
			findings.add(ruleNoSecretMaterial, string(env.Topic), "event", "payload reveals the secret set through %s", origin)
		}
	}
}

// seedSecrets places sentinels in writeOnly fields and records coverage gaps.
func (s *conformanceSweep) seedSecrets(ctx context.Context) {
	for _, op := range s.ops {
		if op.JSONBody == nil || !declaresWriteOnly(op.JSONBody, map[*openapi3.Schema]bool{}) {
			continue
		}
		params, _ := s.values.pathParams(op)
		body := s.values.body(op.JSONBody, func(name string) string { return s.secrets.issue(op.ID + "." + name) })
		if op.ID == "createMcpProvider" {
			// Exercise custom provider credentials without starting a transport.
			fields, ok := body.(map[string]any)
			if !ok {
				s.findings.add(ruleNoSecretMaterial, op.ID, "seed secret", "provider creation schema did not produce an object")
				continue
			}
			fields["source"] = "custom"
			fields["id"] = "conformance-" + uuid.NewString()
			fields["url"] = "https://example.invalid/mcp"
			fields["enabled"] = false
		}
		ex := s.send(ctx, jsonRequest(op, "seed secret", params, s.values.requiredQuery(op), body))
		if ex.status/100 != 2 {
			s.findings.note("%s: could not seed a secret: answered %s", op.ID, ex.outcome())
			continue
		}
		s.secrets.mu.Lock()
		s.secrets.seeded = append(s.secrets.seeded, op.ID)
		s.secrets.mu.Unlock()
		s.findings.note("%s: seeded a secret", op.ID)
	}
	if len(s.secrets.seeded) == 0 {
		s.findings.add(ruleNoSecretMaterial, "sweep", "seed secret", "no operation accepted a secret, so nothing was scanned for")
	}
}

func declaresWriteOnly(schema *openapi3.Schema, seen map[*openapi3.Schema]bool) bool {
	if schema == nil || seen[schema] {
		return false
	}
	seen[schema] = true
	if schema.WriteOnly {
		return true
	}
	for _, ref := range schema.Properties {
		if declaresWriteOnly(refValue(ref), seen) {
			return true
		}
	}
	for _, refs := range []openapi3.SchemaRefs{schema.AllOf, schema.OneOf, schema.AnyOf} {
		for _, ref := range refs {
			if declaresWriteOnly(refValue(ref), seen) {
				return true
			}
		}
	}
	return declaresWriteOnly(refValue(schema.Items), seen)
}
