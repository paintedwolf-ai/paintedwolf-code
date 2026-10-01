package gotreesitter

import "testing"

func parseRuntimeHasNormalizationPass(rt ParseRuntime, name string) bool {
	if rt.NormalizationPasses == nil {
		return false
	}
	for _, pass := range *rt.NormalizationPasses {
		if pass.Name == name {
			return true
		}
	}
	return false
}

func seedStaleForestNormalizationStats(parser *Parser) {
	parser.recordNormalizationMetric("stale.before-forest-attempt", 1, 1, 7, 3)
}

func TestForestAttemptResetsNormalizationStatsOnParserReuse(t *testing.T) {
	previous := glrForestEnabled
	glrForestEnabled = true
	t.Cleanup(func() { glrForestEnabled = previous })

	t.Run("success", func(t *testing.T) {
		lang := loadBlobForDecode(t, "json")
		lang.WantsForest = true
		parser := NewParser(lang)
		seedStaleForestNormalizationStats(parser)

		tree, err := parser.Parse([]byte(`[1, 2, 3]`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		defer tree.Release()
		rt := tree.ParseRuntime()
		if !rt.ForestFastPath {
			t.Fatalf("parse did not use the forest path: %s", rt.Summary())
		}
		if parseRuntimeHasNormalizationPass(rt, "stale.before-forest-attempt") {
			t.Fatal("successful forest parse published stale normalization stats")
		}
	})

	t.Run("decline", func(t *testing.T) {
		lang := loadBlobForDecode(t, "json")
		parser := NewParser(lang)
		seedStaleForestNormalizationStats(parser)

		tree, ok := parser.ParseForestExperimental([]byte(`[`))
		if tree != nil {
			tree.Release()
		}
		if ok {
			t.Fatal("invalid JSON unexpectedly produced an experimental forest tree")
		}
		if len(parser.normalizationStats.namedPasses) != 0 {
			t.Fatalf("declined forest attempt retained normalization stats: %+v", parser.normalizationStats.namedPasses)
		}
	})

	t.Run("fallback", func(t *testing.T) {
		lang := loadBlobForDecode(t, "json")
		lang.WantsForest = true
		parser := NewParser(lang)
		seedStaleForestNormalizationStats(parser)

		tree, err := parser.Parse([]byte(`[`))
		if err != nil {
			t.Fatalf("fallback parse: %v", err)
		}
		defer tree.Release()
		rt := tree.ParseRuntime()
		if rt.ForestFastPath {
			t.Fatalf("invalid JSON unexpectedly returned a forest tree: %s", rt.Summary())
		}
		if parseRuntimeHasNormalizationPass(rt, "stale.before-forest-attempt") {
			t.Fatal("forest fallback published stale normalization stats")
		}
	})
}
