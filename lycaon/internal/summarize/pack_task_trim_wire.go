package summarize

// TrimWirePackMapOneStep removes one low-value row from stored pack JSON.
func TrimWirePackMapOneStep(task string, pack map[string]any) bool {
	if pack == nil {
		return false
	}
	for _, key := range []string{"imports", "call_sites", "neighbors"} {
		if _, ok := pack[key]; ok {
			delete(pack, key)
			return true
		}
	}
	substance, _ := pack["substance"].([]any)
	skeleton, _ := pack["skeleton"].([]any)
	if idx, ok := lowestUnbackedSymbolIndex(task, wirePackSymbols(skeleton), wirePackWindows(substance)); ok {
		pack["skeleton"] = append(skeleton[:idx:idx], skeleton[idx+1:]...)
		return true
	}
	if len(substance) > 1 {
		windows := wirePackWindows(substance)
		idx := lowestWindowIndex(task, windows)
		pack["substance"] = append(substance[:idx:idx], substance[idx+1:]...)
		return true
	}
	if len(skeleton) > 0 {
		syms := wirePackSymbols(skeleton)
		if idx, ok := lowestSkeletonDropIndex(task, syms, false); ok {
			pack["skeleton"] = append(skeleton[:idx:idx], skeleton[idx+1:]...)
			return true
		}
	}
	if len(substance) > 0 {
		delete(pack, "substance")
		return true
	}
	if idx, ok := lowestSkeletonDropIndex(task, wirePackSymbols(skeleton), true); ok {
		pack["skeleton"] = append(skeleton[:idx:idx], skeleton[idx+1:]...)
		return true
	}
	return false
}

func wirePackWindows(rows []any) []PackWindow {
	out := make([]PackWindow, len(rows))
	for i, r := range rows {
		m, _ := r.(map[string]any)
		if m == nil {
			continue
		}
		w := PackWindow{}
		w.StartLine = wirePackLine(m["start_line"])
		w.EndLine = wirePackLine(m["end_line"])
		if p, ok := m["path"].(string); ok {
			w.Path = p
		}
		if sym, ok := m["symbol"].(string); ok {
			w.Symbol = sym
		}
		if body, ok := m["body"].(string); ok {
			w.Body = body
		}
		out[i] = w
	}
	return out
}

func wirePackSymbols(rows []any) []PackSymbol {
	out := make([]PackSymbol, len(rows))
	for i, r := range rows {
		m, _ := r.(map[string]any)
		if m == nil {
			continue
		}
		s := PackSymbol{}
		s.Line = wirePackLine(m["line"])
		if p, ok := m["path"].(string); ok {
			s.Path = p
		}
		if k, ok := m["kind"].(string); ok {
			s.Kind = k
		}
		if n, ok := m["name"].(string); ok {
			s.Name = n
		}
		out[i] = s
	}
	return out
}

func wirePackLine(value any) int {
	switch n := value.(type) {
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}
