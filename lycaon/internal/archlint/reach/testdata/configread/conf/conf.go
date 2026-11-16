package conf

import (
	"encoding/json"

	"gopkg.in/yaml.v3"
)

// Config is what the product decodes.
type Config struct {
	Used   string `yaml:"used"`
	Unused string `yaml:"unused"`
	Why    string `yaml:"why" archlint:"unread"`
	Nested Nested `yaml:"nested"`
	Self   Custom `yaml:"self"`
	Shown  Shown  `yaml:"shown"`
	hidden string
}

// Nested is decoded under Config.
type Nested struct {
	Depth int `yaml:"depth"`
	Width int `yaml:"width"`
}

// Custom decodes itself, so its fields are its own business.
type Custom struct {
	Raw   string
	Extra string
}

func (c *Custom) UnmarshalYAML(n *yaml.Node) error { return n.Decode(&c.Raw) }

// Shown is serialized whole, which reads every field.
type Shown struct {
	Title string `yaml:"title"`
}

// decode hands its target to the decoder, so callers name the type.
func decode(data []byte, target any) error { return yaml.Unmarshal(data, target) }

// Load reads the configuration.
func Load(data []byte) (Config, error) {
	var c Config
	err := decode(data, &c)
	return c, err
}

// Describe uses what the product acts on.
func Describe(c Config) string {
	out, _ := json.Marshal(c.Shown)
	return c.Used + c.Self.Raw + string(out) + string(rune(c.Nested.Depth)) + c.hidden
}
