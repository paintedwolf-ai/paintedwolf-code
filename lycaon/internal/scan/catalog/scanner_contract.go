package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/scan/bundled"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/scan/rules"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/version"
	"github.com/lycaon/lycaon/pkg/api"
)

type scannerPolicyDigestPair struct {
	rules      string
	exclusions string
}

var scannerPolicyDigestCache sync.Map

var (
	hostExecutableIdentityOnce sync.Once
	hostExecutableDigest       string
)

// ScopeKind is the closed source-coverage contract a scanner declares.
type ScopeKind string

const (
	ScopeSourceHostFloor ScopeKind = "source_host_floor"
	ScopeSourceDriver    ScopeKind = "source_driver"
	ScopeDependencies    ScopeKind = "dependencies"
	ScopeSecrets         ScopeKind = "secrets"
	ScopeContainer       ScopeKind = "container"
	ScopeCustom          ScopeKind = "custom"
)

// ScannerContract is the immutable execution identity captured with a scan.
type ScannerContract struct {
	AnalysisMode          string
	ScannerID             string
	Engine                string
	Driver                string
	Scope                 ScopeKind
	DefinitionFingerprint string
	ParserID              string
	MapperID              string
	RulesSHA256           string
	ExclusionsSHA256      string
	EngineVersion         string
	EngineSHA256          string
	EngineExecutable      string
	Runtime               RuntimePolicy
}

// Contract returns the typed execution identity for this exact catalog row.
func (e ScannerEntry) Contract() ScannerContract {
	parserID := strings.TrimSpace(e.OutputParser)
	if parserID == "" && e.Impl == bundled.ImplOpengrep {
		parserID = scanoutput.OutputParserOpengrepJSON
	}
	rulesDigest, exclusionsDigest := scannerPolicyDigests(e)
	engineVersion, engineDigest := scannerEngineIdentity(e)
	engineExecutable := ""
	if e.Driver == DriverExternal && len(e.Command) > 0 {
		engineExecutable = strings.TrimSpace(e.Command[0])
	}
	analysisMode := ""
	if e.Impl == bundled.ImplOpengrep {
		if cfg, err := rules.LoadOpengrepGates(); err == nil {
			analysisMode = string(cfg.Analysis.Mode)
		}
	}
	return ScannerContract{
		AnalysisMode:          analysisMode,
		ScannerID:             strings.TrimSpace(e.ID),
		Engine:                strings.TrimSpace(e.Engine),
		Driver:                strings.TrimSpace(e.Driver),
		Scope:                 ScopeKind(strings.TrimSpace(e.ScopeKind)),
		DefinitionFingerprint: scannerDefinitionFingerprint(e),
		ParserID:              parserID,
		MapperID:              strings.TrimSpace(e.MapperID),
		RulesSHA256:           rulesDigest,
		ExclusionsSHA256:      exclusionsDigest,
		EngineVersion:         engineVersion,
		EngineSHA256:          engineDigest,
		EngineExecutable:      engineExecutable,
		Runtime:               e.RuntimePolicy(),
	}
}

// Valid reports whether a contract came from a validated scanner definition.
func (c ScannerContract) Valid() bool {
	return strings.TrimSpace(c.ScannerID) != "" &&
		strings.TrimSpace(c.Engine) != "" &&
		strings.TrimSpace(c.Driver) != "" &&
		validScopeKind(c.Scope) &&
		validDefinitionFingerprint(c.DefinitionFingerprint)
}

func validDefinitionFingerprint(value string) bool {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	return err == nil && len(decoded) == sha256.Size
}

