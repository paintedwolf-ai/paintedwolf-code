package gotreesitter

import "bytes"

// These scoped-lambda repairs remain conservative fallbacks for legacy,
// generated, and overridden C# language artifacts without native capability
// certification.

func normalizeCSharpRecoveredScopedLambdaBlocks(root *Node, source []byte, p *Parser) {
	if root == nil || p == nil || p.language == nil || p.language.Name != "c_sharp" || len(source) == 0 || len(source) > csharpMaxTopLevelChunkRecoverySourceBytes {
		return
	}
	walkResultTree(root, func(n *Node) {
		if n.Type(p.language) == "block" && csharpBlockNeedsScopedLambdaRecovery(n, source, p.language) {
			if recovered, ok := csharpRecoverScopedLambdaBlock(n, source, p); ok {
				csharpReplaceNodeContents(n, recovered)
			}
		}
	})
}

func csharpBlockNeedsScopedLambdaRecovery(block *Node, source []byte, lang *Language) bool {
	if block == nil || lang == nil || block.Type(lang) != "block" || block.startByte >= block.endByte || int(block.endByte) > len(source) {
		return false
	}
	for _, child := range block.children {
		if child == nil || child.Type(lang) != "local_declaration_statement" || child.startByte >= child.endByte || int(child.endByte) > len(source) {
			continue
		}
		stmtSource := source[child.startByte:child.endByte]
		if !bytes.Contains(stmtSource, []byte("scoped")) || !bytes.Contains(stmtSource, []byte("=>")) {
			continue
		}
		if end, ok := csharpFirstStatementEndInRange(source, child.startByte, block.endByte); ok && end < child.endByte {
			return true
		}
	}
	return false
}

func normalizeCSharpSplitScopedLambdaStatements(root *Node, source []byte, lang *Language) {
	if root == nil || lang == nil || lang.Name != "c_sharp" || len(source) == 0 {
		return
	}
	walkResultTree(root, func(n *Node) {
		if n.Type(lang) == "block" {
			csharpSplitScopedLambdaStatementChildren(n, source, lang)
		}
	})
}

func csharpSplitScopedLambdaStatementChildren(block *Node, source []byte, lang *Language) bool {
	if block == nil || lang == nil || block.ownerArena == nil || len(block.children) == 0 {
		return false
	}
	var rebuilt []*Node
	var rebuiltFields []FieldID
	hadFields := len(block.fieldIDs()) > 0
	changed := false
	for i, child := range block.children {
		replacements, ok := csharpSplitScopedLambdaStatementChild(child, source, lang, block.ownerArena)
		if !ok {
			rebuilt = append(rebuilt, child)
			if hadFields {
				rebuiltFields = append(rebuiltFields, csharpFieldIDAt(block, i))
			}
			continue
		}
		changed = true
		rebuilt = append(rebuilt, replacements...)
		if hadFields {
			for range replacements {
				rebuiltFields = append(rebuiltFields, 0)
			}
		}
	}
	if !changed {
		return false
	}
	children := block.ownerArena.allocNodeSlice(len(rebuilt))
	copy(children, rebuilt)
	block.children = children
	if hadFields {
		fieldIDs := cloneFieldIDSliceInArena(block.ownerArena, rebuiltFields)
		block.setFieldMetadata(fieldIDs, defaultFieldSourcesInArena(block.ownerArena, fieldIDs))
	} else {
		block.clearFieldMetadata()
	}
	block.setHasError(false)
	populateParentNode(block, block.children)
	return true
}

func csharpSplitScopedLambdaStatementChild(child *Node, source []byte, lang *Language, arena *nodeArena) ([]*Node, bool) {
	if child == nil || lang == nil || arena == nil || child.Type(lang) != "local_declaration_statement" ||
		child.startByte >= child.endByte || int(child.endByte) > len(source) {
		return nil, false
	}
	stmtSource := source[child.startByte:child.endByte]
	if !bytes.Contains(stmtSource, []byte("scoped")) || !bytes.Contains(stmtSource, []byte("=>")) {
		return nil, false
	}
	if end, ok := csharpFirstStatementEndInRange(source, child.startByte, child.endByte); !ok || end >= child.endByte {
		return nil, false
	}
	relSpans := csharpTopLevelChunkSpans(stmtSource)
	if len(relSpans) < 2 {
		return nil, false
	}
	replacements := make([]*Node, 0, len(relSpans))
	for _, rel := range relSpans {
		start := child.startByte + rel[0]
		end := child.startByte + rel[1]
		stmt, ok := csharpRecoverScopedLambdaLocalDeclarationStatementFromRange(source, start, end, lang, arena)
		if !ok {
			return nil, false
		}
		replacements = append(replacements, stmt)
	}
	return replacements, true
}

func csharpFieldIDAt(n *Node, index int) FieldID {
	fieldIDs := n.fieldIDs()
	if index < 0 || index >= len(fieldIDs) {
		return 0
	}
	return fieldIDs[index]
}

func csharpRecoverScopedLambdaBlock(block *Node, source []byte, p *Parser) (*Node, bool) {
	if block == nil || p == nil || p.language == nil || block.startByte >= block.endByte || int(block.endByte) > len(source) || block.ownerArena == nil {
		return nil, false
	}
	openBrace := block.startByte
	if source[openBrace] != '{' {
		found := false
		for i := block.startByte; i < block.endByte && int(i) < len(source); i++ {
			if source[i] == '{' {
				openBrace = i
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	if block.endByte == 0 || source[block.endByte-1] != '}' {
		return nil, false
	}
	closeBrace := block.endByte - 1
	statements, ok := csharpRecoverMethodBlockStatementsFromRange(source, openBrace+1, closeBrace, p, block.ownerArena)
	if !ok {
		return nil, false
	}
	return csharpBuildRecoveredMethodBlockNode(source, p.language, block.ownerArena, openBrace, closeBrace, statements)
}

func csharpFirstStatementEndInRange(source []byte, start, end uint32) (uint32, bool) {
	if start >= end || int(end) > len(source) {
		return 0, false
	}
	spans := csharpTopLevelChunkSpans(source[start:end])
	if len(spans) == 0 {
		return 0, false
	}
	return start + spans[0][1], true
}
