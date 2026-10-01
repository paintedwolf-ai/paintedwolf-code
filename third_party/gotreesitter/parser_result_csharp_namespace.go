package gotreesitter

// csharpBuildRecoveredNamespaceDeclarationFromErrorRoot synthesises a
// namespace_declaration from the ERROR root of a namespace sub-parse whose body
// could not be parsed cleanly. The keyword, name and braces are read directly
// from source (mirroring csharpRecoverNamespaceFromChildren), and the body
// declarations are recovered leniently from the already-parsed children of
// errRoot — so no additional reparse is performed. This is the partial-recovery
// fallback for issue #115, where large real-world files collapse to a single
// ERROR node and the strict whole-namespace extraction finds no clean
// namespace_declaration.
func csharpBuildRecoveredNamespaceDeclarationFromErrorRoot(errRoot *Node, source []byte, start, end uint32, p *Parser, lang *Language, arena *nodeArena) (*Node, bool) {
	if errRoot == nil || lang == nil || arena == nil || start >= end || int(end) > len(source) {
		return nil, false
	}
	nsKwStart := csharpSkipSpaceBytes(source, start)
	if !csharpHasKeywordAt(source, nsKwStart, "namespace") {
		return nil, false
	}
	nsKwEnd := nsKwStart + uint32(len("namespace"))
	nameStart := csharpSkipSpaceBytes(source, nsKwEnd)
	openBrace := csharpFindTopLevelByte(source, nameStart, end, '{')
	if openBrace >= end {
		return nil, false
	}
	nameEnd := csharpTrimRightSpaceBytes(source, openBrace)
	if nameStart >= nameEnd {
		return nil, false
	}
	closeBrace := csharpFindMatchingBraceByte(source, int(openBrace), int(end))
	if closeBrace < 0 || uint32(closeBrace+1) != end {
		return nil, false
	}

	keywordTok, ok := csharpBuildLeafNodeByName(arena, source, lang, "namespace", nsKwStart, nsKwEnd)
	if !ok {
		return nil, false
	}
	nameNode, ok := csharpNamespaceNameNodeFromErrorRoot(errRoot, source, nameStart, openBrace, lang, arena)
	if !ok {
		return nil, false
	}
	members, ok := csharpRecoverNamespaceBodyMembersFromErrorRoot(errRoot, source, openBrace, uint32(closeBrace), p, lang, arena)
	// SHORT-TERM RELIEF for issue #136: the children-based recovery above only
	// sees whatever the namespace sub-parse (offsetRoot) itself managed to
	// produce before it died, which for large real-world files can be a small
	// prefix of the body (or nothing at all — a pure GLR dead end leaves no
	// children to recover from downstream of that point). Try two purely
	// source-based, bounded reconstructions of the whole body too. On large
	// namespace bodies, try the Alt driver first and skip the primary driver only
	// when Alt clearly beats the children-based recovery; this avoids a second
	// whole-body member-reparse pass on Bicep-sized files while preserving the
	// primary driver's extra coverage for smaller or marginal cases.
	var sourceMembers []*Node
	sourceOK := false
	altMembers, altOK := csharpRecoverNamespaceBodyMembersFromSourceAlt(source, openBrace, uint32(closeBrace), p, arena)
	if !csharpLargeNamespaceAltRecoveryCanSkipPrimary(end-start, members, ok, altMembers, altOK, lang) {
		sourceMembers, sourceOK = csharpRecoverNamespaceBodyMembersFromSource(source, openBrace, uint32(closeBrace), p, lang, arena)
	}
	if best, bestOK := csharpChooseRecoveredNamespaceMembers(lang,
		csharpRecoveredMembersCandidate{members: members, ok: ok},
		csharpRecoveredMembersCandidate{members: sourceMembers, ok: sourceOK},
		csharpRecoveredMembersCandidate{members: altMembers, ok: altOK},
	); bestOK {
		members, ok = best, true
	}
	if !ok || len(members) == 0 {
		return nil, false
	}
	declList, ok := csharpBuildSourceDeclarationListNode(source, openBrace, uint32(closeBrace), members, lang, arena)
	if !ok {
		return nil, false
	}

	declSym, ok := symbolByName(lang, "namespace_declaration")
	if !ok {
		return nil, false
	}
	nameFieldID, _ := lang.FieldByName("name")
	bodyFieldID, _ := lang.FieldByName("body")
	children := arena.allocNodeSlice(3)
	children[0] = keywordTok
	children[1] = nameNode
	children[2] = declList
	fieldIDs := cloneFieldIDSliceInArena(arena, []FieldID{0, nameFieldID, bodyFieldID})
	decl := newParentNodeInArena(arena, declSym, symbolIsNamed(lang, declSym), children, fieldIDs, 0)
	decl.setHasError(false)
	extendNodeEndTo(decl, end, source)
	return decl, true
}

