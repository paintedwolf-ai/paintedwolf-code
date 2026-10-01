package packageexec

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/sensitivepath"
)

const (
	lookupTimeout       = 8 * time.Second
	maxLookupBody       = 4 << 20
	maxSensitiveEntries = 20_000
	maxSensitiveReads   = 64
)

var errIdentityNotFound = errors.New("package identity not found")

// Service classifies command plans and resolves package identities for approval.
type Service struct {
	catalog   *catalog
	client    *http.Client
	now       func() time.Time
	sensitive *sensitivepath.Catalog
}

// NewService loads the bundled manager and sensitive-path catalogs.
func NewService() (*Service, error) {
	cat, err := loadCatalog()
	if err != nil {
		return nil, err
	}
	sensitive, err := sensitivepath.Load(sensitivepath.Bundled())
	if err != nil {
		return nil, fmt.Errorf("package execution sensitive paths: %w", err)
	}
	return &Service{
		catalog:   cat,
		client:    httpclient.Bounded(egressclass.PackageIdentityLookup, lookupTimeout),
		now:       time.Now,
		sensitive: sensitive,
	}, nil
}

// Preflight resolves identity for recognized package actions.
func (s *Service) Preflight(ctx context.Context, args map[string]any, projectDir string) (*Execution, error) {
	if s == nil || s.catalog == nil {
		return nil, nil
	}
	execution, ok := s.classifyArgs(args)
	if !ok {
		return nil, nil
	}
	for i := range execution.Packages {
		s.resolve(ctx, &execution.Packages[i])
	}
	execution.AllowedHosts = normalizedStrings(append(execution.AllowedHosts, registryHosts(execution.Packages)...))
	execution.SensitiveReads = normalizedStrings(append(
		s.sensitive.ProtectedPathRoots(sensitivepath.ModeRead),
		s.sensitiveProjectReads(projectDir)...,
	))
	return execution, nil
}

// classifyArgs accepts command plans parsed by the command boundary.
func (s *Service) classifyArgs(args map[string]any) (*Execution, bool) {
	plan, err := commandsurface.ParsePlan(args)
	if err != nil {
		return nil, false
	}
	return s.catalog.classify(plan.Stages)
}

