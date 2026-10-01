package structrewrite

import "github.com/odvcencio/gotreesitter"

// binding records a captured single metavariable's text and byte span.
type binding struct {
	text      string
	startByte int
	endByte   int
}

// matchEnv accumulates metavariable bindings during a single match attempt.
type matchEnv struct {
	lang   *gotreesitter.Language
	src    []byte
	single map[string]binding
	multi  map[string][]*gotreesitter.Node
}

func newMatchEnv(lang *gotreesitter.Language, src []byte) *matchEnv {
	return &matchEnv{
		lang:   lang,
		src:    src,
		single: map[string]binding{},
		multi:  map[string][]*gotreesitter.Node{},
	}
}

func (e *matchEnv) clone() *matchEnv {
	c := newMatchEnv(e.lang, e.src)
	for k, v := range e.single {
		c.single[k] = v
	}
	for k, v := range e.multi {
		c.multi[k] = v
	}
	return c
}

func (e *matchEnv) adopt(other *matchEnv) {
	e.single = other.single
	e.multi = other.multi
}

// candidate pairs a syntax child with its grammar field.
type candidate struct {
	node  *gotreesitter.Node
	field string
}

// matchNode binds one node; matchChildren handles sequence metavariables.
func matchNode(p *patternNode, c *gotreesitter.Node, env *matchEnv) bool {
	if p.meta != nil {
		if p.meta.ellipsis {
			return false // handled in matchChildren
		}
		if !c.IsNamed() {
			return false // single metavar binds a named node
		}
		if !p.meta.capture {
			return true
		}
		txt := c.Text(env.src)
		if prev, ok := env.single[p.meta.name]; ok {
			return prev.text == txt
		}
		env.single[p.meta.name] = binding{
			text:      txt,
			startByte: int(c.StartByte()),
			endByte:   int(c.EndByte()),
		}
		return true
	}
	if c.Type(env.lang) != p.kind {
		return false
	}
	if p.terminal {
		return c.Text(env.src) == p.text
	}
	return matchChildren(p.children, candidateChildren(c, env.lang), env)
}

// Missing nodes and trivia do not occupy positional matching slots.
func candidateChildren(n *gotreesitter.Node, lang *gotreesitter.Language) []candidate {
	out := make([]candidate, 0, n.ChildCount())
	for i := 0; i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c == nil || c.IsMissing() || c.IsExtra() {
			continue
		}
		out = append(out, candidate{node: c, field: n.FieldNameForChild(i, lang)})
	}
	return out
}

// Ellipsis lookahead clones bindings so failed probes leave the match unchanged.
func matchChildren(ps []*patternNode, cs []candidate, env *matchEnv) bool {
	if len(ps) == 0 {
		return len(cs) == 0
	}
	p := ps[0]
	if p.meta != nil && p.meta.ellipsis {
		if len(ps) == 1 {
			bindMulti(env, p.meta, cs)
			return true
		}
		for k := 0; k <= len(cs); k++ {
			probe := env.clone()
			if matchChildren(ps[1:], cs[k:], probe) {
				bindMulti(probe, p.meta, cs[:k])
				env.adopt(probe)
				return true
			}
		}
		return false
	}
	if len(cs) == 0 {
		return false
	}
	if !sameSlot(p, cs[0]) {
		return false
	}
	if !matchNode(p, cs[0].node, env) {
		return false
	}
	return matchChildren(ps[1:], cs[1:], env)
}

// Grammar fields constrain metavariables whose node kind is unrestricted.
// Unnamed fields add no constraint.
func sameSlot(p *patternNode, c candidate) bool {
	if p.meta == nil || p.field == "" || c.field == "" {
		return true
	}
	return p.field == c.field
}

func bindMulti(env *matchEnv, mv *metaVar, cs []candidate) {
	if !mv.capture {
		return
	}
	nodes := make([]*gotreesitter.Node, 0, len(cs))
	for _, c := range cs {
		nodes = append(nodes, c.node)
	}
	env.multi[mv.name] = nodes
}
