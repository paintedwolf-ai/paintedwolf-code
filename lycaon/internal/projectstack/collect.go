package projectstack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/repoinfo"
)

const stackSeedQueryTerms = 20

// StackSignals is the bounded, deterministic project fingerprint for warming.
type StackSignals struct {
	Languages   []string
	Deps        []Dep
	DocURLs     []string
	Fingerprint string
}

// Collect gathers languages, manifest deps, and doc URLs for a project root.
func Collect(ctx context.Context, root string, repo repoinfo.Provider) (StackSignals, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return StackSignals{}, nil
	}
	var langs []string
	if repo != nil {
		brief, err := repo.Brief(ctx, root)
		if err != nil {
			return StackSignals{}, err
		}
		if brief != nil && len(brief.Languages) > 0 {
			langs = append([]string(nil), brief.Languages...)
			if len(langs) > repoinfo.MaxLanguages {
				langs = langs[:repoinfo.MaxLanguages]
			}
		}
	}
	manifests, err := discoverManifests(ctx, root)
	if err != nil {
		return StackSignals{}, err
	}
	deps := depsFromManifests(root, manifests)
	docURLs := docURLsFromRoot(root)
	fp := fingerprint(root, langs, manifests, docURLs)
	return StackSignals{
		Languages:   langs,
		Deps:        deps,
		DocURLs:     docURLs,
		Fingerprint: fp,
	}, nil
}

// SeedQuery builds the deterministic Summarizer seed string from stack signals.
// Dependency names take precedence; languages fill remaining term budget.
func SeedQuery(s StackSignals) string {
	terms := seedQueryTerms(s)
	if len(terms) == 0 {
		return ""
	}
	return "official documentation for " + strings.Join(terms, ", ")
}

func seedQueryTerms(s StackSignals) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(term string) {
		term = strings.TrimSpace(term)
		if term == "" || len(out) >= stackSeedQueryTerms {
			return
		}
		key := strings.ToLower(term)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, term)
	}
	for _, dep := range s.Deps {
		add(dep.Name)
	}
	for _, lang := range s.Languages {
		add(lang)
	}
	return out
}

func fingerprint(root string, langs, manifestRels, docURLs []string) string {
	h := sha256.New()
	if len(langs) > 0 {
		h.Write([]byte("langs:"))
		h.Write([]byte(strings.Join(langs, ",")))
	}
	for _, rel := range manifestRels {
		data, err := os.ReadFile(manifestAbs(root, rel)) // #nosec G703 -- rel from discover under user root
		if err != nil {
			continue
		}
		h.Write([]byte(rel))
		h.Write(data)
	}
	if len(docURLs) > 0 {
		sorted := append([]string(nil), docURLs...)
		sort.Strings(sorted)
		h.Write([]byte("docs:"))
		h.Write([]byte(strings.Join(sorted, "\n")))
	}
	// Doc file heads contribute via extracted URLs; also hash raw heads for
	// prose-free stability when links unchanged but file edits occur.
	for _, rel := range stackDocRelPaths {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) // #nosec G703 -- fixed doc paths under user root
		if err != nil {
			continue
		}
		if len(data) > StackDocPerFileCap {
			data = data[:StackDocPerFileCap]
		}
		h.Write([]byte(rel))
		h.Write(data)
	}
	if h.Size() == 0 {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}