// ClassifyTerminalInput identifies package execution in PTY input.
func (s *Service) ClassifyTerminalInput(input string) (*Execution, bool) {
	if s == nil || s.catalog == nil {
		return nil, false
	}
	normalized := strings.ReplaceAll(input, "{Enter}", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	for _, line := range strings.Split(normalized, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		plan, err := commandsurface.ParsePlan(map[string]any{"command": line})
		if err != nil {
			continue
		}
		if execution, ok := s.catalog.classify(plan.Stages); ok {
			return execution, true
		}
	}
	return nil, false
}

func (s *Service) resolve(ctx context.Context, pkg *Package) {
	if pkg == nil {
		return
	}
	if strings.TrimSpace(pkg.System) == "" {
		pkg.Status = IdentityUnsupported
		pkg.StatusDetail = "This ecosystem is not indexed by the identity provider."
		pkg.ResolutionNonce = resolutionNonce()
		return
	}
	resolved := strings.TrimSpace(pkg.RequestedVersion)
	if resolved == "" || strings.EqualFold(resolved, "latest") {
		candidate, err := s.defaultVersion(ctx, pkg.System, pkg.Name)
		if err != nil {
			setLookupFailure(pkg, err)
			return
		}
		resolved = candidate
	}
	version, err := s.version(ctx, pkg.System, pkg.Name, resolved)
	if err != nil {
		setLookupFailure(pkg, err)
		return
	}
	pkg.System = firstNonEmpty(version.VersionKey.System, pkg.System)
	pkg.Name = firstNonEmpty(version.VersionKey.Name, pkg.Name)
	pkg.ResolvedVersion = firstNonEmpty(version.VersionKey.Version, resolved)
	pkg.Status = IdentityResolved
	pkg.Registry = firstString(version.Registries)
	pkg.SourceRepository = sourceRepository(version)
	for _, attestation := range version.Attestations {
		if attestation.Verified {
			pkg.VerifiedAttestation = true
			if pkg.SourceRepository == "" {
				pkg.SourceRepository = strings.TrimSpace(attestation.SourceRepository)
			}
			break
		}
	}
	if published := strings.TrimSpace(version.PublishedAt); published != "" {
		if parsed, parseErr := time.Parse(time.RFC3339, published); parseErr == nil {
			parsed = parsed.UTC()
			pkg.PublishedAt = &parsed
			days := int(s.now().UTC().Sub(parsed).Hours() / 24)
			if days < 0 {
				days = 0
			}
			pkg.AgeDays = &days
		}
	}
}

func setLookupFailure(pkg *Package, err error) {
	if errors.Is(err, errIdentityNotFound) {
		pkg.Status = IdentityNotFound
		pkg.StatusDetail = "This package or version was not found in the registry identity index."
	} else {
		pkg.Status = IdentityUnavailable
		pkg.StatusDetail = lookupDetail(err)
	}
	pkg.ResolutionNonce = resolutionNonce()
}

type packageResponse struct {
	Versions []struct {
		VersionKey versionKey `json:"versionKey"`
		IsDefault  bool       `json:"isDefault"`
	} `json:"versions"`
}

type versionKey struct {
	System  string `json:"system"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type versionResponse struct {
	VersionKey  versionKey `json:"versionKey"`
	PublishedAt string     `json:"publishedAt"`
	Registries  []string   `json:"registries"`
	Links       []struct {
		Label string `json:"label"`
		URL   string `json:"url"`
	} `json:"links"`
	Attestations []struct {
		Verified         bool   `json:"verified"`
		SourceRepository string `json:"sourceRepository"`
	} `json:"attestations"`
	RelatedProjects []struct {
		ProjectKey struct {
			ID string `json:"id"`
		} `json:"projectKey"`
		RelationType string `json:"relationType"`
	} `json:"relatedProjects"`
}

func (s *Service) defaultVersion(ctx context.Context, system, name string) (string, error) {
	var response packageResponse
	if err := s.getJSON(ctx, packageURL(system, name), &response); err != nil {
		return "", err
	}
	for _, version := range response.Versions {
		if version.IsDefault && strings.TrimSpace(version.VersionKey.Version) != "" {
			return version.VersionKey.Version, nil
		}
	}
	return "", fmt.Errorf("default version was not reported")
}

func (s *Service) version(ctx context.Context, system, name, version string) (versionResponse, error) {
	var response versionResponse
	err := s.getJSON(ctx, packageURL(system, name)+"/versions/"+url.PathEscape(version), &response)
	return response, err
}

func packageURL(system, name string) string {
	return egressclass.PackageIdentityEndpoint + "/v3/systems/" +
		url.PathEscape(strings.ToUpper(strings.TrimSpace(system))) + "/packages/" + url.PathEscape(strings.TrimSpace(name))
}

func (s *Service) getJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		if resp.StatusCode == http.StatusNotFound {
			return errIdentityNotFound
		}
		return fmt.Errorf("identity service returned HTTP %d", resp.StatusCode)
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxLookupBody))
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode identity response: %w", err)
	}
	return nil
}

func (s *Service) sensitiveProjectReads(projectDir string) []string {
	if s == nil || s.sensitive == nil || strings.TrimSpace(projectDir) == "" {
		return nil
	}
	root, err := filepath.Abs(projectDir)
	if err != nil {
		return nil
	}
	var out []string
	visited := 0
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		visited++
		if visited > maxSensitiveEntries || len(out) >= maxSensitiveReads {
			return fs.SkipAll
		}
		if entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if match, ok := s.sensitive.Match(path, sensitivepath.ModeRead); ok && match.Protected {
			out = append(out, path)
		}
		return nil
	})
	return normalizedStrings(out)
}

func registryHosts(packages []Package) []string {
	var out []string
	for _, pkg := range packages {
		u, err := url.Parse(pkg.Registry)
		if err == nil && u.Hostname() != "" {
			out = append(out, strings.ToLower(u.Hostname()))
		}
	}
	return out
}

func sourceRepository(version versionResponse) string {
	for _, link := range version.Links {
		label := strings.ToUpper(strings.TrimSpace(link.Label))
		if label == "SOURCE_REPO" || label == "SOURCE_REPOSITORY" || label == "ORIGIN" {
			return strings.TrimSpace(link.URL)
		}
	}
	for _, project := range version.RelatedProjects {
		if id := strings.TrimSpace(project.ProjectKey.ID); id != "" {
			return id
		}
	}
	return ""
}

func resolutionNonce() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("unresolved-%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func lookupDetail(err error) string {
	if err == nil {
		return "Package identity was unavailable."
	}
	text := strings.TrimSpace(err.Error())
	if len(text) > 160 {
		text = text[:160]
	}
	return text
}

func firstString(items []string) string {
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			return item
		}
	}
	return ""
}

func firstNonEmpty(items ...string) string {
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			return item
		}
	}
	return ""
}
