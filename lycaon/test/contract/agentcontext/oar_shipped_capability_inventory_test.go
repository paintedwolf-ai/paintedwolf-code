package contract

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"gopkg.in/yaml.v3"
)

type oarInventorySummary struct {
	PolicyFiles         int
	PolicyRules         int
	PolicyOARHints      int
	PolicyLegacyBanners int
	NextStepsFiles      int
	AnchorFiles         int
	PackCounts          map[string]int
	Violations          []oarViolation
}

type decodedNextStepsFile struct {
	rel string
	ns  *oar.NextSteps
}

func auditStockOARRoot(
	t *testing.T,
	vocab *toolvocab.Catalog,
	packsRoot, schemaDir string,
	verifyEffectiveCatalog bool,
) oarInventorySummary {
	t.Helper()
	sum := oarInventorySummary{PackCounts: map[string]int{}}
	var policyPaths, nextStepPaths []string
	walkErr := filepath.WalkDir(packsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, rErr := filepath.Rel(packsRoot, path)
		if rErr != nil {
			return rErr
		}
		slash := filepath.ToSlash(rel)
		parts := strings.Split(slash, "/")
		pack := parts[0]
		if len(parts) >= 2 && parts[0] == "painted-wolf" {
			pack = parts[0] + "/" + parts[1]
		}
		isOARDir := strings.Contains(slash, "/policy/") || strings.Contains(slash, "/host/next-steps/") || strings.Contains(slash, "/host/anchors/")
		if !isOARDir {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			sum.Violations = append(sum.Violations, oarViolation{
				Source: slash, Action: "discovery", Reason: "unrecognized file extension under OAR/policy directory",
			})
			return nil
		}
		sum.PackCounts[pack]++
		switch {
		case strings.Contains(slash, "/policy/"):
			policyPaths = append(policyPaths, path)
		case strings.Contains(slash, "/host/next-steps/"):
			nextStepPaths = append(nextStepPaths, path)
		case strings.Contains(slash, "/host/anchors/"):
			sum.AnchorFiles++
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s: %v", packsRoot, walkErr)
	}
	slices.Sort(policyPaths)
	slices.Sort(nextStepPaths)
	sum.PolicyFiles = len(policyPaths)
	sum.NextStepsFiles = len(nextStepPaths)

	nsRawByCode, nsDecoded := decodeDiscoveredNextSteps(packsRoot, nextStepPaths, &sum.Violations)
	oarEntries, nsUsed := auditDiscoveredPolicies(packsRoot, policyPaths, nsRawByCode, nsDecoded, vocab, &sum)
	for code, nsPath := range nsDecoded {
		if !nsUsed[code] {
			sum.Violations = append(sum.Violations, oarViolation{
				Source: nsPath.rel, Action: code, Reason: "orphan host/next-steps file has no matching compiled OAR rule",
			})
		}
	}
	compileOAREntriesCheck(schemaDir, oarEntries, &sum.Violations)
	if verifyEffectiveCatalog {
		reconcileWithEffectiveCatalog(t, packsRoot, policyPaths, nextStepPaths, &sum.Violations)
	}
	return sum
}

func decodeDiscoveredNextSteps(packsRoot string, paths []string, out *[]oarViolation) (map[string][]byte, map[string]decodedNextStepsFile) {
	rawByCode := make(map[string][]byte, len(paths))
	decoded := make(map[string]decodedNextStepsFile, len(paths))
	for _, p := range paths {
		rel, _ := filepath.Rel(packsRoot, p)
		slash := filepath.ToSlash(rel)
		code := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		raw, err := os.ReadFile(p)
		if err != nil {
			*out = append(*out, oarViolation{Source: slash, Action: code, Reason: err.Error()})
			continue
		}
		ns, dErr := oar.DecodeNextSteps(raw)
		if dErr != nil {
			*out = append(*out, oarViolation{Source: slash, Action: code, Reason: "decode next-steps: " + dErr.Error()})
			continue
		}
		rawByCode[code] = raw
		decoded[code] = decodedNextStepsFile{rel: slash, ns: ns}
	}
	return rawByCode, decoded
}

func auditDiscoveredPolicies(
	packsRoot string,
	policyPaths []string,
	nsRawByCode map[string][]byte,
	nsDecoded map[string]decodedNextStepsFile,
	vocab *toolvocab.Catalog,
	sum *oarInventorySummary,
) ([]hintregistry.Entry, map[string]bool) {
	var oarEntries []hintregistry.Entry
	nsUsed := map[string]bool{}
	for _, p := range policyPaths {
		rel, _ := filepath.Rel(packsRoot, p)
		slash := filepath.ToSlash(rel)
		raw, err := os.ReadFile(p)
		if err != nil {
			sum.Violations = append(sum.Violations, oarViolation{Source: slash, Reason: err.Error()})
			continue
		}
		norm, nErr := extpacks.NormalizePolicyDocument(raw)
		if nErr != nil {
			sum.Violations = append(sum.Violations, oarViolation{Source: slash, Reason: nErr.Error()})
			continue
		}
		var entry guidance.HintEntry
		if uErr := yaml.Unmarshal(norm, &entry); uErr != nil {
			sum.Violations = append(sum.Violations, oarViolation{Source: slash, Reason: uErr.Error()})
			continue
		}
		code := strings.TrimSpace(entry.ID)
		if code == "" {
			code = strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		}
		switch {
		case !entry.IsOAR():
			sum.PolicyLegacyBanners++
		case strings.TrimSpace(entry.Kind) == "hint":
			sum.PolicyOARHints++
			oarEntries = append(oarEntries, hintregistry.Entry{Code: code, Path: extpacks.OnDisk(p), Body: norm})
		default:
			sum.PolicyRules++
			oarEntries = append(oarEntries, hintregistry.Entry{
				Code: code, Path: extpacks.OnDisk(p), Body: norm, NextSteps: nsRawByCode[code],
			})
			if dec, ok := nsDecoded[code]; ok {
				nsUsed[code] = true
				auditNextStepsList(dec.rel, code, dec.ns.Steps, vocab, &sum.Violations)
			}
		}
		auditPolicyDocument(slash, code, entry, vocab, &sum.Violations)
	}
	return oarEntries, nsUsed
}

func compileOAREntriesCheck(schemaDir string, entries []hintregistry.Entry, out *[]oarViolation) {
	if len(entries) == 0 {
		return
	}
	loader, err := oar.NewLoader(schemaDir)
	if err != nil {
		*out = append(*out, oarViolation{Source: schemaDir, Action: "oar.NewLoader", Reason: err.Error()})
		return
	}
	loader.SetDetectors(oar.HostDetectors(nil))
	if _, lErr := loader.LoadEffectivePolicyEntries(entries); lErr != nil {
		*out = append(*out, oarViolation{Source: "oar.LoadEffectivePolicyEntries", Action: "compile", Reason: lErr.Error()})
	}
}

func reconcileWithEffectiveCatalog(t *testing.T, packsRoot string, policyPaths, nextStepPaths []string, out *[]oarViolation) {
	t.Helper()
	catalog, err := extpacks.CatalogForConsumers()
	if err != nil {
		t.Fatalf("CatalogForConsumers: %v", err)
	}
	effEntries, err := hintregistry.ListEffectiveWithCatalog(catalog)
	if err != nil {
		t.Fatalf("ListEffectiveWithCatalog: %v", err)
	}
	hintCfg, err := guidance.LoadHintConfigStock()
	if err != nil {
		t.Fatalf("LoadHintConfigStock: %v", err)
	}
	if len(effEntries) != len(policyPaths) {
		*out = append(*out, oarViolation{
			Source: packsRoot, Action: "policy_inventory",
			Reason: fmt.Sprintf("filesystem has %d policy files but effective catalog loaded %d", len(policyPaths), len(effEntries)),
		})
	}
	if len(hintCfg.HintCodes) != len(policyPaths) {
		*out = append(*out, oarViolation{
			Source: packsRoot, Action: "hint_config_inventory",
			Reason: fmt.Sprintf("filesystem has %d policy files but HintConfig loaded %d", len(policyPaths), len(hintCfg.HintCodes)),
		})
	}
	for _, p := range nextStepPaths {
		code := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		if _, _, ok := catalog.UnitContent(extpacks.NextStepsUnitID(code)); !ok {
			rel, _ := filepath.Rel(packsRoot, p)
			*out = append(*out, oarViolation{
				Source: filepath.ToSlash(rel), Action: code,
				Reason: "next-steps file on disk is absent from effective pack catalog",
			})
		}
	}
}