// csharpNamespaceNameNodeFromErrorRoot returns the namespace name node, reusing
// the already-parsed qualified_name/identifier from the sub-parse children when
// available (it is offset-adjusted to the original source) and falling back to
// rebuilding it from source.
func csharpNamespaceNameNodeFromErrorRoot(errRoot *Node, source []byte, nameStart, openBrace uint32, lang *Language, arena *nodeArena) (*Node, bool) {
	for _, c := range errRoot.children {
		if c == nil || c.HasError() {
			continue
		}
		switch c.Type(lang) {
		case "qualified_name", "identifier":
			if c.startByte >= nameStart && c.endByte <= openBrace {
				return cloneTreeNodesIntoArena(c, arena), true
			}
		}
	}
	nameEnd := csharpTrimRightSpaceBytes(source, openBrace)
	if nameStart >= nameEnd {
		return nil, false
	}
	return csharpBuildQualifiedNameNode(source, nameStart, nameEnd, lang, arena)
}

// csharpRecoverNamespaceBodyMembersFromErrorRoot recovers the declarations that
// live inside a namespace body from the (offset-adjusted) children of the
// namespace sub-parse ERROR root. It performs a single O(children) pass: each
// step either consumes a recovered type declaration (advancing past all of its
// children) or skips an unrecoverable fragment. Type bodies are recovered in
// lenient mode so internal ERROR fragments do not abort the enclosing type.
func csharpRecoverNamespaceBodyMembersFromErrorRoot(errRoot *Node, source []byte, openBrace, closeBrace uint32, p *Parser, lang *Language, arena *nodeArena) ([]*Node, bool) {
	if errRoot == nil || lang == nil || arena == nil || openBrace >= closeBrace {
		return nil, false
	}
	children := errRoot.children
	members := make([]*Node, 0, len(children))
	for i := 0; i < len(children); {
		child := children[i]
		if child == nil || child.endByte <= openBrace+1 || child.startByte >= closeBrace {
			i++
			continue
		}
		if recovered, next, ok := csharpRecoverNonEmptyTopLevelTypeDeclarationFromChildren(children, i, source, p, lang, arena, true); ok {
			members = append(members, recovered)
			i = next
			continue
		}
		if member, ok := csharpRecoverTypeDeclarationBodyChild(child, lang, arena); ok {
			members = append(members, member)
			i++
			continue
		}
		// Unrecoverable fragment (e.g. an ERROR span or namespace header token):
		// skip it so the declarations that did parse still surface (issue #115).
		i++
	}
	return members, len(members) > 0
}

