package secretmatch

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/lycaon/lycaon/config"
)

const maxOCRKeywordVariants = 64

type ocrConfusionFile struct {
	Version int                 `yaml:"version"`
	Classes []ocrConfusionClass `yaml:"classes"`
}

type ocrConfusionClass struct {
	Glyphs []string `yaml:"glyphs"`
	Reason string   `yaml:"reason"`
}

type ocrConfusionCatalog struct {
	classes       [][]rune
	partners      map[rune][]rune
	canonical     map[rune]rune
	lowerPartners map[rune][]rune
}

var (
	ocrConfusionOnce sync.Once
	ocrConfusionData ocrConfusionCatalog
	ocrConfusionErr  error
)

func bundledOCRConfusions() (ocrConfusionCatalog, error) {
	ocrConfusionOnce.Do(func() {
		raw, err := config.Read(config.SecretOCRConfusions)
		if err != nil {
			ocrConfusionErr = fmt.Errorf("secretmatch: ocr confusion catalog: %w", err)
			return
		}
		ocrConfusionData, ocrConfusionErr = parseOCRConfusions(raw)
	})
	return ocrConfusionData, ocrConfusionErr
}

func parseOCRConfusions(raw []byte) (ocrConfusionCatalog, error) {
	var file ocrConfusionFile
	if err := config.DecodeYAML(raw, &file); err != nil {
		return ocrConfusionCatalog{}, fmt.Errorf("secretmatch: ocr confusion catalog: %w", err)
	}
	if file.Version != 1 {
		return ocrConfusionCatalog{}, fmt.Errorf("secretmatch: ocr confusion catalog version %d is not supported", file.Version)
	}
	if len(file.Classes) == 0 {
		return ocrConfusionCatalog{}, errors.New("secretmatch: ocr confusion catalog has no classes")
	}
	cat := ocrConfusionCatalog{
		classes:       make([][]rune, 0, len(file.Classes)),
		partners:      map[rune][]rune{},
		canonical:     map[rune]rune{},
		lowerPartners: map[rune][]rune{},
	}
	for i, cls := range file.Classes {
		if strings.TrimSpace(cls.Reason) == "" {
			return ocrConfusionCatalog{}, fmt.Errorf("secretmatch: ocr confusion class %d requires a reason", i)
		}
		if len(cls.Glyphs) < 2 {
			return ocrConfusionCatalog{}, fmt.Errorf("secretmatch: ocr confusion class %d requires at least two glyphs", i)
		}
		runes := make([]rune, 0, len(cls.Glyphs))
		lowerSet := map[rune]struct{}{}
		for _, g := range cls.Glyphs {
			if utf8.RuneCountInString(g) != 1 {
				return ocrConfusionCatalog{}, fmt.Errorf("secretmatch: ocr confusion glyph %q must be a single rune", g)
			}
			r, _ := utf8.DecodeRuneInString(g)
			if unicode.IsSpace(r) {
				return ocrConfusionCatalog{}, fmt.Errorf("secretmatch: ocr confusion glyph %q cannot be whitespace", g)
			}
			if _, dup := cat.canonical[r]; dup {
				return ocrConfusionCatalog{}, fmt.Errorf("secretmatch: ocr confusion glyph %q is listed twice", g)
			}
			runes = append(runes, r)
			lowerSet[unicode.ToLower(r)] = struct{}{}
		}
		canon := runes[0]
		sorted := append([]rune(nil), runes...)
		sort.Slice(sorted, func(a, b int) bool { return sorted[a] < sorted[b] })
		for _, r := range runes {
			cat.canonical[r] = canon
			cat.partners[r] = sorted
		}
		lowerSlice := make([]rune, 0, len(lowerSet))
		for lr := range lowerSet {
			lowerSlice = append(lowerSlice, lr)
		}
		sort.Slice(lowerSlice, func(a, b int) bool { return lowerSlice[a] < lowerSlice[b] })
		for _, lr := range lowerSlice {
			cat.lowerPartners[lr] = lowerSlice
		}
		cat.classes = append(cat.classes, sorted)
	}
	return cat, nil
}

func (c ocrConfusionCatalog) canonicalize(s string) string {
	if len(c.canonical) == 0 || s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if canon, ok := c.canonical[r]; ok {
			b.WriteRune(canon)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isOCRPlaceholder(p placeholderCatalog, secret string, c ocrConfusionCatalog) bool {
	if p.IsPlaceholder(secret) {
		return true
	}
	if len(p.values) == 0 {
		return false
	}
	canon := c.canonicalize(secret)
	for val := range p.values {
		if c.canonicalize(val) == canon {
			return true
		}
	}
	return false
}

func acceptOCRRequirements(r kingfisherRequirements, secret string, c ocrConfusionCatalog) bool {
	var digits, uppercase, lowercase int
	for _, ch := range secret {
		partners := c.partners[ch]
		if len(partners) == 0 {
			partners = []rune{ch}
		}
		hasDigit, hasUpper, hasLower := false, false, false
		for _, p := range partners {
			switch {
			case p >= '0' && p <= '9':
				hasDigit = true
			case p >= 'A' && p <= 'Z':
				hasUpper = true
			case p >= 'a' && p <= 'z':
				hasLower = true
			}
		}
		if hasDigit {
			digits++
		}
		if hasUpper {
			uppercase++
		}
		if hasLower {
			lowercase++
		}
	}
	if below(digits, r.MinDigits) || below(uppercase, r.MinUppercase) || below(lowercase, r.MinLowercase) {
		return false
	}
	if r.MinSpecialChars != nil {
		specials := r.SpecialChars
		if specials == "" {
			specials = defaultSpecials
		}
		count := 0
		for _, ch := range secret {
			partners := c.partners[ch]
			if len(partners) == 0 {
				partners = []rune{ch}
			}
			for _, p := range partners {
				if strings.ContainsRune(specials, p) {
					count++
					break
				}
			}
		}
		if count < *r.MinSpecialChars {
			return false
		}
	}
	lowerSecret := strings.ToLower(secret)
	canonLower := strings.ToLower(c.canonicalize(secret))
	for _, term := range r.IgnoreIfContains {
		term = strings.ToLower(strings.TrimSpace(term))
		if term == "" {
			continue
		}
		if strings.Contains(lowerSecret, term) || strings.Contains(canonLower, strings.ToLower(c.canonicalize(term))) {
			return false
		}
	}
	return true
}
