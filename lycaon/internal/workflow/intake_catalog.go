package workflow

import (
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
)

// IntakeQuestion is a catalog intake entry.
type IntakeQuestion struct {
	Key      string
	Question string
	Options  []string
}

// IntakeCatalog holds keyed intake questions.
type IntakeCatalog struct {
	entries map[string]IntakeQuestion
}

// LoadIntakeCatalog reads all intake entries under dir.
func LoadIntakeCatalog(dir extpacks.Source) (*IntakeCatalog, error) {
	if dir.Empty() {
		dir = extpacks.Bundled(config.SharedIntake)
	}
	ents, err := dir.List()
	if err != nil {
		return nil, err
	}
	out := &IntakeCatalog{entries: map[string]IntakeQuestion{}}
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		key := ent.Name()
		q, err := loadIntakeQuestionFile(dir.Join(key, "question.yaml"))
		if err != nil {
			return nil, fmt.Errorf("intake %q: %w", key, err)
		}
		q.Key = key
		out.entries[key] = q
	}
	return out, nil
}

func loadIntakeQuestionFile(path extpacks.Source) (IntakeQuestion, error) {
	data, err := path.Read()
	if err != nil {
		return IntakeQuestion{}, err
	}
	var raw struct {
		Question string   `yaml:"question"`
		Options  []string `yaml:"options"`
	}
	if err := config.DecodeYAML(data, &raw); err != nil {
		return IntakeQuestion{}, err
	}
	question := strings.TrimSpace(raw.Question)
	if question == "" {
		return IntakeQuestion{}, fmt.Errorf("question required")
	}
	options := make([]string, 0, len(raw.Options))
	seen := map[string]bool{}
	for _, option := range raw.Options {
		option = strings.TrimSpace(option)
		if option == "" || seen[option] {
			continue
		}
		seen[option] = true
		options = append(options, option)
	}
	if len(options) == 0 {
		return IntakeQuestion{}, fmt.Errorf("options required")
	}
	return IntakeQuestion{Question: question, Options: options}, nil
}

// Get returns one intake entry by key.
func (c *IntakeCatalog) Get(key string) (IntakeQuestion, bool) {
	if c == nil {
		return IntakeQuestion{}, false
	}
	q, ok := c.entries[strings.TrimSpace(key)]
	return q, ok
}

var bundledIntakeCatalog = sync.OnceValues(func() (*IntakeCatalog, error) {
	return LoadIntakeCatalog(extpacks.Bundled(config.SharedIntake))
})