// csharpRecoverNamespaceBodyMembersFromSource is a purely source-based,
// bounded fallback for issue #136: it recovers the namespace body's top-level
// members (type declarations, and — via
// csharpRecoverSourceTopLevelTypeDeclarationFromRange's lenient member
// recovery — the methods/fields/properties inside them) directly from
// source, independent of what the namespace sub-parse itself produced. This
// exists because for large real-world files the underlying GLR sub-parse can
// die partway through the body (a genuine dead end, not just an isolated
// ERROR span), leaving csharpRecoverNamespaceBodyMembersFromErrorRoot nothing
// to walk downstream of that point.
//
// It reuses the same chunk machinery as the whole-file top-level recovery
// (csharpRecoverTopLevelChunks / csharpRecoverTopLevelChunkNodesFromRange)
// but is NOT gated by the enclosing file's size: cost is bounded instead by
// the number of top-level members in the body (csharpMaxTypeBodyRecoveryMembers)
// and, per LEAF member, the size of that individual member
// (csharpMaxTopLevelChunkRecoverySourceBytes, enforced in
// csharpRecoverSourceTypeMembersFromRange's lenient mode and in
// csharpRecoverTopLevelChunkNodesFromRange's own unrecognized-chunk bound) —
// so worst-case reparse cost stays predictable regardless of how large the
// file is. Note a single top-level chunk at THIS level can legitimately be an
// entire class body (tens of KB) when it is recognized as a class/record
// declaration — that is fine, because the recursive per-member bound applies
// inside it.
//
// Unlike csharpRecoverTopLevelChunks (which aborts entirely if any single
// top-level span fails to recover), this is lenient: an unrecoverable
// top-level member (e.g. one straddling an unbalanced #if/#endif — see #136)
// is skipped so the rest still surface.
//
// Error-masking byproduct (inherited from the #115/#116 children-based
// recovery this sits alongside, not new to this path): every node this
// function returns is synthesized with setHasError(false), and its caller
// (csharpBuildRecoveredNamespaceDeclarationFromErrorRoot) retags the
// enclosing namespace/root the same way. That means a file that is
// genuinely, meaningfully malformed (not just a GLR-engine limitation on
// otherwise-valid C#) can come out of Parse() with HasError()==false once
// enough of it round-trips through this reconstruction — a downstream
// consumer that gates purely on HasError() to decide "is this a real parse
// failure" will not see the difference between "recovered a truly valid
// file" and "recovered as much of a broken file as this heuristic could
// find." Treat HasError()==false on a recovery-heavy C# parse as "best
// effort," not a correctness guarantee.
//
// THIS IS SHORT-TERM RELIEF, not a permanent fixture: remove once the GLR
// engine gains real mid-parse error recovery (the underlying gap tracked by
// #136) and these files parse (mostly) cleanly without needing source
// reconstruction.
func csharpRecoverNamespaceBodyMembersFromSource(source []byte, openBrace, closeBrace uint32, p *Parser, lang *Language, arena *nodeArena) ([]*Node, bool) {
	if p == nil || lang == nil || arena == nil || openBrace >= closeBrace || int(closeBrace) > len(source) {
		return nil, false
	}
	bodyStart, bodyEnd := openBrace+1, closeBrace
	if bodyStart >= bodyEnd {
		return nil, false
	}
	if bytesAreTrivia(source[bodyStart:bodyEnd]) {
		return nil, false
	}
	relSpans := csharpTopLevelChunkSpans(source[bodyStart:bodyEnd])
	if len(relSpans) == 0 || len(relSpans) > csharpMaxTypeBodyRecoveryMembers {
		return nil, false
	}
	out := make([]*Node, 0, len(relSpans))
	for _, rel := range relSpans {
		spanStart := bodyStart + rel[0]
		spanEnd := bodyStart + rel[1]
		for _, part := range csharpSplitLeadingTopLevelCommentSpans(source, spanStart, spanEnd) {
			// NOTE: no blanket size cap on the top-level part here — a
			// single namespace-level "member" is often an entire
			// class/record declaration, which
			// csharpRecoverTopLevelChunkNodesFromRange (lenient=true)
			// already bounds internally, per actual leaf member, via
			// csharpRecoverSourceTypeMembersFromRange /
			// csharpMaxTypeBodyRecoveryMembers, and separately bounds its
			// own unrecognized-chunk fallback.
			nodes, ok := csharpRecoverTopLevelChunkNodesFromRange(source, part[0], part[1], p, arena, true)
			if !ok || len(nodes) == 0 {
				// Lenient: skip an unrecoverable member, keep the rest.
				continue
			}
			out = append(out, nodes...)
		}
	}
	return out, len(out) > 0
}

// csharpRecoveredDeclarationNodeTypes are the node types
// csharpCountRecoveredDeclarationNodes counts — the same "useful, recovered"
// set csharpRecoverTypeDeclarationBodyChild accepts as a type-body member,
// plus the top-level type declarations themselves. Comments are
// intentionally excluded so a pile of recovered comments cannot outweigh
// actual declarations in the comparison.
var csharpRecoveredDeclarationNodeTypes = map[string]bool{
	"class_declaration":                 true,
	"struct_declaration":                true,
	"record_declaration":                true,
	"interface_declaration":             true,
	"enum_declaration":                  true,
	"delegate_declaration":              true,
	"namespace_declaration":             true,
	"file_scoped_namespace_declaration": true,
	"constructor_declaration":           true,
	"destructor_declaration":            true,
	"field_declaration":                 true,
	"method_declaration":                true,
	"property_declaration":              true,
	"event_declaration":                 true,
	"indexer_declaration":               true,
	"operator_declaration":              true,
	"conversion_operator_declaration":   true,
}

// csharpRecoveredDeclarationStats is the per-candidate tally
// csharpPreferRecoveredDeclarations compares on: total declaration-shaped
// nodes (csharpRecoveredDeclarationNodeTypes) and, separately,
// method_declaration nodes specifically. Kept as two counts (not one
// weighted scalar) so the comparison can explicitly prioritize methods —
// see csharpPreferRecoveredDeclarations.
type csharpRecoveredDeclarationStats struct {
	total   int
	methods int
}

const (
	csharpLargeNamespaceRecoveryBodyBytes     = 64 * 1024
	csharpLargeNamespaceAltMethodWinThreshold = 8
	csharpLargeNamespaceAltTotalWinThreshold  = 16
)

