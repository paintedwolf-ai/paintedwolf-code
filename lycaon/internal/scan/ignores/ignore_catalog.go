package ignores

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/projectignore"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

// IgnoreSource says where an entry came from; only project entries are withdrawable.
type IgnoreSource string

const (
	IgnoreSourceBundled IgnoreSource = "bundled"
	IgnoreSourceProject IgnoreSource = "project"
)

// IgnoreRule is one loaded entry with the provenance the loader knows.
type IgnoreRule struct {
	IgnoreEntry
	Source IgnoreSource
	// Origin is the overlay root an entry came from, empty for bundled ones.
	Origin string
}

// The catalog digest invalidates cached ledger verdicts.
type IgnoreCatalog struct {
	Rules  []IgnoreRule
	Digest string
	// Invalid lists refused entries; a bad entry disables only itself.
	Invalid []IgnoreDefect
}

// IgnoreDefect is one refused entry and why.
type IgnoreDefect struct {
	Source IgnoreSource
	Index  int
	ID     string
	Reason string
}

// Match returns the first entry that both matches and is still in force.
func (c *IgnoreCatalog) Match(subject IgnoreSubject, now time.Time) (IgnoreRule, bool) {
	if c == nil {
		return IgnoreRule{}, false
	}
	for _, rule := range c.Rules {
		if rule.Expired(now) {
			continue
		}
		if rule.Matches(subject) {
			return rule, true
		}
	}
	return IgnoreRule{}, false
}

// LoadIgnoreCatalog merges the bundled catalog with each overlay root, in order.
func LoadIgnoreCatalog(rootPaths []string) (*IgnoreCatalog, error) {
	if err := settingsoverlay.CheckFormats(rootPaths); err != nil {
		return nil, err
	}
	bundled, err := config.Read(config.ScanIgnores)
	if err != nil {
		return nil, fmt.Errorf("read bundled ignores: %w", err)
	}
	catalog := &IgnoreCatalog{}
	hash := sha256.New()
	hash.Write(bundled)
	catalog.absorb(bundled, IgnoreSourceBundled, "", string(config.ScanIgnores))

	for _, rootPath := range rootPaths {
		rootPath = strings.TrimSpace(rootPath)
		if rootPath == "" {
			continue
		}
		path := projectignore.Path(rootPath)
		data, err := projectignore.Read(rootPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		if err := settingsoverlay.CheckFormat(rootPath); err != nil {
			return nil, err
		}
		hash.Write([]byte(path))
		hash.Write([]byte(projectignore.SectionDigest(data, "findings")))
		catalog.absorb(data, IgnoreSourceProject, rootPath, path)
	}
	catalog.Digest = hex.EncodeToString(hash.Sum(nil))
	return catalog, nil
}

// Invalid declarations produce defects without stopping the scan.
func (c *IgnoreCatalog) absorb(data []byte, source IgnoreSource, origin, path string) {
	entries, err := decodeFindingEntries(data)
	if err != nil {
		c.Invalid = append(c.Invalid, IgnoreDefect{Source: source, Index: -1, Reason: fmt.Sprintf("%s: %v", path, err)})
		return
	}
	for index, entry := range entries {
		if err := entry.Validate(); err != nil {
			c.Invalid = append(c.Invalid, IgnoreDefect{
				Source: source, Index: index, ID: entry.ID, Reason: err.Error(),
			})
			continue
		}
		c.Rules = append(c.Rules, IgnoreRule{IgnoreEntry: entry, Source: source, Origin: origin})
	}
}

func decodeFindingEntries(data []byte) ([]IgnoreEntry, error) {
	doc, err := projectignore.Parse(data)
	if err != nil {
		return nil, err
	}
	node, err := projectignore.Section(doc, "findings")
	if err != nil {
		return nil, err
	}
	var entries []IgnoreEntry
	// Entry decoding is isolated so a mistyped entry does not disable its peers.
	for _, item := range node.Content {
		var entry IgnoreEntry
		raw, err := yaml.Marshal(item)
		if err == nil {
			err = config.DecodeYAML(raw, &entry)
		}
		if err != nil {
			entry.decodeError = err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