func scannerDefinitionFingerprint(e ScannerEntry) string {
	var canonical strings.Builder
	write := func(value string) {
		canonical.WriteString(strconv.Itoa(len(value)))
		canonical.WriteByte(':')
		canonical.WriteString(value)
	}
	writeList := func(values []string) {
		write(strconv.Itoa(len(values)))
		for _, value := range values {
			write(value)
		}
	}
	for _, value := range []string{
		e.ID, e.Driver, e.Impl, e.Engine, e.ScopeKind, e.Config,
		e.OutputParser, e.MapperID, e.Workdir,
	} {
		write(value)
	}
	writeList(e.Categories)
	writeList(e.Command)
	writeList(e.Env)
	rulesDigest, exclusionsDigest := scannerPolicyDigests(e)
	engineVersion, engineDigest := scannerEngineIdentity(e)
	write(rulesDigest)
	write(exclusionsDigest)
	write(engineVersion)
	write(engineDigest)
	if e.Driver == DriverBundled {
		// Source projections and result interpretation execute in the host adapter.
		_, adapterDigest := hostExecutableIdentity()
		if adapterDigest == "" {
			return ""
		}
		write(adapterDigest)
	}
	write(string(e.CatalogSource))
	write(strconv.FormatBool(e.SkipIfBinaryMissingOrDefault()))
	runtime := e.RuntimePolicy()
	write(strconv.Itoa(runtime.SoftLimitSec))
	write(strconv.Itoa(runtime.HardLimitSec))
	write(strconv.Itoa(runtime.CPUUnits))
	write(strconv.Itoa(runtime.Parallelism))
	write(strconv.Itoa(len(e.OkExitCodes)))
	for _, code := range e.OkExitCodes {
		write(strconv.Itoa(code))
	}
	sum := sha256.Sum256([]byte(canonical.String()))
	return hex.EncodeToString(sum[:])
}

// ExecutionManifest freezes the scanner inputs before a job can be claimed.
func ExecutionManifest(contract ScannerContract) (api.ScanExecutionManifest, string, error) {
	if !contract.Valid() {
		return api.ScanExecutionManifest{}, "", fmt.Errorf("valid scanner contract required")
	}
	engineDigest := contract.EngineSHA256
	switch contract.Driver {
	case DriverExternal:
		var err error
		engineDigest, err = executableSHA256(contract.EngineExecutable)
		if err != nil {
			return api.ScanExecutionManifest{}, "", fmt.Errorf("capture scanner executable: %w", err)
		}
	case DriverLibrary:
		if engineDigest == "" {
			_, engineDigest = hostExecutableIdentity()
		}
	}
	if (contract.Driver == DriverLibrary || contract.Driver == DriverBundled) && engineDigest == "" {
		return api.ScanExecutionManifest{}, "", fmt.Errorf("scanner %s has no engine byte identity", contract.ScannerID)
	}
	if contract.Scope == ScopeSourceHostFloor && (contract.RulesSHA256 == "" || contract.ExclusionsSHA256 == "" || engineDigest == "") {
		return api.ScanExecutionManifest{}, "", fmt.Errorf("scanner %s has incomplete host-floor execution identity", contract.ScannerID)
	}
	if contract.Scope == ScopeSecrets && contract.RulesSHA256 == "" {
		return api.ScanExecutionManifest{}, "", fmt.Errorf("scanner %s has no secret-rule identity", contract.ScannerID)
	}
	manifest := api.ScanExecutionManifest{
		SchemaVersion: "v1", ScannerID: contract.ScannerID, Engine: contract.Engine,
		EngineVersion: contract.EngineVersion, EngineSHA256: engineDigest,
		Driver: contract.Driver, ScopeKind: string(contract.Scope), ParserID: contract.ParserID,
		MapperID: contract.MapperID, DefinitionFingerprint: contract.DefinitionFingerprint,
		RulesSHA256: contract.RulesSHA256, ExclusionsSHA256: contract.ExclusionsSHA256,
		FingerprintScheme: api.ScanFingerprintScheme,
		Runtime: api.ScanRuntimePolicy{
			SoftLimitMs: contract.Runtime.Normalized().SoftLimitSec * 1000,
			HardLimitMs: contract.Runtime.Normalized().HardLimitSec * 1000,
			CPUUnits:    contract.Runtime.Normalized().CPUUnits,
			Parallelism: contract.Runtime.Normalized().Parallelism,
		},
	}
	raw, err := surveyjson.Marshal(manifest)
	if err != nil {
		return api.ScanExecutionManifest{}, "", err
	}
	sum := sha256.Sum256(raw)
	return manifest, hex.EncodeToString(sum[:]), nil
}