func csharpLargeNamespaceAltRecoveryCanSkipPrimary(spanBytes uint32, childMembers []*Node, childOK bool, altMembers []*Node, altOK bool, lang *Language) bool {
	if spanBytes < csharpLargeNamespaceRecoveryBodyBytes || lang == nil || !altOK || len(altMembers) == 0 {
		return false
	}
	childStats := csharpRecoveredDeclarationStats{}
	if childOK && len(childMembers) > 0 {
		childStats = csharpCountRecoveredDeclarationNodes(childMembers, lang)
	}
	altStats := csharpCountRecoveredDeclarationNodes(altMembers, lang)
	return csharpLargeNamespaceAltStatsCanSkipPrimary(spanBytes, childStats, altStats)
}

func csharpLargeNamespaceAltStatsCanSkipPrimary(spanBytes uint32, childStats, altStats csharpRecoveredDeclarationStats) bool {
	if spanBytes < csharpLargeNamespaceRecoveryBodyBytes {
		return false
	}
	if !csharpPreferRecoveredDeclarations(altStats, childStats) {
		return false
	}
	return altStats.methods >= childStats.methods+csharpLargeNamespaceAltMethodWinThreshold ||
		altStats.total >= childStats.total+csharpLargeNamespaceAltTotalWinThreshold
}

// csharpCountRecoveredDeclarationNodes recursively tallies declaration-shaped
// nodes (and, separately, method_declaration nodes) across a recovered
// top-level member slice in a single pass. Used by
// csharpBuildRecoveredNamespaceDeclarationFromErrorRoot (issue #136
// short-term relief) to compare the children-based and source-based
// namespace-body recovery strategies by actual recovered content instead of
// top-level slice length, since a correctly-nested class_declaration with
// dozens of methods inside it is a single top-level element.
func csharpCountRecoveredDeclarationNodes(nodes []*Node, lang *Language) csharpRecoveredDeclarationStats {
	var stats csharpRecoveredDeclarationStats
	var walk func(n *Node)
	walk = func(n *Node) {
		if n == nil {
			return
		}
		t := n.Type(lang)
		if csharpRecoveredDeclarationNodeTypes[t] {
			stats.total++
		}
		if t == "method_declaration" {
			stats.methods++
		}
		for _, child := range n.children {
			walk(child)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	return stats
}

// csharpPreferRecoveredDeclarations reports whether candidate a should be
// preferred over candidate b when choosing between the children-based
// (#115/#116) and source-based (#136) namespace-body recovery strategies.
// Prioritizes method_declaration count first — methods are the concrete
// goal issue #136 tracks (symbol/complexity extraction), so a candidate
// with fewer total declarations but strictly more methods (e.g. a
// source-reconstructed class with all its real methods but a couple of
// unrecovered nested types) must still win over a candidate with more
// total declarations but fewer methods. Total declaration count is only
// the tiebreak when method counts are equal, matching the reviewer's
// "weight or tiebreak toward method_declaration count" guidance on issue
// #136 (regression: XmlNodeConverter.cs recovered 25 methods via a
// higher-total-declaration-count source candidate instead of the
// children-based candidate's 27).
func csharpPreferRecoveredDeclarations(a, b csharpRecoveredDeclarationStats) bool {
	if a.methods != b.methods {
		return a.methods > b.methods
	}
	return a.total > b.total
}

// csharpRecoveredMembersCandidate pairs a candidate namespace-body member
// recovery with whether it succeeded, for csharpChooseRecoveredNamespaceMembers.
type csharpRecoveredMembersCandidate struct {
	members []*Node
	ok      bool
}

// csharpChooseRecoveredNamespaceMembers picks the best of several candidate
// namespace-body member recoveries — the children-based (#115/#116) pass and
// one or more independently-bounded source-based (#136) passes, such as
// csharpRecoverNamespaceBodyMembersFromSource and its Alt driver
// csharpRecoverNamespaceBodyMembersFromSourceAlt (upstream #138) — using
// csharpPreferRecoveredDeclarations (method_declaration count first, then
// total declaration count) as the ranking. Candidates with ok=false or no
// members are skipped. Returns ok=false if no candidate succeeded.
func csharpChooseRecoveredNamespaceMembers(lang *Language, candidates ...csharpRecoveredMembersCandidate) ([]*Node, bool) {
	var best []*Node
	var bestStats csharpRecoveredDeclarationStats
	found := false
	for _, c := range candidates {
		if !c.ok || len(c.members) == 0 {
			continue
		}
		stats := csharpCountRecoveredDeclarationNodes(c.members, lang)
		if !found || csharpPreferRecoveredDeclarations(stats, bestStats) {
			best, bestStats, found = c.members, stats, true
		}
	}
	return best, found
}