func executableSHA256(command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("executable required")
	}
	path, err := exec.LookPath(command)
	if err != nil {
		return "", err
	}
	file, err := os.Open(path) // #nosec G304 -- resolved selected scanner executable
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func scannerPolicyDigests(e ScannerEntry) (string, string) {
	keyRaw, _ := surveyjson.Marshal(e)
	keySum := sha256.Sum256(keyRaw)
	cacheKey := strconv.FormatUint(config.SourceGeneration(), 10) + ":" + hex.EncodeToString(keySum[:])
	if config.SourceImmutable() {
		if cached, ok := scannerPolicyDigestCache.Load(cacheKey); ok {
			pair := cached.(scannerPolicyDigestPair)
			return pair.rules, pair.exclusions
		}
	}
	var roots []config.Rel
	switch ScopeKind(strings.TrimSpace(e.ScopeKind)) {
	case ScopeSourceHostFloor:
		roots = append(roots, activeOpengrepRuleRoots()...)
	case ScopeSecrets:
		roots = append(roots, config.SecretRulesDir)
	case ScopeSourceDriver, ScopeDependencies, ScopeContainer, ScopeCustom:
	}
	if rel, ok := scannerConfigRel(e.Config); ok {
		roots = append(roots, rel)
	}
	rules := digestConfigRoots(roots...)
	exclusions := ""
	if e.ScopeKind == string(ScopeSourceHostFloor) || e.ScopeKind == string(ScopeSourceDriver) {
		exclusions = digestConfigRoots(config.ScanExcludes)
	}
	pair := scannerPolicyDigestPair{rules: rules, exclusions: exclusions}
	if config.SourceImmutable() {
		scannerPolicyDigestCache.Store(cacheKey, pair)
	}
	return pair.rules, pair.exclusions
}

func activeOpengrepRuleRoots() []config.Rel {
	gates, err := rules.LoadOpengrepGates()
	if err != nil {
		return nil
	}
	roots := []config.Rel{config.OpengrepGates}
	paths := append(rules.ActiveVendorGatePaths(gates), rules.ActiveLycaonGatePaths(gates)...)
	for _, path := range paths {
		if rel, ok := scannerConfigRel(path); ok {
			roots = append(roots, rel)
		}
	}
	return roots
}

func scannerConfigRel(raw string) (config.Rel, bool) {
	value := strings.TrimPrefix(strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/")), "/")
	if !strings.HasPrefix(value, "config/") {
		return "", false
	}
	return config.Rel(strings.TrimPrefix(value, "config/")), true
}

func digestConfigRoots(roots ...config.Rel) string {
	type part struct {
		path string
		data []byte
	}
	parts := make([]part, 0)
	for _, root := range roots {
		if root == "" {
			continue
		}
		info, err := config.Info(root)
		if err != nil {
			return ""
		}
		if !info.IsDir() {
			data, readErr := config.Read(root)
			if readErr != nil {
				return ""
			}
			parts = append(parts, part{path: string(root), data: data})
			continue
		}
		walkErr := config.Walk(root, func(rel config.Rel, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			data, readErr := config.Read(rel)
			if readErr != nil {
				return readErr
			}
			parts = append(parts, part{path: string(rel), data: data})
			return nil
		})
		if walkErr != nil {
			return ""
		}
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].path < parts[j].path })
	hash := sha256.New()
	for _, item := range parts {
		_, _ = hash.Write([]byte(strconv.Itoa(len(item.path)) + ":" + item.path))
		_, _ = hash.Write([]byte(strconv.Itoa(len(item.data)) + ":"))
		_, _ = hash.Write(item.data)
	}
	if len(parts) == 0 {
		return ""
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func scannerEngineIdentity(e ScannerEntry) (string, string) {
	if e.Impl == bundled.ImplOpengrep {
		manifest, err := bundled.LoadManifest()
		if err != nil {
			return "", ""
		}
		artifact, err := manifest.ArtifactForCurrentPlatform()
		if err != nil {
			return strings.TrimSpace(manifest.OpenGrep.Version), ""
		}
		return strings.TrimSpace(manifest.OpenGrep.Version), strings.TrimSpace(artifact.SHA256)
	}
	if e.Driver == DriverLibrary || e.Driver == DriverBundled {
		return hostExecutableIdentity()
	}
	return "", ""
}

func hostExecutableIdentity() (string, string) {
	hostExecutableIdentityOnce.Do(func() {
		path, err := os.Executable()
		if err != nil {
			return
		}
		hostExecutableDigest, _ = executableSHA256(path)
	})
	return version.Version, hostExecutableDigest
}

func validScopeKind(scope ScopeKind) bool {
	switch scope {
	case ScopeSourceHostFloor, ScopeSourceDriver, ScopeDependencies, ScopeSecrets, ScopeContainer, ScopeCustom:
		return true
	default:
		return false
	}
}
